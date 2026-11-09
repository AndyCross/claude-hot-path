// A Claude Messages API client in one C# file, tuned for the hot path rather
// than written the way the templates say. libc's socket, connect, read and
// write called directly with hand-written HTTP/1.1 on top, in place of
// HttpClient; Utf8JsonReader and Utf8JsonWriter in place of JsonNode;
// synchronous I/O; bare file descriptors in place of Console; no background
// GC thread; and _exit(2) once the last byte is out. Published with Native
// AOT as a static executable on musl, shipped from scratch.
using System.Buffers;
using System.Net.Security;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;

var key = Environment.GetEnvironmentVariable("ANTHROPIC_API_KEY");
if (string.IsNullOrEmpty(key)) { Console.Error.WriteLine("ANTHROPIC_API_KEY is not set"); return 2; }
var baseUrl = Environment.GetEnvironmentVariable("ANTHROPIC_BASE_URL") ?? "https://api.anthropic.com";
var model = Environment.GetEnvironmentVariable("MODEL") ?? "claude-opus-5-5";

// Stdin and stdout as bare file descriptors: no Console, no encoder, no
// buffering, one write(2) per text delta.
var stdin = new FdStream(0);
var prompt = new ArrayBufferWriter<byte>(4096);
for (int n; (n = stdin.Read(prompt.GetSpan(4096))) > 0;) prompt.Advance(n);
var stdout = new FdStream(1);

var conn = new Conn(baseUrl);
var turns = new List<Turn>();
var scratch = new byte[64 * 1024];

while (true)
{
    conn.Send(key, BuildBody(model, prompt.WrittenSpan, turns));
    var status = conn.ReadHeaders();
    if (status < 200 || status > 299)
    {
        var raw = conn.ReadAll();
        Console.Error.WriteLine($"error {status}: {ErrorMessage(raw)}");
        return 1;
    }

    var content = new List<Block>();
    string? stop = null;
    var line = new ArrayBufferWriter<byte>(1024);
    ReadOnlySpan<byte> chunk;
    while (!(chunk = conn.Next()).IsEmpty)
    {
        int nl;
        while ((nl = chunk.IndexOf((byte)'\n')) >= 0)
        {
            if (line.WrittenCount == 0) HandleLine(chunk[..nl], content, stdout, scratch, ref stop);
            else { line.Write(chunk[..nl]); HandleLine(line.WrittenSpan, content, stdout, scratch, ref stop); line.Clear(); }
            chunk = chunk[(nl + 1)..];
        }
        if (!chunk.IsEmpty) line.Write(chunk);
    }
    stdout.Write("\n"u8);
    if (stop != "tool_use") break;

    var results = new List<(string id, string result)>();
    foreach (var b in content)
        if (b.Type == "tool_use") results.Add((b.Id, RunTool(b)));
    turns.Add(new Turn(content, results));
}
Native.Exit(0);
return 0;

static void HandleLine(ReadOnlySpan<byte> line, List<Block> content, Stream stdout, byte[] scratch, ref string? stop)
{
    if (!line.StartsWith("data: "u8)) return;
    var r = new Utf8JsonReader(line[6..]);
    int kind = 0, index = 0, valLen = 0;
    string sub = "", id = "", name = "";
    r.Read();
    while (r.Read() && r.TokenType != JsonTokenType.EndObject)
    {
        if (r.ValueTextEquals("type"u8))
        {
            r.Read();
            kind = r.ValueTextEquals("content_block_start"u8) ? 1 : r.ValueTextEquals("content_block_delta"u8) ? 2
                 : r.ValueTextEquals("message_delta"u8) ? 3 : 0;
        }
        else if (r.ValueTextEquals("index"u8)) { r.Read(); index = r.GetInt32(); }
        else if (r.ValueTextEquals("content_block"u8) || r.ValueTextEquals("delta"u8))
        {
            r.Read();
            while (r.Read() && r.TokenType != JsonTokenType.EndObject)
            {
                if (r.ValueTextEquals("type"u8)) { r.Read(); sub = r.GetString()!; }
                else if (r.ValueTextEquals("id"u8)) { r.Read(); id = r.GetString()!; }
                else if (r.ValueTextEquals("name"u8)) { r.Read(); name = r.GetString()!; }
                else if (r.ValueTextEquals("text"u8) || r.ValueTextEquals("partial_json"u8)) { r.Read(); valLen = r.CopyString(scratch); }
                else if (r.ValueTextEquals("stop_reason"u8)) { r.Read(); if (r.TokenType == JsonTokenType.String) stop = r.GetString(); }
                else { r.Read(); r.Skip(); }
            }
        }
        else { r.Read(); r.Skip(); }
    }
    switch (kind)
    {
        case 1:
            while (content.Count <= index) content.Add(new Block());
            content[index] = new Block { Type = sub, Id = id, Name = name };
            break;
        case 2 when sub == "text_delta":
            stdout.Write(scratch, 0, valLen);
            content[index].Text.Write(scratch.AsSpan(0, valLen));
            break;
        case 2 when sub == "input_json_delta":
            content[index].Input.Write(scratch.AsSpan(0, valLen));
            break;
    }
}

