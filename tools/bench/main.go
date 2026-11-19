// Command bench measures hook overhead the way a client pays it: it starts
// the agentwatch binary as a new process per call, writes one payload to its
// stdin and waits for it to exit. Numbers include process start-up.
//
//	go run ./tools/bench -bin ./agentwatch -n 300 -out docs/benchmarks.json
//
// Scenarios:
//
//	local_allow   Read tool: decided locally, no network
//	local_deny    tool on the cached denied list: decided locally, report spooled
//	server_allow  Bash: POST /steps/preflight to the configured API
//
// The environment (AGENTWATCH_HOME, API URL, credentials) is the caller's.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"time"
)

type result struct {
	Scenario string  `json:"scenario"`
	N        int     `json:"n"`
	P50      float64 `json:"p50_ms"`
	P95      float64 `json:"p95_ms"`
	P99      float64 `json:"p99_ms"`
	Max      float64 `json:"max_ms"`
}

func payload(tool, cmd string, i int) []byte {
	in := map[string]any{"command": cmd}
	if tool == "Read" {
		in = map[string]any{"file_path": "/tmp/bench/README.md"}
	}
	b, _ := json.Marshal(map[string]any{"session_id": "bench-session", "transcript_path": "/dev/null", "cwd": "/tmp/bench",
		"permission_mode": "default", "hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": in,
		"tool_use_id": fmt.Sprintf("toolu_bench_%d", i)})
	return b
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	i := int(float64(len(xs)-1) * p)
	return xs[i]
}

func run(bin string, n int, scenario, tool, cmd string) result {
	var ms []float64
	for i := 0; i < n+5; i++ {
		c := exec.Command(bin, "hook", "claude-code", "PreToolUse")
		c.Stdin = bytes.NewReader(payload(tool, cmd, i))
		var out bytes.Buffer
		c.Stdout = &out
		t0 := time.Now()
		if err := c.Run(); err != nil {
			fmt.Fprintln(os.Stderr, scenario, "error:", err)
		}
		d := float64(time.Since(t0).Microseconds()) / 1000
		if i >= 5 { // warm-up
			ms = append(ms, d)
		}
	}
	sort.Float64s(ms)
	return result{Scenario: scenario, N: n, P50: pct(ms, .5), P95: pct(ms, .95), P99: pct(ms, .99), Max: ms[len(ms)-1]}
}

func main() {
	bin := flag.String("bin", "./agentwatch", "binary under test")
	n := flag.Int("n", 300, "calls per scenario")
	out := flag.String("out", "", "write JSON here")
	flag.Parse()
	res := []result{
		run(*bin, *n, "local_allow", "Read", ""),
		run(*bin, *n, "local_deny", "DeniedTool", "x"),
		run(*bin, *n, "server_allow", "Bash", "echo bench"),
	}
	meta := map[string]any{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "cpus": runtime.NumCPU(),
		"date": time.Now().UTC().Format(time.RFC3339), "results": res}
	b, _ := json.MarshalIndent(meta, "", "  ")
	fmt.Println(string(b))
	if *out != "" {
		_ = os.WriteFile(*out, append(b, '\n'), 0o644)
	}
}
