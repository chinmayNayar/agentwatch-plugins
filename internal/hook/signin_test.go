package hook

import (
	"encoding/json"
	"strings"
	"testing"
)

// No config file and no sign-in (a fresh install): hooks still run, answer
// within their contract, queue events locally, and say once per session how
// to sign in.
func TestNotSignedInHooksQueueAndExplain(t *testing.T) {
	api := newFakeAPI(t)
	sandbox(t, api.srv.URL, "dry_run", "fail_open")
	t.Setenv("AGENTWATCH_API_KEY", "")
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/plugins/agentwatch")

	out, code := invoke(t, []string{"claude-code"}, []byte(`{"session_id":"s1","hook_event_name":"SessionStart","cwd":"/tmp","source":"startup"}`))
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("session start output is not JSON: %q", out)
	}
	msg, _ := m["systemMessage"].(string)
	if !strings.Contains(msg, "not signed in") || !strings.Contains(msg, "/agentwatch:setup") {
		t.Fatalf("message: %q", msg)
	}

	out, _ = invoke(t, []string{"claude-code"}, []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"go build ./..."}}`))
	if strings.TrimSpace(out) != "" {
		t.Errorf("dry_run without sign-in must not decide: %q", out)
	}
	invoke(t, []string{"claude-code"}, []byte(`{"session_id":"s1","hook_event_name":"PostToolUse","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"go build ./..."},"tool_response":{"stdout":"ok"}}`))

	recs := spooled(t)
	if len(recs) == 0 {
		t.Fatal("nothing queued")
	}
	for _, r := range recs {
		if r["type"] == "event" {
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "sensor_preflight_unavailable") {
				t.Errorf("not being signed in is not an outage: %s", b)
			}
		}
	}
	if n := len(api.requests); n != 0 {
		t.Errorf("a signed-out sensor called the API %d times", n)
	}
}

func TestNotSignedInFailClosedSaysWhy(t *testing.T) {
	api := newFakeAPI(t)
	sandbox(t, api.srv.URL, "enforce", "fail_closed")
	t.Setenv("AGENTWATCH_API_KEY", "")
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/plugins/agentwatch")
	out, _ := invoke(t, []string{"claude-code"}, []byte(`{"session_id":"s2","hook_event_name":"PreToolUse","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"go build ./..."}}`))
	if !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "not signed in") || !strings.Contains(out, "/agentwatch:setup") {
		t.Fatalf("out: %s", out)
	}
}

// Signed in: no session-start message.
func TestSignedInSessionStartIsQuiet(t *testing.T) {
	api := newFakeAPI(t)
	sandbox(t, api.srv.URL, "dry_run", "fail_open")
	out, _ := invoke(t, []string{"claude-code"}, []byte(`{"session_id":"s3","hook_event_name":"SessionStart","cwd":"/tmp","source":"startup"}`))
	if strings.TrimSpace(out) != "" {
		t.Fatalf("out: %q", out)
	}
}
