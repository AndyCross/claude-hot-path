// runner launches one client, feeds it the prompt on stdin, and reports what
// the kernel knows about it: wall time from spawn to exit, peak RSS, exit code,
// and the bytes it printed. It is copied into each client image at bench time
// so every language is measured by the same few lines of Go.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type report struct {
	SpawnUnixNs int64   `json:"spawn_unix_ns"`
	ExitUnixNs  int64   `json:"exit_unix_ns"`
	WallMs      float64 `json:"wall_ms"`
	// When the first byte that is not whitespace reached stdout. A client that
	// buffers the reply and prints it at the end has this close to its exit.
	FirstTextUnixNs int64  `json:"first_text_unix_ns"`
	MaxRSSKB        int64  `json:"max_rss_kb"`
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: runner <command> [args...]  (prompt on stdin)")
		os.Exit(2)
	}
	prompt, _ := os.ReadFile("/dev/stdin")
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin = bytes.NewReader(prompt)
	var out, errb bytes.Buffer
	cmd.Stderr = &errb
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var firstText int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				if firstText == 0 && len(bytes.TrimSpace(buf[:n])) > 0 {
					firstText = time.Now().UnixNano()
				}
				out.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	spawn := time.Now()
	err = cmd.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	<-done
	werr := cmd.Wait()
	exit := time.Now()
	rep := report{
		SpawnUnixNs: spawn.UnixNano(), ExitUnixNs: exit.UnixNano(),
		WallMs: float64(exit.Sub(spawn).Microseconds()) / 1000, FirstTextUnixNs: firstText,
		Stdout: out.String(), Stderr: errb.String(),
	}
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		rep.MaxRSSKB = rssKB(ru)
	}
	if werr != nil {
		if ee, ok := werr.(*exec.ExitError); ok {
			rep.ExitCode = ee.ExitCode()
		} else {
			rep.ExitCode = -1
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.Encode(rep)
	_ = strings.TrimSpace
}