static ReadOnlySpan<byte> BuildBody(string model, ReadOnlySpan<byte> prompt, List<Turn> turns)
{
    var buf = new ArrayBufferWriter<byte>(4096);
    using var w = new Utf8JsonWriter(buf, new JsonWriterOptions { SkipValidation = true });
    w.WriteStartObject();
    w.WriteString("model", model);
    w.WriteNumber("max_tokens", 1024);
    w.WriteBoolean("stream", true);
    w.WritePropertyName("tools");
    w.WriteRawValue("""
        [{"name":"local_time","description":"The current local time in an IANA timezone.",
          "input_schema":{"type":"object","properties":{"timezone":{"type":"string","description":"IANA timezone, e.g. Europe/London"}},"required":["timezone"]}}]
        """u8);
    w.WriteStartArray("messages");
    w.WriteStartObject(); w.WriteString("role", "user"); w.WriteString("content", prompt); w.WriteEndObject();
    foreach (var t in turns)
    {
        w.WriteStartObject(); w.WriteString("role", "assistant"); w.WriteStartArray("content");
        foreach (var b in t.Content)
        {
            w.WriteStartObject();
            w.WriteString("type", b.Type);
            if (b.Type == "tool_use")
            {
                w.WriteString("id", b.Id); w.WriteString("name", b.Name);
                w.WritePropertyName("input");
                if (b.Input.WrittenCount > 0) w.WriteRawValue(b.Input.WrittenSpan); else w.WriteRawValue("{}"u8);
            }
            else w.WriteString("text", b.Text.WrittenSpan);
            w.WriteEndObject();
        }
        w.WriteEndArray(); w.WriteEndObject();
        w.WriteStartObject(); w.WriteString("role", "user"); w.WriteStartArray("content");
        foreach (var (id, result) in t.Results)
        {
            w.WriteStartObject();
            w.WriteString("type", "tool_result"); w.WriteString("tool_use_id", id); w.WriteString("content", result);
            w.WriteEndObject();
        }
        w.WriteEndArray(); w.WriteEndObject();
    }
    w.WriteEndArray();
    w.WriteEndObject();
    w.Flush();
    return buf.WrittenSpan;
}

static string RunTool(Block b)
{
    if (b.Name != "local_time") return "unknown tool " + b.Name;
    var tz = "UTC";
    var r = new Utf8JsonReader(b.Input.WrittenSpan);
    while (r.Read())
        if (r.TokenType == JsonTokenType.PropertyName && r.ValueTextEquals("timezone"u8)) { r.Read(); tz = r.GetString() ?? tz; break; }
    try
    {
        var info = TimeZoneInfo.FindSystemTimeZoneById(tz);
        return TimeZoneInfo.ConvertTime(DateTimeOffset.UtcNow, info).ToString("yyyy-MM-dd'T'HH:mm:sszz00");
    }
    catch (TimeZoneNotFoundException) { return "unknown timezone " + tz; }
}

static string ErrorMessage(ReadOnlySpan<byte> raw)
{
    var r = new Utf8JsonReader(raw);
    try
    {
        while (r.Read())
            if (r.TokenType == JsonTokenType.PropertyName && r.CurrentDepth == 2 && r.ValueTextEquals("message"u8)) { r.Read(); return r.GetString()!; }
    }
    catch (JsonException) { }
    return Encoding.UTF8.GetString(raw).Trim();
}

sealed class Block
{
    public string Type = "", Id = "", Name = "";
    public ArrayBufferWriter<byte> Text = new(), Input = new();
}

