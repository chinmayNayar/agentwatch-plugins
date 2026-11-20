package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// InPlugin reports whether this process was started by a Claude Code plugin
// (hooks and the MCP server get CLAUDE_PLUGIN_ROOT / CLAUDE_PLUGIN_DATA, and
// the binary lives in the plugin's data directory).
func InPlugin() bool {
	if os.Getenv("CLAUDE_PLUGIN_ROOT") != "" || os.Getenv("CLAUDE_PLUGIN_DATA") != "" {
		return true
	}
	exe, err := os.Executable()
	return err == nil && strings.Contains(filepath.ToSlash(exe), "/.claude/plugins/data/")
}

// Command is how a person runs this binary from a terminal on this machine:
// "agentwatch" only when that name on PATH is this binary; otherwise the
// binary's full path, quoted for the shell. The plugin's stable link
// (<data>/bin/agentwatch, which follows plugin updates) is preferred over the
// versioned file it points to.
func Command() string {
	exe, err := os.Executable()
	if err != nil {
		return "agentwatch"
	}
	real := exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		real = r
	}
	if p, err := exec.LookPath("agentwatch"); err == nil {
		if r, err := filepath.EvalSymlinks(p); err == nil && sameFile(r, real) {
			return "agentwatch"
		}
	}
	show := real
	name := "agentwatch"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	stable := filepath.Join(filepath.Dir(real), name)
	if r, err := filepath.EvalSymlinks(stable); err == nil && sameFile(r, real) {
		show = stable
	}
	return ShellQuote(show)
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// ShellQuote quotes a path for the user's shell (PowerShell's call operator
// on Windows).
func ShellQuote(p string) string {
	if runtime.GOOS == "windows" {
		return `& "` + p + `"`
	}
	safe := true
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@", r)) {
			safe = false
			break
		}
	}
	if safe && p != "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// SignInHint says how to sign this machine in, with a command that works
// here (never a bare `agentwatch` that is not on PATH).
func SignInHint() string {
	term := Command() + " login"
	if InPlugin() {
		return "run /agentwatch:setup in Claude Code (it opens the approval page and waits), or in a terminal: " + term
	}
	return "run in a terminal: " + term
}
