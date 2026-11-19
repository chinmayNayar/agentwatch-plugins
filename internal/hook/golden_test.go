package hook

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

type scenario struct {
	name, mode, fail string
	server           func(map[string]any) (int, any)
	preToolOnly      bool
}

var scenarios = []scenario{
	{name: "dryrun_allow", mode: "dry_run", fail: "fail_open", server: func(map[string]any) (int, any) {
		return 200, map[string]any{"decision": "allow", "reasons": []string{}, "display_reason": "AgentWatch allowed"}
	}},
	{name: "dryrun_deny", mode: "dry_run", fail: "fail_open", preToolOnly: true, server: func(map[string]any) (int, any) {
		return 200, map[string]any{"decision": "deny", "reasons": []string{"rule 'no-force-push' -> deny"},
			"display_reason": "AgentWatch blocked this call: rule 'no-force-push'"}
	}},
	{name: "enforce_deny", mode: "enforce", fail: "fail_open", preToolOnly: true, server: func(map[string]any) (int, any) {
		return 200, map[string]any{"decision": "deny", "reasons": []string{"rule 'no-force-push' -> deny"},
			"display_reason": "AgentWatch blocked this call: rule 'no-force-push'"}
	}},
	{name: "enforce_hold", mode: "enforce", fail: "fail_open", preToolOnly: true, server: func(map[string]any) (int, any) {
		return 200, map[string]any{"decision": "hold", "hold_id": "hold-1234567890", "reasons": []string{"needs approval"},
			"display_reason": "AgentWatch is holding this call"}
	}},
	{name: "enforce_scrub", mode: "enforce", fail: "fail_open", preToolOnly: true, server: func(map[string]any) (int, any) {
		return 200, map[string]any{"decision": "scrub", "reasons": []string{"secret in the call's arguments"},
			"display_reason": "AgentWatch redacted secrets", "scrubbed_input": map[string]any{"command": "echo [REDACTED]"}}
	}},
}

// normalise removes values that change per run.
func normalise(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			switch k {
			case "id", "ts", "event_id", "latency_ms", "sha256_12":
				out[k] = "<var>"
				continue
			}
			out[k] = normalise(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalise(x)
		}
		if len(out) > 0 {
			if _, ok := out[0].(string); ok {
				strs := make([]string, len(out))
				for i, x := range out {
					strs[i], _ = x.(string)
				}
				sort.Strings(strs)
				for i := range strs {
					out[i] = strs[i]
				}
			}
		}
		return out
	case string:
		if strings.HasPrefix(t, "sensor:") {
			return "sensor:<version>"
		}
		if strings.HasPrefix(t, "os:") {
			return "os:<os>"
		}
		return t
	}
	return v
}

func isPreTool(path string) bool {
	b := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(b, "pretooluse") || strings.HasPrefix(b, "beforeshell") ||
		strings.HasPrefix(b, "beforemcp") || strings.HasPrefix(b, "beforereadfile")
}

// TestGolden: every fixture × scenario → exact client output, exact spooled
// report and exact preflight request.
func TestGolden(t *testing.T) {
	for _, sc := range scenarios {
		for _, fx := range fixtures(t) {
			if sc.preToolOnly && !isPreTool(fx) {
				continue
			}
			sc, fx := sc, fx
			name := sc.name + "/" + filepath.Base(filepath.Dir(fx)) + "/" + strings.TrimSuffix(filepath.Base(fx), ".json")
			t.Run(name, func(t *testing.T) {
				api := newFakeAPI(t)
				api.preflight = sc.server
				sandbox(t, api.srv.URL, sc.mode, sc.fail)
				out, code := invoke(t, argsFor(fx), loadFixture(t, fx))
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
				var stdout any
				if s := strings.TrimSpace(out); s != "" {
					if err := json.Unmarshal([]byte(s), &stdout); err != nil {
						t.Fatalf("stdout is not JSON: %q", out)
					}
				}
				var pre []any
				for _, r := range api.calls("/steps/preflight") {
					pre = append(pre, r.Body)
				}
				var sp []any
				for _, r := range spooled(t) {
					sp = append(sp, r)
				}
				got := normalise(map[string]any{"stdout": stdout, "preflight": pre, "spooled": sp})
				gb, _ := json.MarshalIndent(got, "", "  ")
				gpath := filepath.Join("../../testdata/golden", sc.name, filepath.Base(filepath.Dir(fx)),
					strings.TrimSuffix(filepath.Base(fx), ".json")+".golden.json")
				if *update {
					_ = os.MkdirAll(filepath.Dir(gpath), 0o755)
					if err := os.WriteFile(gpath, append(gb, '\n'), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(gpath)
				if err != nil {
					t.Fatalf("missing golden (run go test ./internal/hook -update): %v", err)
				}
				if strings.TrimSpace(string(want)) != strings.TrimSpace(string(gb)) {
					_ = os.WriteFile(gpath+".got", append(gb, '\n'), 0o644) // for diffing; gitignored
					t.Fatalf("golden mismatch for %s\n--- want\n%s\n--- got\n%s", gpath, want, gb)
				}
			})
		}
	}
}
