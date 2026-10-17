//! A Claude Messages API client in one Rust file. Rust's standard library has
//! no HTTP, so this uses ureq (with rustls) for the transport and serde_json
//! for the parsing, and nothing else. Streams the reply, completes one tool
//! call, reports API errors.
use serde_json::{json, Value};
use std::io::{BufRead, BufReader, Read, Write};
use std::process::{exit, Command};

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

    let agent: ureq::Agent = ureq::Agent::config_builder()
        .http_status_as_error(false)
        .build()
        .into();

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

fn run(block: &Value) -> String {
    if block["name"] != "local_time" {
        return format!("unknown tool {}", block["name"]);
    }
    let tz = block["input"]["timezone"].as_str().unwrap_or("UTC");
    // std has no timezone database; the platform does.
    match Command::new("date").env("TZ", tz).arg("+%Y-%m-%dT%H:%M:%S%z").output() {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).trim().to_string(),
        _ => format!("unknown timezone {tz}"),
    }
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
