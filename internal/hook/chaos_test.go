package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
)

// The secret every chaos case carries; it must never appear in any output.
var chaosSecret = "sk-proj-" + strings.Repeat("C", 28)

func chaosPayload(client string) ([]string, []byte) {
	switch client {
	case "cursor":
		return []string{"cursor"}, mustJSON(map[string]any{"hook_event_name": "beforeShellExecution", "conversation_id": "c1",
			"cursor_version": "3.19.7", "command": "deploy --token " + chaosSecret, "cwd": "/tmp/p"})
	case "copilot":
		return []string{"copilot", "preToolUse"}, mustJSON(map[string]any{"sessionId": "s1", "toolName": "bash",
			"toolArgs": map[string]any{"command": "deploy --token " + chaosSecret}})
	case "codex":
		return []string{"codex"}, mustJSON(map[string]any{"hook_event_name": "PreToolUse", "session_id": "s1", "turn_id": "t",
			"tool_name": "Bash", "tool_input": map[string]any{"command": "deploy --token " + chaosSecret}})
	}
	return []string{"claude-code"}, mustJSON(map[string]any{"hook_event_name": "PreToolUse", "session_id": "s1",
		"tool_name": "Bash", "tool_use_id": "u1", "tool_input": map[string]any{"command": "deploy --token " + chaosSecret}})
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// blocked parses each client's output and reports whether it denies.
func blocked(t *testing.T, client, out string) bool {
	t.Helper()
	s := strings.TrimSpace(out)
	if s == "" {
		if client == "cursor" {
			t.Fatalf("cursor permission event got empty output (Cursor treats that as a block)")
		}
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%s output is not one JSON object: %q", client, out)
	}
	if p, ok := m["permission"]; ok {
		return p == "deny"
	}
	if d, ok := m["permissionDecision"]; ok {
		return d == "deny"
	}
	if h, ok := m["hookSpecificOutput"].(map[string]any); ok {
		return h["permissionDecision"] == "deny"
	}
	return false
}

type chaosCase struct {
	name  string
	setup func(t *testing.T, f *fakeAPI)
	// isolation-type failures block in enforce even with fail_open.
	isolation bool
}

var chaosCases = []chaosCase{
	{"api_down", func(t *testing.T, f *fakeAPI) { t.Setenv("AGENTWATCH_API_URL", "http://127.0.0.1:1") }, false},
	{"slow_beyond_timeout", func(t *testing.T, f *fakeAPI) {
		t.Setenv("AGENTWATCH_PREFLIGHT_TIMEOUT_MS", "150")
		f.delay = 700 * time.Millisecond
	}, false},
	{"401", func(t *testing.T, f *fakeAPI) {
		f.preflight = func(map[string]any) (int, any) { return 401, map[string]any{"detail": "Unauthorized"} }
	}, false},
	{"403_isolated", func(t *testing.T, f *fakeAPI) { f.runStatus = 403 }, true},
	{"409_run_killed", func(t *testing.T, f *fakeAPI) {
		f.preflight = func(map[string]any) (int, any) { return 409, map[string]any{"detail": "Run is not accepting steps"} }
	}, true},
	{"429", func(t *testing.T, f *fakeAPI) {
		f.preflight = func(map[string]any) (int, any) { return 429, map[string]any{"detail": "rate limited"} }
	}, false},
	{"500", func(t *testing.T, f *fakeAPI) {
		f.preflight = func(map[string]any) (int, any) { return 500, map[string]any{"detail": "boom"} }
	}, false},
	{"invalid_json", func(t *testing.T, f *fakeAPI) { f.rawBody = "<html>not json" }, false},
	{"clock_skew", func(t *testing.T, f *fakeAPI) {}, false}, // no signed timestamps: behaves like a normal allow
	{"missing_keychain_no_credentials", func(t *testing.T, f *fakeAPI) {
		t.Setenv("AGENTWATCH_API_KEY", "")
		t.Setenv("AGENTWATCH_NO_KEYCHAIN", "1")
	}, false},
}

func TestChaosMatchesFailureModes(t *testing.T) {
	for _, cl := range []string{"claude-code", "cursor", "codex", "copilot"} {
		for _, cc := range chaosCases {
			for _, mode := range []struct{ mode, fail string }{{"dry_run", "fail_open"}, {"enforce", "fail_open"}, {"enforce", "fail_closed"}} {
				cl, cc, mode := cl, cc, mode
				t.Run(cl+"/"+cc.name+"/"+mode.mode+"+"+mode.fail, func(t *testing.T) {
					f := newFakeAPI(t)
					sandbox(t, f.srv.URL, mode.mode, mode.fail)
					cc.setup(t, f)
					args, payload := chaosPayload(cl)
					start := time.Now()
					out, code := invoke(t, args, payload)
					elapsed := time.Since(start)
					if code != 0 {
						t.Fatalf("exit %d", code)
					}
					if elapsed > 3*time.Second {
						t.Fatalf("hook took %v", elapsed)
					}
					got := blocked(t, cl, out)
					want := false
					switch {
					case mode.mode == "dry_run":
						want = false // dry_run never blocks
					case cc.isolation:
						want = true // isolated / stopped session: blocked in enforce
					case cc.name == "clock_skew":
						want = false
					default:
						want = mode.fail == "fail_closed"
					}
					if got != want {
						t.Fatalf("blocked=%v, want %v; output %s", got, want, out)
					}
					assertNoSecret(t, out, chaosSecret)
				})
			}
		}
	}
}

func assertNoSecret(t *testing.T, out, secret string) {
	t.Helper()
	if strings.Contains(out, secret) {
		t.Fatalf("secret printed to the client: %s", out)
	}
	_ = filepath.Walk(config.Home(), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret written to %s", p)
		}
		if strings.Contains(string(b), testToken) && !strings.HasSuffix(p, "credentials") {
			t.Fatalf("API token written to %s", p)
		}
		return nil
	})
}

