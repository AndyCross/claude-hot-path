"""The baseline: the official Python SDK, used the way its docs say to.

Same spec as every hand-written client on the board. The SDK reads
ANTHROPIC_API_KEY and ANTHROPIC_BASE_URL from the environment itself, streams
with messages.stream(), and the tool loop is the manual one from its docs.
"""
import os
import sys
from datetime import datetime
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

import anthropic

MODEL = os.environ.get("MODEL", "claude-opus-5-5")
TOOLS = [{
    "name": "local_time",
    "description": "The current local time in an IANA timezone.",
    "input_schema": {
        "type": "object",
        "properties": {"timezone": {"type": "string", "description": "IANA timezone, e.g. Europe/London"}},
        "required": ["timezone"],
    },
}]


def local_time(timezone: str) -> str:
    try:
        return datetime.now(ZoneInfo(timezone)).strftime("%Y-%m-%dT%H:%M:%S%z")
    except (ZoneInfoNotFoundError, ValueError):
        return f"unknown timezone {timezone}"


def main() -> int:
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ANTHROPIC_API_KEY is not set", file=sys.stderr)
        return 2
    client = anthropic.Anthropic(max_retries=0)
    messages = [{"role": "user", "content": sys.stdin.read()}]

    while True:
        try:
            with client.messages.stream(model=MODEL, max_tokens=1024, tools=TOOLS, messages=messages) as stream:
                for text in stream.text_stream:
                    print(text, end="", flush=True)
                response = stream.get_final_message()
        except anthropic.APIStatusError as e:
            message = e.message
            if isinstance(e.body, dict):
                message = e.body.get("error", {}).get("message", message)
            print(f"error {e.status_code}: {message}", file=sys.stderr)
            return 1
        print()
        if response.stop_reason != "tool_use":
            return 0
        results = []
        for block in response.content:
            if block.type == "tool_use":
                content = local_time(block.input.get("timezone", "UTC")) if block.name == "local_time" else f"unknown tool {block.name}"
                results.append({"type": "tool_result", "tool_use_id": block.id, "content": content})
        messages.append({"role": "assistant", "content": response.content})
        messages.append({"role": "user", "content": results})


if __name__ == "__main__":
    sys.exit(main())
