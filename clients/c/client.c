// A Claude Messages API client in one C file. libcurl for the transport, a
// JSON scanner written here for the handful of fields the stream carries, and
// the C library's own timezone database for the tool. Streams the reply,
// completes one tool call, reports API errors.
#define _GNU_SOURCE
#include <curl/curl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>

#define VERSION "2023-06-01"
#define TOOLS "[{\"name\":\"local_time\",\"description\":\"The current local time in an IANA timezone.\"," \
  "\"input_schema\":{\"type\":\"object\",\"properties\":{\"timezone\":{\"type\":\"string\"," \
  "\"description\":\"IANA timezone, e.g. Europe/London\"}},\"required\":[\"timezone\"]}}]"

// A growable byte buffer, always NUL-terminated. Every string this program
// builds goes through it: the request bodies, the SSE line buffer, the text
// and tool input assembled from deltas.
typedef struct { char *p; size_t n, cap; } buf;

static void *xrealloc(void *p, size_t n) {
  p = realloc(p, n);
  if (!p) { fputs("out of memory\n", stderr); exit(1); }
  return p;
}

static void put(buf *b, const char *s, size_t n) {
  if (b->n + n + 1 > b->cap) {
    b->cap = (b->n + n + 1) * 2;
    b->p = xrealloc(b->p, b->cap);
  }
  memcpy(b->p + b->n, s, n);
  b->n += n;
  b->p[b->n] = 0;
}

static void puts_(buf *b, const char *s) { put(b, s, strlen(s)); }

// ---- JSON, the reading half -------------------------------------------------
// The API's payloads are small objects with known keys, so this scans rather
// than parses: find a key at the top level of an object, hand back a pointer
// at its value, decode a string when one is wanted. Nothing is copied until
// json_str, and nothing is validated beyond what finding the key needs.

static const char *ws(const char *p) {
  while (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r') p++;
  return p;
}

// Pointer to the byte after one complete JSON value.
static const char *skip(const char *p) {
  p = ws(p);
  if (*p == '"') {
    for (p++; *p && *p != '"'; p++) if (*p == '\\' && p[1]) p++;
    return *p ? p + 1 : p;
  }
  if (*p == '{' || *p == '[') {
    char close = *p == '{' ? '}' : ']';
    p = ws(p + 1);
    while (*p && *p != close) {
      p = skip(p);
      if (*p == ':' || *p == ',') p = ws(p + 1);
    }
    return *p ? p + 1 : p;
  }
  while (*p && !strchr(",}] \t\r\n", *p)) p++;
  return p;
}

// The value for key in the object at p, or NULL. Keys in this protocol carry
// no escapes, so the comparison is on raw bytes.
static const char *get(const char *p, const char *key) {
  if (!p || *(p = ws(p)) != '{') return NULL;
  size_t klen = strlen(key);
  p = ws(p + 1);
  while (*p == '"') {
    const char *k = p + 1, *end = skip(p);
    p = ws(end);
    if (*p != ':') return NULL;
    p = ws(p + 1);
    if ((size_t)(end - 1 - k) == klen && memcmp(k, key, klen) == 0) return p;
    p = ws(skip(p));
    if (*p == ',') p = ws(p + 1);
  }
  return NULL;
}

// True when the value at p is the string lit. Escapes never appear in the
// values this is used on (event and block types, stop reasons).
static int is(const char *p, const char *lit) {
  size_t n = strlen(lit);
  return p && *p == '"' && strncmp(p + 1, lit, n) == 0 && p[n + 1] == '"';
}

static void utf8(buf *b, unsigned cp) {
  char s[4]; int n = 0;
  if (cp < 0x80) s[n++] = cp;
  else if (cp < 0x800) { s[n++] = 0xC0 | cp >> 6; s[n++] = 0x80 | (cp & 0x3F); }
  else if (cp < 0x10000) { s[n++] = 0xE0 | cp >> 12; s[n++] = 0x80 | (cp >> 6 & 0x3F); s[n++] = 0x80 | (cp & 0x3F); }
  else { s[n++] = 0xF0 | cp >> 18; s[n++] = 0x80 | (cp >> 12 & 0x3F); s[n++] = 0x80 | (cp >> 6 & 0x3F); s[n++] = 0x80 | (cp & 0x3F); }
  put(b, s, n);
}

// Decode the string at p into out. Returns 0 when p is not a string.
static int json_str(buf *out, const char *p) {
  if (!p || *p != '"') return 0;
  for (p++; *p && *p != '"'; p++) {
    if (*p != '\\') { put(out, p, 1); continue; }
    switch (*++p) {
      case 'n': put(out, "\n", 1); break;
      case 't': put(out, "\t", 1); break;
      case 'r': put(out, "\r", 1); break;
      case 'b': put(out, "\b", 1); break;
      case 'f': put(out, "\f", 1); break;
      case 'u': {
        unsigned cp = strtoul((char[]){p[1], p[2], p[3], p[4], 0}, NULL, 16);
        p += 4;
        if (cp >= 0xD800 && cp < 0xDC00 && p[1] == '\\' && p[2] == 'u') { // surrogate pair
          unsigned lo = strtoul((char[]){p[3], p[4], p[5], p[6], 0}, NULL, 16);
          cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00);
          p += 6;
        }
        utf8(out, cp);
        break;
      }
      default: put(out, p, 1); // \" \\ \/
    }
  }
  return 1;
}