sealed record Turn(List<Block> Content, List<(string id, string result)> Results);

// One keep-alive connection and just enough HTTP/1.1 to read a status line,
// find the framing header, and hand back the body in decoded pieces. Chunked
// and content-length framing, nothing else. The socket is libc's, called
// directly: no SocketAsyncEngine, no epoll threads, no protocol probes.
sealed class Conn
{
    readonly string scheme, host, path;
    readonly int port;
    readonly byte[] hdr;
    Stream s = Stream.Null;
    byte[] buf = new byte[32 * 1024];
    int start, end;
    bool chunked, done;
    long remaining;

    public Conn(string url)
    {
        // scheme://host[:port][/path], parsed by hand: System.Uri is a lot of
        // machinery to split one string in three places.
        int at = url.IndexOf("://", StringComparison.Ordinal);
        scheme = url[..at];
        var rest = url[(at + 3)..];
        int slash = rest.IndexOf('/');
        var authority = slash < 0 ? rest : rest[..slash];
        path = slash < 0 ? "" : rest[slash..].TrimEnd('/');
        int colon = authority.IndexOf(':');
        host = colon < 0 ? authority : authority[..colon];
        port = colon < 0 ? (scheme == "https" ? 443 : 80) : int.Parse(authority[(colon + 1)..]);
        hdr = Encoding.ASCII.GetBytes($"POST {path}/v1/messages HTTP/1.1\r\nHost: {authority}\r\nanthropic-version: 2023-06-01\r\ncontent-type: application/json\r\n");
    }

    void Open()
    {
        Stream ns = new FdStream(Native.Connect(host, port));
        if (scheme == "https")
        {
            var ssl = new SslStream(ns, false);
            ssl.AuthenticateAsClient(host);
            ns = ssl;
        }
        s = ns;
    }
    public void Send(string key, ReadOnlySpan<byte> body)
    {
        if (s == Stream.Null) Open();
        var req = new ArrayBufferWriter<byte>(hdr.Length + body.Length + 128);
        req.Write(hdr);
        req.Write(Encoding.ASCII.GetBytes($"x-api-key: {key}\r\ncontent-length: {body.Length}\r\n\r\n"));
        req.Write(body);
        s.Write(req.WrittenSpan);
        start = end = 0; done = false; chunked = false; remaining = -1;
    }

    int Fill()
    {
        if (start == end) start = end = 0;
        else if (end == buf.Length)
        {
            if (start > 0) { Buffer.BlockCopy(buf, start, buf, 0, end - start); end -= start; start = 0; }
            else Array.Resize(ref buf, buf.Length * 2);
        }
        int n = s.Read(buf, end, buf.Length - end);
        end += n;
        return n;
    }

    public int ReadHeaders()
    {
        int at;
        while ((at = buf.AsSpan(start, end - start).IndexOf("\r\n\r\n"u8)) < 0)
            if (Fill() == 0) throw new IOException("connection closed before headers");
        var head = buf.AsSpan(start, at);
        int status = (head[9] - '0') * 100 + (head[10] - '0') * 10 + (head[11] - '0');
        foreach (var range in head.Split("\r\n"u8))
        {
            var h = head[range];
            int colon = h.IndexOf((byte)':');
            if (colon < 0) continue;
            var value = h[(colon + 1)..].Trim((byte)' ');
            if (Ascii.EqualsIgnoreCase(h[..colon], "transfer-encoding"u8) && Ascii.EqualsIgnoreCase(value, "chunked"u8)) chunked = true;
            else if (Ascii.EqualsIgnoreCase(h[..colon], "content-length"u8)) remaining = long.Parse(value);
        }
        start += at + 4;
        if (chunked) remaining = 0;
        return status;
    }

