package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestModeSourceIsNeverEmpty(t *testing.T) {
	sandbox(t)
	c := Load()
	for _, k := range []string{"mode", "fail_mode", "api_url"} {
		if c.Sources[k] != "default" {
			t.Errorf("%s source = %q, want default", k, c.Sources[k])
		}
	}
}

// The plugin's mode option is free text on Claude Code versions without
// option lists: case and spacing must not silently fall back to dry_run.
func TestModeOptionIsTolerant(t *testing.T) {
	for in, want := range map[string]string{
		"enforce": ModeEnforce, " Enforce ": ModeEnforce, "ENFORCE": ModeEnforce,
		"dry_run": ModeDryRun, "dry-run": ModeDryRun, "Dry_Run": ModeDryRun, "nonsense": ModeDryRun,
	} {
		sandbox(t)
		t.Setenv("CLAUDE_PLUGIN_OPTION_MODE", in)
		c := Load()
		if c.Mode != want {
			t.Errorf("%q -> %s, want %s", in, c.Mode, want)
		}
		if in == "nonsense" && !strings.Contains(c.Sources["mode"], `unknown value "nonsense"`) {
			t.Errorf("source: %s", c.Sources["mode"])
		}
	}
}

// The sign-in hint must name a command that works here: a bare
// `agentwatch` only when that is this binary on PATH.
func TestSignInHintNamesAWorkingCommand(t *testing.T) {
	sandbox(t)
	t.Setenv("PATH", t.TempDir()) // agentwatch is not on PATH
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	h := SignInHint()
	if !strings.Contains(h, exe) || !strings.HasSuffix(h, " login") {
		t.Errorf("hint %q does not name %s", h, exe)
	}
	if strings.Contains(h, "/agentwatch:setup") {
		t.Error("outside the plugin, /agentwatch:setup means nothing")
	}
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/plugins/agentwatch")
	if h := SignInHint(); !strings.Contains(h, "/agentwatch:setup") || !strings.Contains(h, exe) {
		t.Errorf("plugin hint: %q", h)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// On PATH under its own name: the bare command.
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, "agentwatch")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if c := Command(); c != "agentwatch" {
		t.Errorf("on PATH: %q", c)
	}
	// A different agentwatch on PATH is not this binary.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "agentwatch"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", other)
	if c := Command(); c == "agentwatch" {
		t.Error("another agentwatch on PATH was taken for this one")
	}
}

func TestShellQuote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	for in, want := range map[string]string{
		"/Users/dev/.claude/plugins/data/agentwatch-agentwatch/bin/agentwatch": "/Users/dev/.claude/plugins/data/agentwatch-agentwatch/bin/agentwatch",
		"/Users/a b/bin/agentwatch": "'/Users/a b/bin/agentwatch'",
		"/tmp/it's":                 `'/tmp/it'\''s'`,
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}
