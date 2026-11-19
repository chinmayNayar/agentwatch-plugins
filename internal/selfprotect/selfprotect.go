// Package selfprotect stops an agent from editing the files that keep it
// watched: hook configs, the sensor's own state, and the plugin installs.
//
// It is a tripwire for actions that go through the client's hooks, not a
// sandbox. A command that builds a path at runtime (variables, base64, an
// interpreter one-liner) can get past it. LIMITS.md says so.
package selfprotect

import (
	"github.com/chinmayNayar/agentwatch-plugins/internal/lazyre"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

// protected globs, relative to the user's home ("~/") or to any directory
// (no prefix: matched as a path suffix, for project-scope files).
var protected = []string{
	"~/.claude/settings.json", "~/.claude/settings.local.json",
	".claude/settings.json", ".claude/settings.local.json",
	"~/.cursor/hooks.json", ".cursor/hooks.json",
	"~/.codex/hooks.json", ".codex/hooks.json", "~/.codex/config.toml",
	"~/.copilot/hooks/", ".github/hooks/", "~/.copilot/settings.json",
	"~/.agentwatch/",
	"~/.claude/plugins/", "~/.cursor/plugins/",
	"~/.vscode/extensions/agentwatch.", "~/.cursor/extensions/agentwatch.", "~/.vscode-oss/extensions/agentwatch.",
	"~/.windsurf/extensions/agentwatch.",
}

// Shell commands that change a file, and the sensor's own off switches.
var (
	writeVerbs = lazyre.New(`(?i)(>>?|\btee\b|\bsed\s+(-[a-z]*i|--in-place)|\brm\b|\bmv\b|\bcp\b|\btruncate\b|\bchmod\b|\bchown\b|\bln\b|\bunlink\b|\bdd\b|\bperl\s+-[a-z]*i|\bpython3?\s+-c|\bnode\s+-e|Set-Content|Add-Content|Out-File|Remove-Item|Move-Item|Copy-Item|\bdel\b|\berase\b|\bjq\b.*>|\bsponge\b|\bpatch\b|\bgit\s+(checkout|restore|apply)\b)`)
	offSwitch  = lazyre.New(`(?i)\bagentwatch(\.exe)?["']?\s+(mode|uninstall|logout|login|install)\b`)
	pluginOff  = lazyre.New(`(?i)\bclaude\s+plugin\s+(uninstall|disable|remove)\b[^\n]*\bagentwatch|\bclaude\s+plugin\s+marketplace\s+remove\b[^\n]*\bagentwatch|disableAllHooks`)
	extOff     = lazyre.New(`(?i)\b(code|cursor|codium|windsurf)\s+--(uninstall|disable)-extension\s+agentwatch`)
)

// Check returns a reason when the event touches a protected file, or "".
func Check(ev *event.Event, home string) string {
	if ev.Kind != event.PreTool {
		return ""
	}
	switch ev.Class {
	case event.ClassWrite:
		paths := []string{ev.Path}
		if ev.ToolName == "apply_patch" {
			paths = append(paths, common.PatchPaths(ev.Command)...)
		}
		for _, p := range paths {
			if hit := match(p, ev.Cwd, home); hit != "" {
				return "AgentWatch self-protection: agents may not modify " + hit +
					". Change AgentWatch settings yourself with the agentwatch CLI or the editor extension."
			}
		}
	case event.ClassShell, event.ClassNetwork, event.ClassMCP:
		cmd := ev.Command
		if cmd == "" {
			return ""
		}
		if offSwitch.MatchString(cmd) || pluginOff.MatchString(cmd) || extOff.MatchString(cmd) {
			return "AgentWatch self-protection: agents may not change or remove AgentWatch. Run that command yourself."
		}
		if writeVerbs.MatchString(cmd) {
			for _, tok := range candidates(cmd) {
				if hit := match(tok, ev.Cwd, home); hit != "" {
					return "AgentWatch self-protection: this command would modify " + hit + "."
				}
			}
		}
	}
	return ""
}

var tokenSplit = lazyre.New(`[\s;&|()<>]+`)

func candidates(cmd string) []string {
	var out []string
	for _, t := range tokenSplit.Split(cmd, -1) {
		t = strings.Trim(t, `"'`+"`")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// match normalises p (~, $HOME, relative to cwd, backslashes) and returns the
// protected pattern it falls under.
func match(p, cwd, home string) string {
	if p == "" {
		return ""
	}
	n := normalise(p, cwd, home)
	h := normalise(home, "", home)
	for _, pat := range protected {
		if strings.HasPrefix(pat, "~/") {
			full := h + "/" + strings.TrimPrefix(pat, "~/")
			if hasPathPrefix(n, full) {
				return pat
			}
			continue
		}
		if strings.HasSuffix(pat, "/") {
			if strings.Contains(n+"/", "/"+pat) {
				return pat
			}
			continue
		}
		if strings.HasSuffix(n, "/"+pat) || n == pat {
			return pat
		}
	}
	return ""
}

func hasPathPrefix(p, prefix string) bool {
	if strings.HasSuffix(prefix, "/") || strings.HasSuffix(prefix, ".") {
		return strings.HasPrefix(p, prefix) || p+"/" == prefix
	}
	return p == prefix
}

func normalise(p, cwd, home string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	for _, v := range []string{"$HOME", "${HOME}", "%USERPROFILE%", "$env:USERPROFILE"} {
		if strings.HasPrefix(p, v) {
			p = home + p[len(v):]
		}
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = home + p[1:]
	}
	p = strings.ReplaceAll(p, `\`, "/")
	p = msysToWin(p)
	// A leading "/" is absolute on every OS. filepath.IsAbs is false for
	// "/Users/dev/..." on Windows (no drive letter), which used to join such
	// paths, and $HOME-expanded ones, onto the cwd so they never matched.
	if !strings.HasPrefix(p, "/") && !isWinAbs(p) && cwd != "" {
		p = msysToWin(strings.ReplaceAll(cwd, `\`, "/")) + "/" + p
	}
	p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if runtime.GOOS == "windows" || isWinAbs(p) {
		p = strings.ToLower(p)
	}
	return p
}

// msysToWin turns a Git Bash / MSYS path ("/c/Users/dev/x") into "c:/Users/dev/x".
func msysToWin(p string) string {
	if len(p) >= 3 && p[0] == '/' && p[2] == '/' && ((p[1] >= 'a' && p[1] <= 'z') || (p[1] >= 'A' && p[1] <= 'Z')) {
		return string(p[1]) + ":" + p[2:]
	}
	if len(p) == 2 && p[0] == '/' && ((p[1] >= 'a' && p[1] <= 'z') || (p[1] >= 'A' && p[1] <= 'Z')) {
		return string(p[1]) + ":/"
	}
	return p
}

func isWinAbs(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}