func TestSlowAPIIsCutAtTheTimeout(t *testing.T) {
	f := newFakeAPI(t)
	sandbox(t, f.srv.URL, "enforce", "fail_closed")
	t.Setenv("AGENTWATCH_PREFLIGHT_TIMEOUT_MS", "200")
	f.delay = 2 * time.Second
	args, payload := chaosPayload("claude-code")
	start := time.Now()
	out, _ := invoke(t, args, payload)
	if el := time.Since(start); el > 900*time.Millisecond {
		t.Fatalf("waited %v for a 200 ms timeout", el)
	}
	if !blocked(t, "claude-code", out) {
		t.Fatalf("fail_closed did not block: %s", out)
	}
}

func TestFullDiskDoesNotChangeTheDecision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not make writes fail on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newFakeAPI(t)
	f.preflight = func(map[string]any) (int, any) {
		return 200, map[string]any{"decision": "deny", "reasons": []string{"x"}, "display_reason": "AgentWatch blocked"}
	}
	sandbox(t, f.srv.URL, "enforce", "fail_open")
	_ = os.MkdirAll(config.SpoolDir(), 0o700)
	_ = os.Chmod(config.SpoolDir(), 0o500) // writes fail like a full disk
	t.Cleanup(func() { _ = os.Chmod(config.SpoolDir(), 0o700) })
	args, payload := chaosPayload("claude-code")
	out, code := invoke(t, args, payload)
	if code != 0 || !blocked(t, "claude-code", out) {
		t.Fatalf("decision changed when the spool could not be written: %d %s", code, out)
	}
}

func TestSpoolLimitDropsAndCounts(t *testing.T) {
	f := newFakeAPI(t)
	sandbox(t, f.srv.URL, "dry_run", "fail_open")
	_ = os.MkdirAll(config.SpoolDir(), 0o700)
	big := strings.Repeat("x", 1000)
	_ = os.WriteFile(filepath.Join(config.SpoolDir(), "seg-0.jsonl"), []byte(strings.Repeat(big+"\n", 21*1024)), 0o600)
	payload := mustJSON(map[string]any{"hook_event_name": "PostToolUse", "session_id": "s1", "tool_name": "Read",
		"tool_input": map[string]any{"file_path": "a"}, "tool_response": "ok", "tool_use_id": "u"})
	out, code := invoke(t, []string{"claude-code"}, payload)
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("unexpected %d %q", code, out)
	}
	b, err := os.ReadFile(filepath.Join(config.SpoolDir(), "dropped"))
	if err != nil || strings.TrimSpace(string(b)) == "0" {
		t.Fatalf("drop not counted: %v %q", err, b)
	}
}

func TestCorruptConfigFallsBackWithoutLockingOut(t *testing.T) {
	f := newFakeAPI(t)
	f.preflight = func(map[string]any) (int, any) {
		return 200, map[string]any{"decision": "deny", "reasons": []string{"x"}, "display_reason": "AgentWatch blocked"}
	}
	sandbox(t, f.srv.URL, "", "")
	_ = os.MkdirAll(config.Home(), 0o700)
	_ = os.WriteFile(config.Path(), []byte("{not json"), 0o600)
	args, payload := chaosPayload("claude-code")
	out, _ := invoke(t, args, payload)
	if blocked(t, "claude-code", out) {
		t.Fatalf("corrupt config with no last-good state must fall back to dry_run: %s", out)
	}
	// With a last-good enforce state, a broken file must not downgrade to dry_run.
	_ = os.MkdirAll(config.StateDir(), 0o700)
	_ = os.WriteFile(config.LastGoodPath(), []byte(`{"Mode":"enforce","FailMode":"fail_open","APIURL":"`+f.srv.URL+`"}`), 0o600)
	t.Setenv("AGENTWATCH_API_URL", f.srv.URL)
	out, _ = invoke(t, args, payload)
	if !blocked(t, "claude-code", out) {
		t.Fatalf("last-good enforce was not kept: %s", out)
	}
}

func TestPanicEndsInTheFailModeAnswer(t *testing.T) {
	f := newFakeAPI(t)
	for _, m := range []struct {
		mode, fail string
		block      bool
	}{{"dry_run", "fail_open", false}, {"enforce", "fail_closed", true}} {
		sandbox(t, f.srv.URL, m.mode, m.fail)
		args, payload := chaosPayload("cursor")
		var out strings.Builder
		code := Run(args, Options{Stdin: strings.NewReader(string(payload)), Stdout: &out, NoFlush: true,
			ParentArgs: func() []string { return nil },
			NewClient:  func(string, string) *client.Client { panic("injected") }})
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if got := blocked(t, "cursor", out.String()); got != m.block {
			t.Fatalf("%s: blocked=%v want %v (%s)", m.mode, got, m.block, out.String())
		}
	}
}

func TestGarbageStdinIsNeutral(t *testing.T) {
	f := newFakeAPI(t)
	sandbox(t, f.srv.URL, "enforce", "fail_closed")
	for _, args := range [][]string{{"claude-code"}, {"cursor"}, {"codex"}, {"copilot", "preToolUse"}} {
		var out strings.Builder
		code := Run(args, Options{Stdin: strings.NewReader("\x00not json{{"), Stdout: &out, NoFlush: true})
		if code != 0 {
			t.Fatalf("%v exit %d", args, code)
		}
		if s := strings.TrimSpace(out.String()); s != "" {
			var m map[string]any
			if json.Unmarshal([]byte(s), &m) != nil {
				t.Fatalf("%v invalid output %q", args, s)
			}
		}
	}
}