    // The next piece of decoded body, or empty at the end of it.
    public ReadOnlySpan<byte> Next()
    {
        if (done) return default;
        if (chunked && remaining == 0)
        {
            int nl;
            while ((nl = buf.AsSpan(start, end - start).IndexOf("\r\n"u8)) < 0)
                if (Fill() == 0) { done = true; return default; }
            var sizeLine = buf.AsSpan(start, nl);
            int semi = sizeLine.IndexOf((byte)';');
            remaining = long.Parse(Encoding.ASCII.GetString(semi < 0 ? sizeLine : sizeLine[..semi]), System.Globalization.NumberStyles.HexNumber);
            start += nl + 2;
            if (remaining == 0) { done = true; return default; }
        }
        if (start == end && Fill() == 0) { done = true; return default; }
        int n = remaining < 0 ? end - start : (int)Math.Min(remaining, end - start);
        var piece = buf.AsSpan(start, n);
        start += n;
        if (remaining > 0) remaining -= n;
        if (remaining == 0)
        {
            if (chunked) { while (end - start < 2) if (Fill() == 0) break; start += 2; }
            else done = true;
        }
        return piece;
    }

    public ReadOnlySpan<byte> ReadAll()
    {
        var all = new ArrayBufferWriter<byte>();
        ReadOnlySpan<byte> p;
        while (!(p = Next()).IsEmpty) all.Write(p);
        return all.WrittenSpan;
    }
}

// A Stream over a file descriptor: read(2) and write(2) and nothing else.
// It is stdin, stdout, the socket, and the thing SslStream wraps when the
// base URL is https.
sealed unsafe class FdStream(int fd) : Stream
{
    public override int Read(byte[] b, int off, int n) => Read(b.AsSpan(off, n));
    public override int Read(Span<byte> b)
    {
        fixed (byte* p = b) { var r = Native.read(fd, p, (nuint)b.Length); if (r < 0) throw new IOException("read failed"); return (int)r; }
    }
    public override void Write(byte[] b, int off, int n) => Write(b.AsSpan(off, n));
    public override void Write(ReadOnlySpan<byte> b)
    {
        fixed (byte* p = b)
            for (int done = 0; done < b.Length;)
            {
                var r = Native.write(fd, p + done, (nuint)(b.Length - done));
                if (r < 0) throw new IOException("write failed");
                done += (int)r;
            }
    }
    public override void Flush() { }
    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => true;
    public override long Length => throw new NotSupportedException();
    public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }
    public override long Seek(long o, SeekOrigin so) => throw new NotSupportedException();
    public override void SetLength(long v) => throw new NotSupportedException();
}

static unsafe partial class Native
{
    [StructLayout(LayoutKind.Sequential)]
    struct AddrInfo { public int Flags, Family, SockType, Protocol; public uint AddrLen; public byte* Addr; public byte* CanonName; public AddrInfo* Next; }

    [LibraryImport("libc", StringMarshalling = StringMarshalling.Utf8)] private static partial int getaddrinfo(string node, string service, AddrInfo* hints, AddrInfo** res);
    [LibraryImport("libc")] private static partial void freeaddrinfo(AddrInfo* res);
    [LibraryImport("libc")] private static partial int socket(int domain, int type, int protocol);
    [LibraryImport("libc")] private static partial int setsockopt(int fd, int level, int name, int* value, uint len);
    [LibraryImport("libc")] private static partial int connect(int fd, byte* addr, uint len);
    [LibraryImport("libc")] private static partial int close(int fd);
    [LibraryImport("libc")] public static partial nint read(int fd, byte* buf, nuint len);
    [LibraryImport("libc")] public static partial nint write(int fd, byte* buf, nuint len);
    // Straight out through the kernel: no runtime shutdown, no finalizers,
    // nothing left to flush because nothing was buffered.
    [LibraryImport("libc", EntryPoint = "_exit")] public static partial void Exit(int code);

    // One A query, one TCP connect, Nagle off.
    public static int Connect(string host, int port)
    {
        var hints = new AddrInfo { Family = 2 /* AF_INET */, SockType = 1 /* SOCK_STREAM */ };
        AddrInfo* res;
        if (getaddrinfo(host, port.ToString(), &hints, &res) != 0) throw new IOException("cannot resolve " + host);
        try
        {
            for (var ai = res; ai != null; ai = ai->Next)
            {
                int fd = socket(ai->Family, ai->SockType | 0x80000 /* SOCK_CLOEXEC */, ai->Protocol);
                if (fd < 0) continue;
                int one = 1;
                setsockopt(fd, 6 /* IPPROTO_TCP */, 1 /* TCP_NODELAY */, &one, 4);
                if (connect(fd, ai->Addr, ai->AddrLen) == 0) return fd;
                close(fd);
            }
        }
        finally { freeaddrinfo(res); }
        throw new IOException("cannot connect to " + host);
    }
}
