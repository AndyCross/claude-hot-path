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
	SpawnUnixNs int64  `json:"spawn_unix_ns"`
	ExitUnixNs  int64  `json:"exit_unix_ns"`
	WallMs      float64 `json:"wall_ms"`
	MaxRSSKB    int64  `json:"max_rss_kb"`
	ExitCode    int    `json:"exit_code"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
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
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Env = os.Environ()
	spawn := time.Now()
	err := cmd.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	werr := cmd.Wait()
	exit := time.Now()
	rep := report{
		SpawnUnixNs: spawn.UnixNano(), ExitUnixNs: exit.UnixNano(),
		WallMs: float64(exit.Sub(spawn).Microseconds()) / 1000,
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
