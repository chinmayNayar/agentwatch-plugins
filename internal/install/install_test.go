package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("AGENTWATCH_USER_HOME", h)
	t.Setenv("AGENTWATCH_HOME", filepath.Join(h, ".agentwatch"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("COPILOT_HOME", "")
	return h
}

func exe(h string) string { return filepath.Join(h, ".agentwatch", "bin", "agentwatch") }

// An existing Claude Code settings file with the user's own hooks and keys.
const existingClaude = `{
  "model": "opus",
  "permissions": {
    "allow": ["Bash(npm test)"]
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "/usr/local/bin/my-guard.sh"}
        ]
      }
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "say done"}]}
    ]
  },
  "statusLine": {"type": "command", "command": "echo hi"}
}
`

var allClients = []string{"claude-code", "cursor", "codex", "copilot"}

func TestMergeKeepsTheUsersHooksAndKeyOrder(t *testing.T) {
	h := home(t)
	p := filepath.Join(h, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(existingClaude), 0o600)
	if _, err := Install(Options{Client: "claude-code", Exe: exe(h), Mode: "dry_run", FailMode: "fail_open"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, want := range []string{"/usr/local/bin/my-guard.sh", "say done", `"model": "opus"`, `"statusLine"`, "Bash(npm test)"} {
		if !strings.Contains(s, want) {
			t.Fatalf("lost %q:\n%s", want, s)
		}
	}
	// Key order: model before permissions before hooks before statusLine.
	if !(strings.Index(s, `"model"`) < strings.Index(s, `"permissions"`) &&
		strings.Index(s, `"permissions"`) < strings.Index(s, `"hooks"`) &&
		strings.Index(s, `"hooks"`) < strings.Index(s, `"statusLine"`)) {
		t.Fatalf("key order changed:\n%s", s)
	}
	doc, _ := Parse(b)
	if _, ok := findOurs(doc, "claude-code"); !ok {
		t.Fatal("our hooks missing")
	}
	// Permissions stay 0600 if they were.
	if info, _ := os.Stat(p); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 { // no Unix modes on Windows
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	if !strings.Contains(s, `"timeout": 5`) || strings.Contains(s, `"timeout": 600`) {
		t.Fatalf("PreToolUse must have a short timeout:\n%s", s)
	}
}

func TestReinstallIsIdempotent(t *testing.T) {
	for _, c := range allClients {
		t.Run(c, func(t *testing.T) {
			h := home(t)
			o := Options{Client: c, Exe: exe(h), Mode: "dry_run", FailMode: "fail_open"}
			p1, err := Install(o)
			if err != nil {
				t.Fatal(err)
			}
			first, _ := os.ReadFile(p1.Path)
			p2, err := Install(o)
			if err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(p2.Path)
			if !bytes.Equal(first, second) {
				t.Fatalf("second install changed the file:\n%s\n---\n%s", first, second)
			}
			if p2.Changed {
				t.Fatal("plan reported a change on re-install")
			}
			// Exactly one handler of ours per event.
			doc, _ := Parse(second)
			hooks := doc.Vals["hooks"].(*Obj)
			for _, ev := range hooks.Keys {
				n := 0
				for _, item := range hooks.Vals[ev].([]any) {
					o := item.(*Obj)
					if inner, ok := o.Vals["hooks"].([]any); ok {
						for _, x := range inner {
							if _, ours := isOurs(x.(*Obj), c); ours {
								n++
							}
						}
					} else if _, ours := isOurs(o, c); ours {
						n++
					}
				}
				if n != 1 {
					t.Fatalf("%s has %d AgentWatch handlers", ev, n)
				}
			}
		})
	}
}

func TestUninstallRestoresTheBackupByteForByte(t *testing.T) {
	for _, c := range allClients {
		t.Run(c, func(t *testing.T) {
			h := home(t)
			p, _ := HookFile(c, ScopeUser, "")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			// Odd but valid formatting the user chose: tabs, no trailing newline.
			original := []byte("{\n\t\"version\": 1,\n\t\"hooks\": {}\n,\t\"x\": [1,2]}")
			if c == "claude-code" || c == "codex" {
				original = []byte(existingClaude[:len(existingClaude)-1]) // no trailing newline
			}
			_ = os.WriteFile(p, original, 0o640)
			if _, err := Install(Options{Client: c, Exe: exe(h)}); err != nil {
				t.Fatal(err)
			}
			if _, err := Install(Options{Client: c, Exe: exe(h)}); err != nil { // twice: backup taken once
				t.Fatal(err)
			}
			r, err := Uninstall(c, ScopeUser, "")
			if err != nil {
				t.Fatal(err)
			}
			if r.Action != "restored_backup" {
				t.Fatalf("action %s", r.Action)
			}
			got, _ := os.ReadFile(p)
			if !bytes.Equal(got, original) {
				t.Fatalf("not byte-for-byte:\n%q\n%q", original, got)
			}
			if _, err := os.Stat(p + ".agentwatch-backup"); !os.IsNotExist(err) {
				t.Fatal("backup left behind")
			}
		})
	}
}

func TestUninstallRemovesAFileItCreated(t *testing.T) {
	for _, c := range allClients {
		h := home(t)
		pl, err := Install(Options{Client: c, Exe: exe(h)})
		if err != nil {
			t.Fatal(err)
		}
		r, _ := Uninstall(c, ScopeUser, "")
		if r.Action != "removed_file" {
			t.Fatalf("%s: %s", c, r.Action)
		}
		if _, err := os.Stat(pl.Path); !os.IsNotExist(err) {
			t.Fatalf("%s: file still there", c)
		}
	}
}

func TestUninstallAfterTheUserEditedKeepsTheirChanges(t *testing.T) {
	h := home(t)
	p := filepath.Join(h, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(existingClaude), 0o600)
	_, _ = Install(Options{Client: "claude-code", Exe: exe(h)})
	b, _ := os.ReadFile(p)
	doc, _ := Parse(b)
	doc.Set("theme", "dark") // the user changes something after install
	_ = os.WriteFile(p, doc.Marshal(), 0o600)
	r, err := Uninstall("claude-code", ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != "removed_entries" {
		t.Fatalf("action %s", r.Action)
	}
	after, _ := os.ReadFile(p)
	s := string(after)
	if strings.Contains(s, "agentwatch") {
		t.Fatalf("our entries remain:\n%s", s)
	}
	for _, want := range []string{`"theme": "dark"`, "my-guard.sh", "say done"} {
		if !strings.Contains(s, want) {
			t.Fatalf("lost %q", want)
		}
	}
}

func TestInvalidJSONIsNeverOverwritten(t *testing.T) {
	h := home(t)
	p := filepath.Join(h, ".cursor", "hooks.json")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	bad := []byte(`{"version": 1, "hooks": {`)
	_ = os.WriteFile(p, bad, 0o644)
	if _, err := Install(Options{Client: "cursor", Exe: exe(h)}); err == nil {
		t.Fatal("installed over invalid JSON")
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, bad) {
		t.Fatal("invalid file was modified")
	}
}

func TestWindowsPathsWithSpaces(t *testing.T) {
	h := home(t)
	winExe := `C:\Users\Ada Lovelace\.agentwatch\bin\agentwatch.exe`
	for _, c := range allClients {
		p, err := PlanInstall(Options{Client: c, Exe: winExe, GOOS: "windows"})
		if err != nil {
			t.Fatal(err)
		}
		switch c {
		case "claude-code":
			// Exec form: the path is one argument, no quoting needed.
			if !strings.Contains(p.After, `"command": "C:\\Users\\Ada Lovelace\\.agentwatch\\bin\\agentwatch.exe"`) ||
				!strings.Contains(p.After, `"args": [`) {
				t.Fatalf("claude exec form wrong:\n%s", p.After)
			}
		case "cursor", "codex":
			want := `powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File \"C:\\Users\\Ada Lovelace\\.agentwatch\\bin\\aw.ps1\" hook ` + c
			if !strings.Contains(p.After, want) {
				t.Fatalf("%s powershell command wrong:\n%s", c, p.After)
			}
		case "copilot":
			if !strings.Contains(p.After, `& 'C:\\Users\\Ada Lovelace\\.agentwatch\\bin\\aw.ps1' hook copilot preToolUse`) {
				t.Fatalf("copilot powershell wrong:\n%s", p.After)
			}
		}
		// Every rendered file is valid JSON.
		var v any
		if err := json.Unmarshal([]byte(p.After), &v); err != nil {
			t.Fatalf("%s: invalid JSON: %v", c, err)
		}
	}
	_ = h
}

func TestUnixPathsWithSpacesAndQuotes(t *testing.T) {
	home(t)
	weird := "/Users/o'brien/my tools/agentwatch"
	p, err := PlanInstall(Options{Client: "cursor", Exe: weird, GOOS: "darwin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.After, `'/Users/o'\\''brien/my tools/agentwatch' hook cursor`) {
		t.Fatalf("shell quoting wrong:\n%s", p.After)
	}
}

func TestPowerShellShim(t *testing.T) {
	for _, want := range []string{"[Console]::In.ReadToEnd()", "UTF8Encoding", "& $exe @args", "exit 0", "agentwatch.exe"} {
		if !strings.Contains(AwPS1, want) {
			t.Fatalf("aw.ps1 missing %q", want)
		}
	}
	h := home(t)
	if _, err := Install(Options{Client: "cursor", Exe: exe(h)}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"aw.ps1", "aw.sh"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe(h)), f)); err != nil {
			t.Fatalf("%s not written: %v", f, err)
		}
	}
}

func TestEnforceFailClosedSetsBlockingAndShortTimeouts(t *testing.T) {
	home(t)
	cc, _ := PlanInstall(Options{Client: "claude-code", Exe: "/x/agentwatch", Mode: "enforce", FailMode: "fail_closed"})
	if !strings.Contains(cc.After, `"onFailure": "block"`) {
		t.Fatalf("claude: no onFailure block:\n%s", cc.After)
	}
	cu, _ := PlanInstall(Options{Client: "cursor", Exe: "/x/agentwatch", Mode: "enforce", FailMode: "fail_closed"})
	if !strings.Contains(cu.After, `"failClosed": true`) {
		t.Fatalf("cursor: no failClosed:\n%s", cu.After)
	}
	dr, _ := PlanInstall(Options{Client: "claude-code", Exe: "/x/agentwatch", Mode: "dry_run", FailMode: "fail_closed"})
	if strings.Contains(dr.After, "onFailure") {
		t.Fatal("dry_run must never block on a hook failure")
	}
	for _, c := range allClients {
		p, _ := PlanInstall(Options{Client: c, Exe: "/x/agentwatch"})
		if strings.Contains(p.After, `"timeout": 600`) || (!strings.Contains(p.After, `"timeout": 5`) && !strings.Contains(p.After, `"timeoutSec": 5`)) {
			t.Fatalf("%s: timeouts:\n%s", c, p.After)
		}
	}
}

func TestProjectScopeWarnsAndUsesProjectFiles(t *testing.T) {
	home(t)
	proj := t.TempDir()
	for _, c := range allClients {
		p, err := PlanInstall(Options{Client: c, Scope: ScopeProject, ProjectDir: proj, Exe: "/x/agentwatch"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.Path, proj) || p.Warning == "" {
			t.Fatalf("%s: %s %q", c, p.Path, p.Warning)
		}
	}
}

func TestCheckSeesRemovedHooks(t *testing.T) {
	h := home(t)
	_ = os.MkdirAll(filepath.Dir(exe(h)), 0o755)
	_ = os.WriteFile(exe(h), []byte("bin"), 0o755)
	pl, _ := Install(Options{Client: "cursor", Exe: exe(h)})
	if st := Check("cursor"); !st.HooksOK {
		t.Fatalf("fresh install not OK: %+v", st)
	}
	_ = os.WriteFile(pl.Path, []byte(`{"version":1,"hooks":{}}`), 0o644) // someone removed them
	st := Check("cursor")
	if st.HooksOK || !st.Installed || st.Problem == "" {
		t.Fatalf("removal not detected: %+v", st)
	}
}
