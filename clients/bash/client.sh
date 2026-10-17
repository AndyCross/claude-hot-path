#!/usr/bin/env bash
# The floor. A Claude Messages API client in bash and curl: streams the reply,
# completes one tool call, and reports API errors. No jq.
set -euo pipefail

: "${ANTHROPIC_API_KEY:?ANTHROPIC_API_KEY is not set}"
BASE="${ANTHROPIC_BASE_URL:-https://api.anthropic.com}"
MODEL="${MODEL:-claude-opus-5-5}"

TOOLS='[{"name":"local_time","description":"The current local time in an IANA timezone.","input_schema":{"type":"object","properties":{"timezone":{"type":"string","description":"IANA timezone, e.g. Europe/London"}},"required":["timezone"]}}]'

# JSON-encode a string the hard way: backslash, quote, and the control characters.
json_str() {
  local s=$1
  s=${s//\\/\\\\}; s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}; s=${s//$'\r'/\\r}; s=${s//$'\t'/\\t}
  printf '"%s"' "$s"
}

# Decode the escapes a JSON string can contain. printf %b handles \n \t \\ and \uXXXX.
json_unstr() {
  local s=$1
  s=${s//\\\"/\"}; s=${s//\\\//\/}
  s=${s//\\b/\\0010}; s=${s//\\f/\\0014}
  printf '%b' "$s"
}

local_time() { # $1 = IANA timezone
  TZ="$1" date '+%Y-%m-%dT%H:%M:%S%z'
}

# One streamed request. Prints text as it arrives. Sets TOOL_ID, TOOL_NAME,
# TOOL_INPUT, ASSISTANT_TEXT and STOP_REASON for the caller.
request() {
  local body=$1 hdr status line data text=""
  hdr=$(mktemp); trap 'rm -f "$hdr"' RETURN
  STOP_REASON=""; TOOL_ID=""; TOOL_NAME=""; TOOL_INPUT=""; ASSISTANT_TEXT=""
  local errbody=""

  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line != data:* ]]; then
      [[ $line == event:* || -z $line ]] || errbody+="$line"
      continue
    fi
    data=${line#data: }
    if [[ $data =~ \"type\":\"text_delta\",\"text\":\"((\\.|[^\"\\])*)\" ]]; then
      text=$(json_unstr "${BASH_REMATCH[1]}"; printf x); text=${text%x}
      printf '%s' "$text"; ASSISTANT_TEXT+="$text"
    elif [[ $data =~ \"type\":\"input_json_delta\",\"partial_json\":\"((\\.|[^\"\\])*)\" ]]; then
      TOOL_INPUT+=$(json_unstr "${BASH_REMATCH[1]}")
    elif [[ $data =~ \"type\":\"tool_use\",\"id\":\"([^\"]+)\",\"name\":\"([^\"]+)\" ]]; then
      TOOL_ID=${BASH_REMATCH[1]}; TOOL_NAME=${BASH_REMATCH[2]}
    elif [[ $data =~ \"stop_reason\":\"([a-z_]+)\" ]]; then
      STOP_REASON=${BASH_REMATCH[1]}
    fi
  done < <(curl -sS -N -D "$hdr" "$BASE/v1/messages" \
      -H "x-api-key: $ANTHROPIC_API_KEY" \
      -H "anthropic-version: 2023-06-01" \
      -H "content-type: application/json" \
      --data-binary "$body")

  status=$(awk 'NR==1{print $2}' "$hdr")
  if [[ $status != 2* ]]; then
    if [[ $errbody =~ \"message\":[[:space:]]*\"((\\.|[^\"\\])*)\" ]]; then
      printf 'error %s: %s\n' "$status" "$(json_unstr "${BASH_REMATCH[1]}")" >&2
    else
      printf 'error %s\n' "$status" >&2
    fi
    exit 1
  fi
}

prompt=$(cat)
user_msg="{\"role\":\"user\",\"content\":$(json_str "$prompt")}"
body="{\"model\":\"$MODEL\",\"max_tokens\":1024,\"stream\":true,\"tools\":$TOOLS,\"messages\":[$user_msg]}"
request "$body"

if [[ $STOP_REASON == tool_use ]]; then
  result=""
  if [[ $TOOL_NAME == local_time && $TOOL_INPUT =~ \"timezone\":[[:space:]]*\"([^\"]+)\" ]]; then
    result=$(local_time "${BASH_REMATCH[1]}")
  else
    result="unknown tool $TOOL_NAME"
  fi
  assistant="{\"role\":\"assistant\",\"content\":["
  [[ -n $ASSISTANT_TEXT ]] && assistant+="{\"type\":\"text\",\"text\":$(json_str "$ASSISTANT_TEXT")},"
  assistant+="{\"type\":\"tool_use\",\"id\":\"$TOOL_ID\",\"name\":\"$TOOL_NAME\",\"input\":${TOOL_INPUT:-{\}}}]}"
  tool_msg="{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"$TOOL_ID\",\"content\":$(json_str "$result")}]}"
  body="{\"model\":\"$MODEL\",\"max_tokens\":1024,\"stream\":true,\"tools\":$TOOLS,\"messages\":[$user_msg,$assistant,$tool_msg]}"
  printf '\n'
  request "$body"
fi
printf '\n'
