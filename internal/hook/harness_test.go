package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
)

// fakeAPI is a scriptable AgentWatch API.
type fakeAPI struct {
	mu        sync.Mutex
	srv       *httptest.Server
	requests  []recorded
	policy    map[string]any
	preflight func(body map[string]any) (int, any)
	runStatus int
	delay     time.Duration
	rawBody   string // when set, /steps/preflight returns this body verbatim
}

type recorded struct {
	Method, Path string
	Body         map[string]any
	APIKey       string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		policy: map[string]any{"agent_name": "x", "policy_mode": "dry_run", "enforce_mode": "fail_closed",
			"allowed_tools": []string{"*"}, "denied_tools": []string{}, "approval_required_tools": []string{},
			"isolated": false, "etag": "e1"},
		preflight: func(map[string]any) (int, any) {
			return 200, map[string]any{"decision": "allow", "reasons": []string{}, "display_reason": "AgentWatch allowed"}
		},
		runStatus: 200,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, body, r.Header.Get("x-api-key")})
	delay, pf, raw := f.delay, f.preflight, f.rawBody
	pol, rs := f.policy, f.runStatus
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/health":
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case "/devices/policy":
		p := map[string]any{}
		for k, v := range pol {
			p[k] = v
		}
		p["agent_name"] = r.URL.Query().Get("agent_name")
		_ = json.NewEncoder(w).Encode(p)
	case "/runs":
		w.WriteHeader(rs)
		if rs == 200 {
			_, _ = w.Write([]byte(`{"run_id":"run-1","status":"created"}`))
		} else {
			_, _ = w.Write([]byte(`{"detail":{"code":"agent_isolated"}}`))
		}
	case "/steps/preflight":
		if raw != "" {
			_, _ = w.Write([]byte(raw))
			return
		}
		code, out := pf(body)
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	case "/ingest", "/devices/events", "/devices/heartbeat":
		_, _ = w.Write([]byte(`{"ok":true}`))
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeAPI) calls(path string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.requests {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

var testToken = "aw_" + strings.Repeat("t", 32) // assembled: no secret-shaped literal in the repo

// sandbox isolates config, state, spool and credentials for one test.
func sandbox(t *testing.T, apiURL, mode, failMode string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGENTWATCH_HOME", filepath.Join(home, ".agentwatch"))
	t.Setenv("AGENTWATCH_USER_HOME", home)
	t.Setenv("AGENTWATCH_NO_KEYCHAIN", "1")
	t.Setenv("AGENTWATCH_NO_FLUSH", "1")
	t.Setenv("AGENTWATCH_API_URL", apiURL)
	t.Setenv("AGENTWATCH_API_KEY", testToken)
	t.Setenv("AGENTWATCH_MODE", mode)
	t.Setenv("AGENTWATCH_FAIL_MODE", failMode)
	t.Setenv("USER", "dev")
	for _, k := range []string{"CLAUDE_PLUGIN_OPTION_MODE", "CLAUDE_PLUGIN_OPTION_API_KEY", "CLAUDE_PLUGIN_OPTION_API_URL",
		"CURSOR_VERSION", "AGENTWATCH_TOKEN", "AGENTWATCH_STRICT", "AGENTWATCH_PREFLIGHT_TIMEOUT_MS"} {
		t.Setenv(k, "")
	}
	return home
}

// invoke runs one hook in-process.
func invoke(t *testing.T, args []string, payload []byte) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := Run(args, Options{Stdin: bytes.NewReader(payload), Stdout: &out, NoFlush: true,
		ParentArgs: func() []string { return nil }})
	return out.String(), code
}

func spooled(t *testing.T) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(config.SpoolDir(), "active.jsonl"))
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("spool line not JSON: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// fixtureSecrets fills placeholders in fixtures with fake secrets built at
// runtime, so no key-shaped literal is committed (gitleaks runs on every push).
var fixtureSecrets = map[string]string{
	"${FAKE_OPENAI_KEY}": "sk-proj-" + strings.Repeat("A", 28),
}

func loadFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for ph, v := range fixtureSecrets {
		if bytes.Contains(b, []byte(ph)) {
			b = bytes.ReplaceAll(b, []byte(ph), []byte(v))
			if !bytes.Contains(b, []byte(v)) {
				t.Fatalf("%s: placeholder %s not filled", path, ph)
			}
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "_source")
	out, _ := json.Marshal(m)
	return out
}

// argsFor: client from the directory, Copilot's event from the file name.
func argsFor(path string) []string {
	cl := filepath.Base(filepath.Dir(path))
	args := []string{cl}
	if cl == "copilot" {
		ev := strings.SplitN(strings.TrimSuffix(filepath.Base(path), ".json"), "_", 2)[0]
		args = append(args, ev)
	}
	return args
}

func fixtures(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../../testdata/fixtures/*/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	return files
}