static char *str(const char *p) {
  buf b = {0};
  if (!json_str(&b, p)) return NULL;
  return b.p;
}

// ---- JSON, the writing half -------------------------------------------------

static void quote(buf *b, const char *s) {
  put(b, "\"", 1);
  for (; *s; s++) {
    unsigned char c = *s;
    if (c == '"' || c == '\\') { put(b, "\\", 1); put(b, s, 1); }
    else if (c == '\n') puts_(b, "\\n");
    else if (c < 0x20) { char e[8]; snprintf(e, sizeof e, "\\u%04x", c); puts_(b, e); }
    else put(b, s, 1);
  }
  put(b, "\"", 1);
}

// ---- The stream --------------------------------------------------------------

typedef struct { int tool; char *id, *name; buf text, input; } block;

typedef struct {
  CURL *curl;
  long status;      // HTTP status, read once the first body bytes arrive
  buf line;         // bytes received and not yet consumed as whole lines
  buf error;        // the body of a non-2xx response
  block *blocks; int nblocks;
  char *stop;
} turn;

static block *at(turn *t, int i) {
  while (i >= t->nblocks) {
    t->blocks = xrealloc(t->blocks, sizeof(block) * (t->nblocks + 1));
    t->blocks[t->nblocks++] = (block){0};
  }
  return &t->blocks[i];
}

static void event(turn *t, const char *ev) {
  const char *type = get(ev, "type"), *idx = get(ev, "index");
  int i = idx ? atoi(idx) : 0;
  if (is(type, "content_block_start")) {
    const char *cb = get(ev, "content_block");
    block *b = at(t, i);
    if (is(get(cb, "type"), "tool_use")) {
      b->tool = 1;
      b->id = str(get(cb, "id"));
      b->name = str(get(cb, "name"));
    }
  } else if (is(type, "content_block_delta")) {
    const char *d = get(ev, "delta");
    block *b = at(t, i);
    if (is(get(d, "type"), "text_delta")) {
      size_t before = b->text.n;
      json_str(&b->text, get(d, "text"));
      fwrite(b->text.p + before, 1, b->text.n - before, stdout);
      fflush(stdout);
    } else if (is(get(d, "type"), "input_json_delta")) {
      json_str(&b->input, get(d, "partial_json"));
    }
  } else if (is(type, "message_delta")) {
    free(t->stop);
    t->stop = str(get(get(ev, "delta"), "stop_reason"));
  }
}

// libcurl hands the body over in whatever pieces the socket delivers. Keep the
// unfinished line, and feed every complete "data:" line to event.
static size_t body(char *data, size_t size, size_t nmemb, void *userdata) {
  turn *t = userdata;
  size_t n = size * nmemb;
  if (!t->status) curl_easy_getinfo(t->curl, CURLINFO_RESPONSE_CODE, &t->status);
  if (t->status < 200 || t->status >= 300) { put(&t->error, data, n); return n; }
  put(&t->line, data, n);
  char *start = t->line.p, *nl;
  while ((nl = memchr(start, '\n', t->line.n - (start - t->line.p)))) {
    *nl = 0;
    if (strncmp(start, "data: ", 6) == 0) event(t, start + 6); // event:, ping and blank lines fall through
    start = nl + 1;
  }
  t->line.n -= start - t->line.p;
  memmove(t->line.p, start, t->line.n + 1);
  return n;
}

static void reset(turn *t) {
  for (int i = 0; i < t->nblocks; i++) {
    free(t->blocks[i].id); free(t->blocks[i].name);
    free(t->blocks[i].text.p); free(t->blocks[i].input.p);
  }
  free(t->blocks); t->blocks = NULL; t->nblocks = 0;
  free(t->stop); t->stop = NULL;
  t->status = 0; t->line.n = 0; t->error.n = 0;
}

// ---- The tool ----------------------------------------------------------------
// libc has read the zoneinfo database for fifty years. Point TZ at the zone,
// let localtime do the transitions, and format with the offset.

static char *local_time(const char *tz) {
  char path[512], out[64];
  struct stat st;
  if (!tz || !*tz || strstr(tz, "..") || *tz == '/') return strdup("unknown timezone");
  snprintf(path, sizeof path, "/usr/share/zoneinfo/%s", tz);
  if (stat(path, &st) != 0 || !S_ISREG(st.st_mode)) {
    snprintf(out, sizeof out, "unknown timezone %.40s", tz);
    return strdup(out);
  }
  setenv("TZ", tz, 1);
  tzset();
  time_t now = time(NULL);
  struct tm tm;
  localtime_r(&now, &tm);
  strftime(out, sizeof out, "%Y-%m-%dT%H:%M:%S%z", &tm);
  return strdup(out);
}

