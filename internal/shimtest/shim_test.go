// Package shimtest runs the plugin shim (plugins/claude-code/scripts/aw.sh)
// against a local release server: the first-session race, the lock, the
// SHA-256 pin, and what each caller does while the binary is missing.
package shimtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const ver = "0.0.9"

func assetName() string {
	osn := runtime.GOOS
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	return fmt.Sprintf("agentwatch-%s-%s-%s", ver, osn, arch)
}

// fake is the "binary" the server hands out: a script that says how it ran.
const fake = "#!/bin/sh\necho \"fake-sensor $*\"\n"

type rig struct {
	t       *testing.T
	scripts string // copy of the plugin's scripts dir, with our pin
	data    string // CLAUDE_PLUGIN_DATA
	home    string
	srv     *httptest.Server
	hits    atomic.Int32
	delay   time.Duration
	body    []byte
}

func newRig(t *testing.T, pinned []byte) *rig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("aw.sh is the POSIX shim; aw.ps1 is parsed in CI")
	}
	r := &rig{t: t, body: []byte(fake)}
	root := t.TempDir()
	r.scripts = filepath.Join(root, "plugin", "scripts")
	r.data = filepath.Join(root, "data")
	r.home = filepath.Join(root, "home")
	must(t, os.MkdirAll(r.scripts, 0o755))
	must(t, os.MkdirAll(r.home, 0o755))
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "claude-code", "scripts", "aw.sh"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(r.scripts, "aw.sh"), src, 0o755))
	must(t, os.WriteFile(filepath.Join(r.scripts, "VERSION"), []byte(ver+"\n"), 0o644))
	sum := sha256.Sum256(pinned)
	must(t, os.WriteFile(filepath.Join(r.scripts, "checksums.txt"),
		[]byte("# test pin\n"+hex.EncodeToString(sum[:])+"  "+assetName()+"\n"), 0o644))
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v"+ver+"/"+assetName() {
			http.NotFound(w, req)
			return
		}
		r.hits.Add(1)
		time.Sleep(r.delay)
		_, _ = w.Write(r.body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (r *rig) run(stdin string, args ...string) (stdout, stderr string, code int, took time.Duration) {
	cmd := exec.Command("sh", append([]string{filepath.Join(r.scripts, "aw.sh")}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + r.home, "CLAUDE_PLUGIN_DATA=" + r.data,
		"AGENTWATCH_DOWNLOAD_BASE=" + r.srv.URL,
	}
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	t0 := time.Now()
	err := cmd.Run()
	took = time.Since(t0)
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return o.String(), e.String(), code, took
}

func (r *rig) binPath() string { return filepath.Join(r.data, "bin", assetName()) }

// The first real install: the MCP server and the SessionStart hook start at
// the same moment, before anything is downloaded. Both must end up running
// the binary, and only one download may happen.
func TestFirstSessionRace(t *testing.T) {
	r := newRig(t, []byte(fake))
	r.delay = 700 * time.Millisecond
	type res struct {
		out  string
		code int
	}
	calls := [][]string{
		{"mcp"},
		{"hook", "claude-code", "SessionStart"},
		{"hook", "claude-code", "UserPromptSubmit"},
		{"fetch"},
		{"hook", "claude-code", "SessionStart"},
	}
	results := make([]res, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c []string) {
			defer wg.Done()
			out, _, code, _ := r.run("{}", c...)
			results[i] = res{out, code}
		}(i, c)
	}
	wg.Wait()
	if n := r.hits.Load(); n != 1 {
		t.Errorf("downloads: %d, want exactly 1 (the lock must be shared)", n)
	}
	for i, c := range calls {
		if c[0] == "fetch" {
			if results[i].code != 0 {
				t.Errorf("fetch exit %d", results[i].code)
			}
			continue
		}
		if !strings.Contains(results[i].out, "fake-sensor "+strings.Join(c, " ")) {
			t.Errorf("%v did not run the binary: %q (exit %d)", c, results[i].out, results[i].code)
		}
	}
	// The stable name points at this version.
	if l, err := os.Readlink(filepath.Join(r.data, "bin", "agentwatch")); err != nil || l != assetName() {
		t.Errorf("bin/agentwatch -> %q (%v)", l, err)
	}
	// The downloader releases the lock as it exits, a moment after the
	// binary lands (waiters may already be running it).
	lock := filepath.Join(r.data, "bin", ".download.lock")
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(lock); os.IsNotExist(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("lock left behind")
	}
}

func TestChecksumMismatchIsRefused(t *testing.T) {
	r := newRig(t, []byte("the bytes we pinned"))
	out, _, code, _ := r.run(`{"hook_event_name":"PreToolUse"}`, "hook", "claude-code", "PreToolUse")
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Errorf("hook without a binary must exit 0 with no decision (dry_run): exit %d out %q", code, out)
	}
	_, errOut, code, _ := r.run("", "fetch")
	if code == 0 {
		t.Fatal("fetch succeeded with a mismatching binary")
	}
	if !strings.Contains(errOut, "checksum mismatch") {
		t.Errorf("stderr: %q", errOut)
	}
	if _, err := os.Stat(r.binPath()); !os.IsNotExist(err) {
		t.Error("a mismatching binary was installed")
	}
	// The MCP server says why on stderr and exits non-zero.
	_, errOut, code, _ = r.run("", "mcp")
	if code == 0 || !strings.Contains(errOut, "checksum mismatch") || !strings.Contains(errOut, "/mcp") {
		t.Errorf("mcp: exit %d stderr %q", code, errOut)
	}
}

