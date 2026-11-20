package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/login"
	"github.com/chinmayNayar/agentwatch-plugins/internal/spool"
)

func sandbox(t *testing.T, api string) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("AGENTWATCH_HOME", filepath.Join(h, ".aw"))
	t.Setenv("AGENTWATCH_USER_HOME", h)
	t.Setenv("AGENTWATCH_NO_KEYCHAIN", "1")
	t.Setenv("AGENTWATCH_NO_BROWSER", "1")
	t.Setenv("AGENTWATCH_NO_FLUSH", "1")
	for _, k := range []string{"AGENTWATCH_API_KEY", "AGENTWATCH_TOKEN", "CLAUDE_PLUGIN_OPTION_API_KEY", "AGENTWATCH_MODE",
		"CLAUDE_PLUGIN_OPTION_MODE", "CLAUDE_PLUGIN_OPTION_API_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("AGENTWATCH_API_URL", api)
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/plugins/agentwatch") // as Claude Code sets it for the MCP server
	return h
}

// session drives one MCP server over a pipe, like Claude Code does.
type session struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
}

func startSession(t *testing.T) *session {
	pr, pw := io.Pipe()
	or, ow := io.Pipe()
	go func() { _ = New().Serve(pr, ow); ow.Close() }()
	s := &session{t: t, in: pw, out: bufio.NewScanner(or)}
	s.out.Buffer(make([]byte, 1<<20), 1<<20)
	t.Cleanup(func() { pw.Close() })
	return s
}

