//! A Claude Messages API client in one Rust file. Rust's standard library has
//! no HTTP, so this uses ureq (with rustls) for the transport and serde_json
//! for the parsing, and nothing else. Streams the reply, completes one tool
//! call, reports API errors.
use serde_json::{json, Value};
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::process::exit;
use std::sync::Mutex;
use ureq::unversioned::resolver::{DefaultResolver, ResolvedSocketAddrs, Resolver};
use ureq::unversioned::transport::{DefaultConnector, NextTimeout};

const VERSION: &str = "2023-06-01";

fn main() {
    let key = match std::env::var("ANTHROPIC_API_KEY") {
        Ok(k) if !k.is_empty() => k,
        _ => {
            eprintln!("ANTHROPIC_API_KEY is not set");
            exit(2)
        }
    };
    let base = std::env::var("ANTHROPIC_BASE_URL").unwrap_or_else(|_| "https://api.anthropic.com".into());
    let model = std::env::var("MODEL").unwrap_or_else(|_| "claude-opus-5-5".into());
    let mut prompt = String::new();
    std::io::stdin().read_to_string(&mut prompt).unwrap();

    let tools = json!([{
        "name": "local_time",
        "description": "The current local time in an IANA timezone.",
        "input_schema": {"type": "object",
            "properties": {"timezone": {"type": "string", "description": "IANA timezone, e.g. Europe/London"}},
            "required": ["timezone"]}
    }]);
    let mut messages = vec![json!({"role": "user", "content": prompt})];

    // ureq resolves the host on every request, even when it is about to reuse a
    // pooled connection. That is a DNS round trip per turn. Cache it.
    let config = ureq::Agent::config_builder().http_status_as_error(false).build();
    let agent = ureq::Agent::with_parts(config, DefaultConnector::new(), CachingResolver::default());

    loop {
        let body = json!({"model": model, "max_tokens": 1024, "stream": true, "tools": tools, "messages": messages});
        let (content, stop) = stream(&agent, &base, &key, &body);
        println!();
        if stop.as_deref() != Some("tool_use") {
            return;
        }
        let results: Vec<Value> = content
            .iter()
            .filter(|b| b["type"] == "tool_use")
            .map(|b| json!({"type": "tool_result", "tool_use_id": b["id"], "content": run(b)}))
            .collect();
        messages.push(json!({"role": "assistant", "content": content}));
        messages.push(json!({"role": "user", "content": results}));
    }
}

#[derive(Debug, Default)]
struct CachingResolver {
    inner: DefaultResolver,
    cache: Mutex<HashMap<String, ResolvedSocketAddrs>>,
}

impl Resolver for CachingResolver {
    fn resolve(&self, uri: &ureq::http::Uri, config: &ureq::config::Config, timeout: NextTimeout) -> Result<ResolvedSocketAddrs, ureq::Error> {
        let key = uri.authority().map(|a| a.to_string()).unwrap_or_default();
        if let Some(hit) = self.cache.lock().unwrap().get(&key) {
            return Ok(hit.clone());
        }
        let addrs = self.inner.resolve(uri, config, timeout)?;
        self.cache.lock().unwrap().insert(key, addrs.clone());
        Ok(addrs)
    }
}

fn run(block: &Value) -> String {
    if block["name"] != "local_time" {
        return format!("unknown tool {}", block["name"]);
    }
    let tz = block["input"]["timezone"].as_str().unwrap_or("UTC");
    match local_time(tz) {
        Some(s) => s,
        None => format!("unknown timezone {tz}"),
    }
}

