package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLinkPutsAgentwatchOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link is for macOS and Linux")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	t.Setenv("PATH", "/usr/bin:/bin")
	var out, errOut bytes.Buffer
	if code := run([]string{"link", "--dir", dir}, nil, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	dst := filepath.Join(dir, "agentwatch")
	target, err := os.Readlink(dst)
	if err != nil || target != selfPath() {
		t.Fatalf("link -> %q (%v), want %s", target, err, selfPath())
	}
	if !strings.Contains(out.String(), `export PATH="`+dir+`:$PATH"`) {
		t.Errorf("no PATH advice: %s", out.String())
	}
	// Idempotent.
	out.Reset()
	if code := run([]string{"link", "--dir", dir}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Already linked") {
		t.Errorf("second link: %d %s", code, out.String())
	}
	// Never replaces a file it did not create.
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"link", "--dir", dir}, nil, &out, &errOut); code == 0 {
		t.Error("replaced a regular file")
	}
	if b, _ := os.ReadFile(dst); string(b) != "mine" {
		t.Error("the user's file was changed")
	}
	// A symlink elsewhere needs --force.
	_ = os.Remove(dst)
	_ = os.Symlink("/bin/true", dst)
	if code := run([]string{"link", "--dir", dir}, nil, &out, &errOut); code == 0 {
		t.Error("replaced a foreign symlink without --force")
	}
	if code := run([]string{"link", "--dir", dir, "--force"}, nil, &out, &errOut); code != 0 {
		t.Errorf("--force: %s", errOut.String())
	}
}

// The plugin's stable name (bin/agentwatch -> versioned asset) is what the
// link points to, so it survives plugin updates.
func TestLinkPrefersTheStableName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	self := selfPath()
	stable := filepath.Join(filepath.Dir(self), "agentwatch")
	if _, err := os.Lstat(stable); err == nil {
		t.Skip("a file named agentwatch already sits next to the test binary")
	}
	if err := os.Symlink(filepath.Base(self), stable); err != nil {
		t.Skip("cannot write next to the test binary:", err)
	}
	defer os.Remove(stable)
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"link", "--dir", dir}, nil, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if target, _ := os.Readlink(filepath.Join(dir, "agentwatch")); target != stable {
		t.Errorf("link -> %s, want the stable %s", target, stable)
	}
}

func TestStatusNeverPrintsEmptyModeSource(t *testing.T) {
	h := t.TempDir()
	t.Setenv("AGENTWATCH_HOME", filepath.Join(h, ".aw"))
	t.Setenv("AGENTWATCH_USER_HOME", h)
	t.Setenv("AGENTWATCH_NO_KEYCHAIN", "1")
	for _, k := range []string{"AGENTWATCH_API_KEY", "AGENTWATCH_MODE", "CLAUDE_PLUGIN_OPTION_MODE", "CLAUDE_PLUGIN_OPTION_API_KEY"} {
		t.Setenv(k, "")
	}
	var out bytes.Buffer
	run([]string{"status"}, nil, &out, &out)
	if strings.Contains(out.String(), "()") || !strings.Contains(out.String(), "dry_run (default)") {
		t.Errorf("status:\n%s", out.String())
	}
	out.Reset()
	run([]string{"mode"}, nil, &out, &out)
	if strings.Contains(out.String(), "()") {
		t.Errorf("mode: %s", out.String())
	}
}