// ---- main --------------------------------------------------------------------

int main(void) {
  const char *key = getenv("ANTHROPIC_API_KEY");
  if (!key || !*key) { fputs("ANTHROPIC_API_KEY is not set\n", stderr); return 2; }
  const char *base = getenv("ANTHROPIC_BASE_URL");
  if (!base || !*base) base = "https://api.anthropic.com";
  const char *model = getenv("MODEL");
  if (!model || !*model) model = "claude-opus-5-5";

  buf prompt = {0};
  char in[4096];
  for (size_t n; (n = fread(in, 1, sizeof in, stdin)) > 0;) put(&prompt, in, n);

  char url[1024], auth[1024];
  snprintf(url, sizeof url, "%s/v1/messages", base);
  snprintf(auth, sizeof auth, "x-api-key: %s", key);
  struct curl_slist *headers = NULL;
  headers = curl_slist_append(headers, auth);
  headers = curl_slist_append(headers, "anthropic-version: " VERSION);
  headers = curl_slist_append(headers, "content-type: application/json");

  curl_global_init(CURL_GLOBAL_DEFAULT);
  turn t = {0};
  t.curl = curl_easy_init();
  curl_easy_setopt(t.curl, CURLOPT_URL, url);
  curl_easy_setopt(t.curl, CURLOPT_HTTPHEADER, headers);
  curl_easy_setopt(t.curl, CURLOPT_WRITEFUNCTION, body);
  curl_easy_setopt(t.curl, CURLOPT_WRITEDATA, &t);

  // The conversation so far, as the JSON text of the messages array's items.
  buf messages = {0};
  puts_(&messages, "{\"role\":\"user\",\"content\":");
  quote(&messages, prompt.p ? prompt.p : "");
  puts_(&messages, "}");

  for (;;) {
    buf req = {0};
    puts_(&req, "{\"model\":"); quote(&req, model);
    puts_(&req, ",\"max_tokens\":1024,\"stream\":true,\"tools\":" TOOLS ",\"messages\":[");
    put(&req, messages.p, messages.n);
    puts_(&req, "]}");

    reset(&t);
    curl_easy_setopt(t.curl, CURLOPT_POSTFIELDS, req.p);
    curl_easy_setopt(t.curl, CURLOPT_POSTFIELDSIZE, (long)req.n);
    CURLcode rc = curl_easy_perform(t.curl);
    if (rc != CURLE_OK) { fprintf(stderr, "error: %s\n", curl_easy_strerror(rc)); return 1; }
    if (t.status < 200 || t.status >= 300) {
      char *msg = str(get(get(t.error.p, "error"), "message"));
      fprintf(stderr, "error %ld: %s\n", t.status, msg ? msg : t.error.p);
      return 1;
    }
    fputs("\n", stdout);
    fflush(stdout);
    free(req.p);
    if (!t.stop || strcmp(t.stop, "tool_use") != 0) break;

    // Echo the assistant turn back, then answer each tool call.
    buf results = {0};
    puts_(&messages, ",{\"role\":\"assistant\",\"content\":[");
    for (int i = 0; i < t.nblocks; i++) {
      block *b = &t.blocks[i];
      if (i) puts_(&messages, ",");
      if (!b->tool) {
        puts_(&messages, "{\"type\":\"text\",\"text\":"); quote(&messages, b->text.p ? b->text.p : ""); puts_(&messages, "}");
        continue;
      }
      const char *input = b->input.n ? b->input.p : "{}";
      puts_(&messages, "{\"type\":\"tool_use\",\"id\":"); quote(&messages, b->id ? b->id : "");
      puts_(&messages, ",\"name\":"); quote(&messages, b->name ? b->name : "");
      puts_(&messages, ",\"input\":"); puts_(&messages, input); puts_(&messages, "}");

      char *tz = str(get(input, "timezone"));
      char *answer = b->name && strcmp(b->name, "local_time") == 0 ? local_time(tz) : strdup("unknown tool");
      if (results.n) puts_(&results, ",");
      puts_(&results, "{\"type\":\"tool_result\",\"tool_use_id\":"); quote(&results, b->id ? b->id : "");
      puts_(&results, ",\"content\":"); quote(&results, answer); puts_(&results, "}");
      free(tz); free(answer);
    }
    puts_(&messages, "]},{\"role\":\"user\",\"content\":[");
    put(&messages, results.p ? results.p : "", results.n);
    puts_(&messages, "]}");
    free(results.p);
  }

  curl_easy_cleanup(t.curl);
  curl_slist_free_all(headers);
  curl_global_cleanup();
  return 0;
}
