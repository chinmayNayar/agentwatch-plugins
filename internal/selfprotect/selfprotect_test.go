package selfprotect

import (
	"testing"

	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

const home = "/Users/dev"

func pre(class, tool, cmd, path string) *event.Event {
	return &event.Event{Kind: event.PreTool, Class: class, ToolName: tool, Command: cmd, Path: path, Cwd: "/Users/dev/project"}
}

func TestBlocksWritesToProtectedFiles(t *testing.T) {
	cases := []*event.Event{
		pre(event.ClassWrite, "Edit", "", "/Users/dev/.claude/settings.json"),
		pre(event.ClassWrite, "Write", "", "~/.cursor/hooks.json"),
		pre(event.ClassWrite, "Write", "", ".cursor/hooks.json"),
		pre(event.ClassWrite, "Write", "", "/Users/dev/.agentwatch/config.json"),
		pre(event.ClassWrite, "Edit", "", "/Users/dev/.codex/hooks.json"),
		pre(event.ClassWrite, "create", "", "/Users/dev/project/.github/hooks/agentwatch.json"),
		pre(event.ClassWrite, "Write", "", "/Users/dev/.claude/plugins/cache/agentwatch/agentwatch/0.1.0/hooks/hooks.json"),
		pre(event.ClassWrite, "apply_patch", "*** Begin Patch\n*** Update File: /Users/dev/.codex/hooks.json\n*** End Patch", ""),
		pre(event.ClassShell, "Bash", "echo '{}' > ~/.claude/settings.json", ""),
		pre(event.ClassShell, "Bash", "rm -rf $HOME/.agentwatch", ""),
		pre(event.ClassShell, "Bash", "sed -i '' 's/hook//' .cursor/hooks.json", ""),
		pre(event.ClassShell, "Bash", "agentwatch mode dry_run --yes", ""),
		pre(event.ClassShell, "Bash", "~/.agentwatch/bin/agentwatch uninstall all", ""),
		pre(event.ClassShell, "Bash", "claude plugin uninstall agentwatch@agentwatch", ""),
		pre(event.ClassShell, "Shell", "code --uninstall-extension agentwatch.agentwatch", ""),
		pre(event.ClassShell, "Bash", `echo '{"disableAllHooks": true}' > x.json`, ""),
		pre(event.ClassShell, "powershell", `Remove-Item C:\Users\dev\.agentwatch -Recurse`, ""),
	}
	for _, ev := range cases {
		h := home
		if ev.ToolName == "powershell" {
			h = `C:\Users\dev`
			ev.Cwd = `C:\Users\dev\project`
		}
		if Check(ev, h) == "" {
			t.Errorf("not blocked: %s %q %q", ev.ToolName, ev.Command, ev.Path)
		}
	}
}

func TestAllowsOrdinaryWork(t *testing.T) {
	cases := []*event.Event{
		pre(event.ClassWrite, "Edit", "", "/Users/dev/project/main.go"),
		pre(event.ClassWrite, "Write", "", "README.md"),
		pre(event.ClassShell, "Bash", "cat ~/.claude/settings.json", ""), // reading is fine
		pre(event.ClassShell, "Bash", "go test ./...", ""),
		pre(event.ClassShell, "Bash", "echo hi > out.txt", ""),
		pre(event.ClassShell, "Bash", "agentwatch status", ""),
		pre(event.ClassShell, "Bash", "agentwatch doctor", ""),
		pre(event.ClassRead, "Read", "", "/Users/dev/.claude/settings.json"),
	}
	for _, ev := range cases {
		if r := Check(ev, home); r != "" {
			t.Errorf("blocked ordinary work %s %q %q: %s", ev.ToolName, ev.Command, ev.Path, r)
		}
	}
}

func TestOnlyPreToolEventsAreChecked(t *testing.T) {
	ev := pre(event.ClassWrite, "Edit", "", "/Users/dev/.claude/settings.json")
	ev.Kind = event.PostTool
	if Check(ev, home) != "" {
		t.Fatal("post events cannot be blocked and must not be checked")
	}
}