func (s *session) call(name string, args map[string]any) (text string, isErr bool) {
	s.id++
	a, _ := json.Marshal(args)
	fmt.Fprintf(s.in, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`+"\n", s.id, name, a)
	if !s.out.Scan() {
		s.t.Fatal("no answer")
	}
	var r struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(s.out.Bytes(), &r); err != nil || len(r.Result.Content) == 0 {
		s.t.Fatalf("bad answer: %s", s.out.Text())
	}
	return r.Result.Content[0].Text, r.Result.IsError
}

// fakeAPI approves the device code after `pendingPolls` polls.
func fakeAPI(t *testing.T, pendingPolls int32, deny bool) (*httptest.Server, *atomic.Int32) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devices/code":
			_, _ = w.Write([]byte(`{"device_code":"dc-secret-123456","user_code":"WXYZ-2345","verification_uri":"https://app.example/devices/activate",` +
				`"verification_uri_complete":"https://app.example/devices/activate?code=WXYZ-2345","interval":1,"expires_in":600}`))
		case "/devices/token":
			n := polls.Add(1)
			if deny {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"detail":{"error":"access_denied"}}`))
				return
			}
			if n <= pendingPolls {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"detail":{"error":"authorization_pending"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"awdv_` + strings.Repeat("a", 48) + `","token_type":"device","device_id":"dev-42","user_label":"dev"}`))
		case "/devices/policy":
			if r.Header.Get("x-api-key") == "" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"agent_name":"` + r.URL.Query().Get("agent_name") + `","policy_mode":"monitor","passport_id":"p1"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &polls
}

// /agentwatch:setup: start, show the code, wait, signed in, token stored,
// passport cached, status says so.
func TestLoginToolSignsTheMachineIn(t *testing.T) {
	srv, _ := fakeAPI(t, 1, false)
	sandbox(t, srv.URL)
	s := startSession(t)

	text, isErr := s.call("agentwatch_login", map[string]any{})
	if isErr || !strings.Contains(text, "WXYZ-2345") || !strings.Contains(text, "devices/activate?code=WXYZ-2345") {
		t.Fatalf("start: %v %s", isErr, text)
	}
	if strings.Contains(text, "dc-secret") {
		t.Fatal("the device code (a secret) was shown")
	}
	p := login.LoadPending()
	if p == nil || p.UserCode != "WXYZ-2345" {
		t.Fatalf("pending file: %+v", p)
	}
	if b, _ := os.ReadFile(login.PendingPath()); strings.Contains(string(b), "dc-secret") {
		t.Fatal("the device code was written to disk")
	}
	// Calling again without wait does not start a second sign-in.
	text, _ = s.call("agentwatch_login", map[string]any{})
	if !strings.Contains(text, "Still waiting") && !strings.Contains(text, "Signed in") {
		t.Fatalf("second call: %s", text)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		text, isErr = s.call("agentwatch_login", map[string]any{"wait": true})
		if isErr {
			t.Fatalf("wait: %s", text)
		}
		if strings.Contains(text, "Signed in") || time.Now().After(deadline) {
			break
		}
	}
	if !strings.Contains(text, "Signed in as device dev-42") || !strings.Contains(text, "monitor") {
		t.Fatalf("result: %s", text)
	}
	cred, err := config.LoadCredential()
	if err != nil || cred.Kind != "device" || cred.DeviceID != "dev-42" {
		t.Fatalf("credential: %+v %v", cred, err)
	}
	if login.LoadPending() != nil {
		t.Error("pending sign-in left behind")
	}
	status, _ := s.call("agentwatch_status", map[string]any{})
	if !strings.Contains(status, "Signed in:  yes") || !strings.Contains(status, "monitor") {
		t.Errorf("status after sign-in:\n%s", status)
	}
	// And now it refuses to sign in again.
	text, _ = s.call("agentwatch_login", map[string]any{})
	if !strings.Contains(text, "Already signed in") {
		t.Errorf("re-login: %s", text)
	}
}

func TestLoginToolReportsRefusal(t *testing.T) {
	srv, _ := fakeAPI(t, 0, true)
	sandbox(t, srv.URL)
	s := startSession(t)
	s.call("agentwatch_login", map[string]any{})
	text, isErr := s.call("agentwatch_login", map[string]any{"wait": true})
	if !isErr || !strings.Contains(text, "refused") {
		t.Fatalf("%v %s", isErr, text)
	}
	if _, err := config.LoadCredential(); err == nil {
		t.Fatal("a refused sign-in stored a credential")
	}
}

// The login tool takes nothing the agent could use to redirect the sign-in.
func TestLoginToolSchema(t *testing.T) {
	for _, tl := range New().tools {
		if tl.Name != "agentwatch_login" {
			continue
		}
		props := tl.InputSchema["properties"].(map[string]any)
		if len(props) != 1 || props["wait"] == nil {
			t.Fatalf("login tool inputs: %v", props)
		}
		return
	}
	t.Fatal("no agentwatch_login tool")
}

// The first real install showed "dry_run ()", "no passports" and told the
// user to run a bare `agentwatch login` that was not on PATH.
func TestStatusBeforeSignIn(t *testing.T) {
	sandbox(t, "https://api.example")
	sp := spool.New(config.SpoolDir())
	for i := 0; i < 11; i++ {
		if err := sp.Append("ingest", map[string]any{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	s := startSession(t)
	text, _ := s.call("agentwatch_status", map[string]any{})
	for _, want := range []string{"dry_run (default)", "/agentwatch:setup", "sign in to load your passport", "Queued:     11 (kept on this machine; sent after sign-in)"} {
		if !strings.Contains(text, want) {
			t.Errorf("status lacks %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"dry_run ()", "no passports", "run agentwatch login"} {
		if strings.Contains(text, bad) {
			t.Errorf("status still says %q:\n%s", bad, text)
		}
	}
	// A bare `agentwatch` is only suggested when it is on PATH.
	if !strings.Contains(text, "CLI:        agentwatch (on PATH)") {
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "terminal:") && strings.Contains(line, " agentwatch login") && !strings.Contains(line, "/") {
				t.Errorf("bare command suggested: %s", line)
			}
		}
	}
	// Tools that need the API explain the same way.
	text, isErr := s.call("agentwatch_runs", map[string]any{})
	if !isErr || !strings.Contains(text, "not signed in") || !strings.Contains(text, "/agentwatch:setup") {
		t.Errorf("runs before sign-in: %v %s", isErr, text)
	}
}

func TestModeSourceIsNamed(t *testing.T) {
	sandbox(t, "https://api.example")
	t.Setenv("CLAUDE_PLUGIN_OPTION_MODE", " Enforce ")
	s := startSession(t)
	text, _ := s.call("agentwatch_status", map[string]any{})
	if !strings.Contains(text, "enforce (Claude Code plugin option)") {
		t.Errorf("status:\n%s", text)
	}
	text, _ = s.call("agentwatch_mode", map[string]any{})
	if strings.Contains(text, "()") || !strings.Contains(text, "set by: CLAUDE_PLUGIN_OPTION_MODE") {
		t.Errorf("mode: %s", text)
	}
}
