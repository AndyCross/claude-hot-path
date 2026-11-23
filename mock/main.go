// mock replays recorded Claude Messages API exchanges as Server-Sent Events
// with their original timing, so a client can be measured without a key and
// without the network. With UPSTREAM set it proxies to the real API instead,
// injecting the key from ANTHROPIC_API_KEY and recording new fixtures.
//
// The replaying and recording are github.com/AndyCross/claude-like-replayable.
// What is the benchmark's own is here: which fixture answers a request, and
// the timings /stats reports.
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

	replayable "github.com/AndyCross/claude-like-replayable"
)

type stats struct {
	Scenario     string  `json:"scenario"`
	ReceivedNs   int64   `json:"received_unix_ns"`
	FirstByteNs  int64   `json:"first_byte_unix_ns"`
	DoneNs       int64   `json:"done_unix_ns"`
	ReplayMs     int64   `json:"replay_ms"` // total of the fixture's delays: the mock's own time
	RequestBytes int     `json:"request_bytes"`
	Remote       string  `json:"remote"`        // client address:port, so a reused connection is visible
	ChunkNs      []int64 `json:"chunk_unix_ns"` // when each chunk was written, for the streaming check
}

var (
	mu      sync.Mutex
	history []stats
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	srv := &replayable.Server{
		Dir:      env("FIXTURE_DIR", "/fixtures"),
		Upstream: os.Getenv("UPSTREAM"),
		Choose:   scenario,
		Observe:  observe,
	}
	http.Handle("/v1/messages", srv)
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
	if srv.Upstream != "" {
		mode = "record via " + srv.Upstream
	}
	log.Printf("mock listening on %s, %s, fixtures in %s", addr, mode, srv.Dir)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// scenario picks the fixture from the request alone, so clients need no
// special header: a bad key is the error case, a tool_result in the messages
// is the second turn, anything else is the first turn.
func scenario(r *http.Request, body []byte) (string, error) {
	if replayable.BadKey(r) {
		return "error-401", nil
	}
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "error-400", nil
	}
	for _, m := range req.Messages {
		if bytes.Contains(m.Content, []byte(`"tool_result"`)) {
			return "turn2", nil
		}
	}
	return "turn1", nil
}

func observe(ex replayable.Exchange) {
	st := stats{
		Scenario:     ex.Name,
		ReceivedNs:   ex.Received.UnixNano(),
		DoneNs:       ex.Done.UnixNano(),
		ReplayMs:     ex.ReplayMs,
		RequestBytes: ex.RequestBytes,
		Remote:       ex.Remote,
	}
	if len(ex.Chunks) > 0 {
		st.FirstByteNs = ex.FirstByte.UnixNano()
	}
	for _, t := range ex.Chunks {
		st.ChunkNs = append(st.ChunkNs, t.UnixNano())
	}
	mu.Lock()
	history = append(history, st)
	mu.Unlock()
}
