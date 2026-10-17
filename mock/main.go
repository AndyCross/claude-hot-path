// mock replays recorded Claude Messages API exchanges as Server-Sent Events
// with their original timing, so a client can be measured without a key and
// without the network. With UPSTREAM set it proxies to the real API instead,
// injecting the key from ANTHROPIC_API_KEY and recording new fixtures.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type chunk struct {
	DelayMs int64  `json:"delay_ms"` // since the previous chunk (or since headers for the first)
	Data    string `json:"data"`
}

type fixture struct {
	Synthetic bool              `json:"synthetic"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Chunks    []chunk           `json:"chunks"`
}

type stats struct {
	Scenario     string `json:"scenario"`
	ReceivedNs   int64  `json:"received_unix_ns"`
	FirstByteNs  int64  `json:"first_byte_unix_ns"`
	DoneNs       int64  `json:"done_unix_ns"`
	ReplayMs     int64  `json:"replay_ms"` // total of the fixture's delays: the mock's own time
	RequestBytes int    `json:"request_bytes"`
}

var (
	fixtureDir = env("FIXTURE_DIR", "/fixtures")
	upstream   = os.Getenv("UPSTREAM")
	mu         sync.Mutex
	history    []stats
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	http.HandleFunc("/v1/messages", messages)
	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodDelete {
			history = nil
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(history)
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	addr := env("ADDR", ":8080")
	mode := "replay"
	if upstream != "" {
		mode = "record via " + upstream
	}
	log.Printf("mock listening on %s, %s, fixtures in %s", addr, mode, fixtureDir)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// scenario picks the fixture from the request alone, so clients need no
// special header: a bad key is the error case, a tool_result in the messages
// is the second turn, anything else is the first turn.
func scenario(body []byte, key string) string {
	if key == "" || key == "invalid" || strings.HasPrefix(key, "sk-ant-invalid") {
		return "error-401"
	}
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "error-400"
	}
	for _, m := range req.Messages {
		if bytes.Contains(m.Content, []byte(`"tool_result"`)) {
			return "turn2"
		}
	}
	return "turn1"
}

func messages(w http.ResponseWriter, r *http.Request) {
	received := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, 405, "invalid_request_error", "method not allowed")
		return
	}
	for _, h := range []string{"x-api-key", "anthropic-version", "content-type"} {
		if r.Header.Get(h) == "" {
			writeAPIError(w, 400, "invalid_request_error", "missing required header: "+h)
			return
		}
	}
	name := scenario(body, r.Header.Get("x-api-key"))
	st := stats{Scenario: name, ReceivedNs: received.UnixNano(), RequestBytes: len(body)}

	var fx *fixture
	if upstream != "" {
		fx, err = record(name, r, body)
	} else {
		fx, err = load(name)
	}
	if err != nil {
		log.Printf("%s: %v", name, err)
		writeAPIError(w, 500, "api_error", err.Error())
		return
	}
	for k, v := range fx.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(fx.Status)
	fl, _ := w.(http.Flusher)
	for i, c := range fx.Chunks {
		if upstream == "" && c.DelayMs > 0 {
			time.Sleep(time.Duration(c.DelayMs) * time.Millisecond)
		}
		st.ReplayMs += c.DelayMs
		if i == 0 {
			st.FirstByteNs = time.Now().UnixNano()
		}
		io.WriteString(w, c.Data)
		if fl != nil {
			fl.Flush()
		}
	}
	st.DoneNs = time.Now().UnixNano()
	mu.Lock()
	history = append(history, st)
	mu.Unlock()
}

func load(name string) (*fixture, error) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, name+".json"))
	if err != nil {
		return nil, err
	}
	var fx fixture
	if err := json.Unmarshal(b, &fx); err != nil {
		return nil, err
	}
	return &fx, nil
}

// record forwards the request to the real API with the real key, streams the
// response back, and saves what came back with inter-chunk timings. The client
// under test never sees the key: it sends a placeholder and the proxy swaps it.
func record(name string, r *http.Request, body []byte) (*fixture, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	token := os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if key == "" && token == "" {
		return nil, fmt.Errorf("record mode needs ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN")
	}
	if strings.HasPrefix(name, "error-") {
		key, token = "sk-ant-invalid", "" // let the real API produce the real error
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(upstream, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", r.Header.Get("anthropic-version"))
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	} else {
		req.Header.Set("x-api-key", key)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	fx := &fixture{Status: resp.StatusCode, Headers: map[string]string{
		"Content-Type": resp.Header.Get("Content-Type"),
		"request-id":   resp.Header.Get("request-id"),
	}}
	buf := make([]byte, 32*1024)
	last := start
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			now := time.Now()
			fx.Chunks = append(fx.Chunks, chunk{DelayMs: now.Sub(last).Milliseconds(), Data: string(buf[:n])})
			last = now
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	out, _ := json.MarshalIndent(fx, "", "  ")
	if err := os.WriteFile(filepath.Join(fixtureDir, name+".json"), out, 0o644); err != nil {
		return nil, err
	}
	log.Printf("recorded %s: status %d, %d chunks", name, fx.Status, len(fx.Chunks))
	return fx, nil
}

func writeAPIError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}
