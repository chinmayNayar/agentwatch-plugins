package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestServerListsReadOnlyToolsAndLogin(t *testing.T) {
	h := t.TempDir()
	t.Setenv("AGENTWATCH_HOME", filepath.Join(h, ".aw"))
	t.Setenv("AGENTWATCH_USER_HOME", h)
	t.Setenv("AGENTWATCH_NO_KEYCHAIN", "1")
	t.Setenv("AGENTWATCH_API_KEY", "")
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"agentwatch_mode","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"agentwatch_set_mode","arguments":{"mode":"dry_run"}}}`,
		`not json`,
	}, "\n")
	var out bytes.Buffer
	if err := New().Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 { // the notification gets no answer
		t.Fatalf("%d answers:\n%s", len(lines), out.String())
	}
	var list struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Annotations map[string]any `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal([]byte(lines[1]), &list)
	if len(list.Result.Tools) != 7 {
		t.Fatalf("tools: %+v", list.Result.Tools)
	}
	for _, tl := range list.Result.Tools {
		// The one tool that is not read-only starts a device sign-in that a
		// person must approve; it takes no URL or key (TestLoginToolSchema).
		if tl.Annotations["readOnlyHint"] != true && tl.Name != "agentwatch_login" {
			t.Fatalf("%s is not read-only", tl.Name)
		}
		if strings.Contains(tl.Name, "set") || strings.Contains(tl.Name, "isolate") {
			t.Fatalf("mutating tool exposed: %s", tl.Name)
		}
	}
	if !strings.Contains(lines[2], "Only a person can change this") {
		t.Fatalf("mode tool: %s", lines[2])
	}
	if !strings.Contains(lines[3], "unknown tool") || !strings.Contains(lines[4], "parse error") {
		t.Fatalf("errors: %s / %s", lines[3], lines[4])
	}
}

func TestDashboardURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.agentwatch.in": "https://app.agentwatch.in",
		"http://localhost:8000":     "http://localhost:3000",
		"https://aw.example.com":    "https://aw.example.com",
	} {
		if got := dashboardURL(in); got != want {
			t.Errorf("%s -> %s want %s", in, got, want)
		}
	}
}

// A device token reads only this device's runs and policy: the runs tool must
// ask /devices/runs, and findings/posture must not call tenant-wide routes.
func TestDeviceTokenToolsStayInDeviceScope(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/devices/runs" {
			_, _ = w.Write([]byte(`{"items":[{"id":"run-1","status":"running","total_steps":3}]}`))
			return
		}
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"detail":{"code":"device_scope"}}`))
	}))
	defer srv.Close()
	h := t.TempDir()
	t.Setenv("AGENTWATCH_HOME", filepath.Join(h, ".aw"))
	t.Setenv("AGENTWATCH_USER_HOME", h)
	t.Setenv("AGENTWATCH_NO_KEYCHAIN", "1")
	t.Setenv("AGENTWATCH_API_URL", srv.URL)
	t.Setenv("AGENTWATCH_API_KEY", "awdv_"+strings.Repeat("0", 48))
	call := func(id int, name string) string {
		in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n" +
			fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"%s","arguments":{}}}`, id, name)
		var out bytes.Buffer
		if err := New().Serve(strings.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := call(2, "agentwatch_runs"); !strings.Contains(got, "run-1") {
		t.Fatalf("runs: %s", got)
	}
	for _, tool := range []string{"agentwatch_findings", "agentwatch_posture"} {
		if got := call(3, tool); !strings.Contains(got, "device token") || !strings.Contains(got, "/findings") && !strings.Contains(got, "/posture") {
			t.Fatalf("%s: %s", tool, got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p != "/devices/runs" {
			t.Fatalf("device token called %s", p)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("calls: %v", paths)
	}
}