/// std has no timezone database, but the platform does. Read the zone's TZif
/// file (RFC 8536) from /usr/share/zoneinfo, take the version 2 block with
/// 64-bit transition times, find the transition in force now, and format.
fn local_time(tz: &str) -> Option<String> {
    if tz.contains("..") || tz.starts_with('/') {
        return None;
    }
    let data = std::fs::read(format!("/usr/share/zoneinfo/{tz}")).ok()?;
    let be32 = |b: &[u8], i: usize| u32::from_be_bytes(b[i..i + 4].try_into().unwrap()) as usize;
    let header = |b: &[u8]| -> Option<[usize; 6]> {
        if &b[..4] != b"TZif" { return None; }
        Some([be32(b, 20), be32(b, 24), be32(b, 28), be32(b, 32), be32(b, 36), be32(b, 40)])
    };
    // Skip the version 1 block: header, then counts of 4-byte times.
    let [isut, isstd, leap, timecnt, typecnt, charcnt] = header(&data)?;
    let v1_len = 44 + timecnt * 5 + typecnt * 6 + charcnt + leap * 8 + isstd + isut;
    let (block, wide) = if data.len() > v1_len && data[v1_len..].starts_with(b"TZif") { (&data[v1_len..], true) } else { (&data[..], false) };
    let [_, _, _, timecnt, typecnt, _] = header(block)?;
    let tsize = if wide { 8 } else { 4 };
    let times = &block[44..];
    let idx = &times[timecnt * tsize..];
    let types = &idx[timecnt..];

    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).ok()?.as_secs() as i64;
    let mut type_i = 0usize;
    for i in 0..timecnt {
        let t = if wide {
            i64::from_be_bytes(times[i * 8..i * 8 + 8].try_into().unwrap())
        } else {
            i32::from_be_bytes(times[i * 4..i * 4 + 4].try_into().unwrap()) as i64
        };
        if t <= now { type_i = idx[i] as usize } else { break; }
    }
    if type_i >= typecnt { return None; }
    let offset = i32::from_be_bytes(types[type_i * 6..type_i * 6 + 4].try_into().unwrap()) as i64;

    // Civil date from days since the epoch (Howard Hinnant's algorithm).
    let local = now + offset;
    let days = local.div_euclid(86_400);
    let secs = local.rem_euclid(86_400);
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = yoe + era * 400 + if m <= 2 { 1 } else { 0 };
    let sign = if offset < 0 { '-' } else { '+' };
    let off = offset.abs();
    Some(format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}{sign}{:02}{:02}", secs / 3600, (secs % 3600) / 60, secs % 60, off / 3600, (off % 3600) / 60))
}

/// Posts the request, prints text deltas as they arrive, and returns the
/// assembled assistant content and the stop reason.
fn stream(agent: &ureq::Agent, base: &str, key: &str, body: &Value) -> (Vec<Value>, Option<String>) {
    let resp = agent
        .post(format!("{base}/v1/messages"))
        .header("x-api-key", key)
        .header("anthropic-version", VERSION)
        .header("content-type", "application/json")
        .send(body.to_string().as_bytes());
    let mut resp = match resp {
        Ok(r) => r,
        Err(e) => {
            eprintln!("error: {e}");
            exit(1)
        }
    };
    let status = resp.status().as_u16();
    if !(200..300).contains(&status) {
        let raw = resp.body_mut().read_to_string().unwrap_or_default();
        let message = serde_json::from_str::<Value>(&raw)
            .ok()
            .and_then(|v| v["error"]["message"].as_str().map(str::to_string))
            .unwrap_or_else(|| raw.trim().to_string());
        eprintln!("error {status}: {message}");
        exit(1);
    }

    let mut content: Vec<Value> = Vec::new();
    let mut partial: Vec<String> = Vec::new();
    let mut stop = None;
    let mut out = std::io::stdout().lock();
    let reader = BufReader::new(resp.body_mut().as_reader());
    for line in reader.lines() {
        let line = line.unwrap_or_default();
        let Some(data) = line.strip_prefix("data: ") else { continue };
        let ev: Value = match serde_json::from_str(data) {
            Ok(v) => v,
            Err(e) => {
                eprintln!("error: bad event: {e}");
                exit(1)
            }
        };
        let index = ev["index"].as_u64().unwrap_or(0) as usize;
        match ev["type"].as_str() {
            Some("content_block_start") => {
                content.push(ev["content_block"].clone());
                partial.push(String::new());
            }
            Some("content_block_delta") => match ev["delta"]["type"].as_str() {
                Some("text_delta") => {
                    let text = ev["delta"]["text"].as_str().unwrap_or("");
                    out.write_all(text.as_bytes()).unwrap();
                    out.flush().unwrap();
                    let existing = content[index]["text"].as_str().unwrap_or("").to_string();
                    content[index]["text"] = Value::String(existing + text);
                }
                Some("input_json_delta") => partial[index].push_str(ev["delta"]["partial_json"].as_str().unwrap_or("")),
                _ => {}
            },
            Some("content_block_stop") => {
                if content[index]["type"] == "tool_use" && !partial[index].is_empty() {
                    content[index]["input"] = serde_json::from_str(&partial[index]).unwrap_or(json!({}));
                }
            }
            Some("message_delta") => stop = ev["delta"]["stop_reason"].as_str().map(str::to_string),
            _ => {}
        }
    }
    (content, stop)
}
