// A Claude Messages API client in one Go file: net/http, encoding/json, and a
// Server-Sent Events reader written with bufio. Streams the reply, completes
// one tool call, reports API errors.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const version = "2023-06-01"

var tools = json.RawMessage(`[{"name":"local_time","description":"The current local time in an IANA timezone.","input_schema":{"type":"object","properties":{"timezone":{"type":"string","description":"IANA timezone, e.g. Europe/London"}},"required":["timezone"]}}]`)

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string, or []block
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitzero"`
	ID        string          `json:"id,omitzero"`
	Name      string          `json:"name,omitzero"`
	Input     json.RawMessage `json:"input,omitzero"`
	ToolUseID string          `json:"tool_use_id,omitzero"`
	Content   string          `json:"content,omitzero"`
}

type request struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream"`
	Tools     json.RawMessage `json:"tools"`
	Messages  []message       `json:"messages"`
}

// event is the union of every SSE payload field we read.
type event struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *block `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set")
		os.Exit(2)
	}
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	model := os.Getenv("MODEL")
	if model == "" {
		model = "claude-opus-5-5"
	}
	prompt, _ := io.ReadAll(os.Stdin)
	msgs := []message{{Role: "user", Content: string(prompt)}}

	for {
		assistant, stop, err := stream(base, key, request{Model: model, MaxTokens: 1024, Stream: true, Tools: tools, Messages: msgs})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println()
		if stop != "tool_use" {
			return
		}
		var results []block
		for _, b := range assistant {
			if b.Type == "tool_use" {
				results = append(results, block{Type: "tool_result", ToolUseID: b.ID, Content: run(b)})
			}
		}
		msgs = append(msgs, message{Role: "assistant", Content: assistant}, message{Role: "user", Content: results})
	}
}

func run(b block) string {
	if b.Name != "local_time" {
		return "unknown tool " + b.Name
	}
	var in struct{ Timezone string }
	json.Unmarshal(b.Input, &in)
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return "unknown timezone " + in.Timezone
	}
	return time.Now().In(loc).Format("2006-01-02T15:04:05-0700")
}

// stream posts the request, prints text deltas as they arrive, and returns
// the assembled assistant content and the stop reason.
func stream(base, key string, req request) ([]block, string, error) {
	body, _ := json.Marshal(req)
	hr, _ := http.NewRequest("POST", base+"/v1/messages", bytes.NewReader(body))
	hr.Header.Set("x-api-key", key)
	hr.Header.Set("anthropic-version", version)
	hr.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var ae apiError
		raw, _ := io.ReadAll(resp.Body)
		json.Unmarshal(raw, &ae)
		if ae.Error.Message == "" {
			ae.Error.Message = strings.TrimSpace(string(raw))
		}
		return nil, "", fmt.Errorf("%d: %s", resp.StatusCode, ae.Error.Message)
	}

	var content []block
	var partial []strings.Builder
	var stop string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue // event:, ping bodies, blank separators
		}
		var ev event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, "", err
		}
		switch ev.Type {
		case "content_block_start":
			content = append(content, *ev.ContentBlock)
			partial = append(partial, strings.Builder{})
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				os.Stdout.WriteString(ev.Delta.Text)
				content[ev.Index].Text += ev.Delta.Text
			case "input_json_delta":
				partial[ev.Index].WriteString(ev.Delta.PartialJSON)
			}
		case "content_block_stop":
			if content[ev.Index].Type == "tool_use" {
				if s := partial[ev.Index].String(); s != "" {
					content[ev.Index].Input = json.RawMessage(s)
				}
			}
		case "message_delta":
			stop = ev.Delta.StopReason
		}
	}
	return content, stop, sc.Err()
}