func TestEnforceFailClosedDeniesWhileMissing(t *testing.T) {
	r := newRig(t, []byte("pinned"))
	must(t, os.MkdirAll(filepath.Join(r.home, ".agentwatch"), 0o700))
	must(t, os.WriteFile(filepath.Join(r.home, ".agentwatch", "config.json"),
		[]byte(`{"mode": "enforce", "fail_mode": "fail_closed"}`), 0o600))
	out, _, code, _ := r.run(`{}`, "hook", "claude-code", "PreToolUse")
	if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Errorf("exit %d out %q", code, out)
	}
}

// A pre-tool hook waits only a moment (its timeout is 5 s); the download it
// started carries on detached, and the next call finds the binary.
func TestSlowDownloadOutlivesAShortHook(t *testing.T) {
	r := newRig(t, []byte(fake))
	r.delay = 5 * time.Second
	out, _, code, took := r.run(`{}`, "hook", "claude-code", "PreToolUse")
	if code != 0 || strings.Contains(out, "fake-sensor") {
		t.Errorf("exit %d out %q", code, out)
	}
	if took > 4500*time.Millisecond {
		t.Errorf("PreToolUse waited %s; its budget is 3 s", took)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(r.binPath()); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	out, _, _, _ = r.run(`{}`, "hook", "claude-code", "PreToolUse")
	if !strings.Contains(out, "fake-sensor hook claude-code PreToolUse") {
		t.Errorf("binary not used after the detached download: %q", out)
	}
	if n := r.hits.Load(); n != 1 {
		t.Errorf("downloads: %d", n)
	}
}

// A lock left by a downloader that was killed must not block forever.
func TestStaleLockIsRecovered(t *testing.T) {
	r := newRig(t, []byte(fake))
	lock := filepath.Join(r.data, "bin", ".download.lock")
	must(t, os.MkdirAll(lock, 0o755))
	must(t, os.WriteFile(filepath.Join(lock, "pid"), []byte("999999\n"), 0o644)) // no such process
	out, _, code, _ := r.run("", "mcp")
	if code != 0 || !strings.Contains(out, "fake-sensor mcp") {
		t.Errorf("exit %d out %q", code, out)
	}
}

func TestMCPWithoutNetworkExplainsItself(t *testing.T) {
	r := newRig(t, []byte(fake))
	r.srv.Close()
	_, errOut, code, took := r.run("", "mcp")
	if code == 0 {
		t.Fatal("mcp exited 0 without a binary")
	}
	if !strings.Contains(errOut, "download failed") || !strings.Contains(errOut, "Reconnect") {
		t.Errorf("stderr %q", errOut)
	}
	if took > 30*time.Second {
		t.Errorf("a failed download should end the wait early, took %s", took)
	}
}

// The download base override must not allow plain http to a remote host.
func TestDownloadBaseHTTPOnlyForLoopback(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "plugins", "claude-code", "scripts", "aw.sh"))
	must(t, err)
	s := string(b)
	if !strings.Contains(s, "proto='=https'") || !strings.Contains(s, "http://127.0.0.1:*|http://localhost:*) proto='=http,https'") {
		t.Error("aw.sh no longer restricts the download protocol")
	}
	// Both plugins ship the same shim.
	c, err := os.ReadFile(filepath.Join("..", "..", "plugins", "cursor", "scripts", "aw.sh"))
	must(t, err)
	if !bytes.Equal(b, c) {
		t.Error("plugins/cursor/scripts/aw.sh differs from the Claude Code copy")
	}
}
