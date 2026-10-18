"""A Claude Messages API client in one Python file: http.client and json from
the standard library, nothing installed. Streams the reply, completes one tool
call, reports API errors."""
import http.client
import json
import os
import sys
from datetime import datetime
from urllib.parse import urlsplit
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

VERSION = "2023-06-01"
TOOLS = [{
    "name": "local_time",
    "description": "The current local time in an IANA timezone.",
    "input_schema": {
        "type": "object",
        "properties": {"timezone": {"type": "string", "description": "IANA timezone, e.g. Europe/London"}},
        "required": ["timezone"],
    },
}]


def local_time(timezone):
    try:
        return datetime.now(ZoneInfo(timezone)).strftime("%Y-%m-%dT%H:%M:%S%z")
    except (ZoneInfoNotFoundError, ValueError):
        return f"unknown timezone {timezone}"


def stream(conn, key, body):
    """POST once, print text deltas as they arrive, return (content, stop_reason)."""
    conn.request("POST", "/v1/messages", json.dumps(body), {
        "x-api-key": key, "anthropic-version": VERSION, "content-type": "application/json",
    })
    resp = conn.getresponse()
    if not 200 <= resp.status < 300:
        raw = resp.read()
        try:
            message = json.loads(raw)["error"]["message"]
        except (ValueError, KeyError, TypeError):
            message = raw.decode(errors="replace").strip()
        print(f"error {resp.status}: {message}", file=sys.stderr)
        sys.exit(1)

    content, partial, stop = [], [], None
    out = sys.stdout
    # One SSE line per iteration. HTTPResponse is a buffered reader, so this
    # returns each "data:" line as soon as its bytes have arrived.
    for raw in resp:
        if not raw.startswith(b"data: "):
            continue  # event:, ping, blank separators
        ev = json.loads(raw[6:])
        index = ev.get("index", 0)
        kind = ev["type"]
        if kind == "content_block_start":
            content.append(ev["content_block"])
            partial.append([])
        elif kind == "content_block_delta":
            delta = ev["delta"]
            if delta["type"] == "text_delta":
                out.write(delta["text"])
                out.flush()
                content[index]["text"] = content[index].get("text", "") + delta["text"]
            elif delta["type"] == "input_json_delta":
                partial[index].append(delta["partial_json"])
        elif kind == "content_block_stop":
            if content[index]["type"] == "tool_use" and partial[index]:
                content[index]["input"] = json.loads("".join(partial[index]))
        elif kind == "message_delta":
            stop = ev["delta"].get("stop_reason")
    return content, stop


def main():
    key = os.environ.get("ANTHROPIC_API_KEY")
    if not key:
        print("ANTHROPIC_API_KEY is not set", file=sys.stderr)
        return 2
    base = urlsplit(os.environ.get("ANTHROPIC_BASE_URL", "https://api.anthropic.com"))
    model = os.environ.get("MODEL", "claude-opus-5-5")
    Conn = http.client.HTTPSConnection if base.scheme == "https" else http.client.HTTPConnection
    conn = Conn(base.hostname, base.port)  # one connection, reused for the second turn

    messages = [{"role": "user", "content": sys.stdin.read()}]
    while True:
        content, stop = stream(conn, key, {
            "model": model, "max_tokens": 1024, "stream": True, "tools": TOOLS, "messages": messages,
        })
        print()
        if stop != "tool_use":
            return 0
        results = []
        for block in content:
            if block["type"] == "tool_use":
                result = local_time(block.get("input", {}).get("timezone", "UTC")) if block["name"] == "local_time" else f"unknown tool {block['name']}"
                results.append({"type": "tool_result", "tool_use_id": block["id"], "content": result})
        messages.append({"role": "assistant", "content": content})
        messages.append({"role": "user", "content": results})


if __name__ == "__main__":
    sys.exit(main())
