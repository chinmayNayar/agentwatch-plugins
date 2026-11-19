// Package install writes and removes the sensor's hooks in each client's
// hook file. Rules (each has a test):
//
//   - user scope by default; project scope only when asked (those files are
//     usually committed);
//   - merge, never replace: other hooks and settings stay as they were, in
//     the same order;
//   - idempotent: re-installing replaces only our own entries;
//   - the first install keeps a byte-for-byte backup, and uninstall restores
//     it when the file has not changed since; otherwise uninstall removes only
//     our entries;
//   - the hook command points at ~/.agentwatch/bin/agentwatch, never at an
//     editor extension's folder that an update would delete.
package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/state"
)

const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

// Options for one install.
type Options struct {
	Client     string
	Scope      string
	ProjectDir string
	Exe        string // the binary hooks will run
	Mode       string
	FailMode   string
	GOOS       string // tests: render for another platform
}

func (o *Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

// Plan is what an install would change.
type Plan struct {
	Client  string `json:"client"`
	Scope   string `json:"scope"`
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Changed bool   `json:"changed"`
	Warning string `json:"warning,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Record is kept per install for uninstall and status.
type Record struct {
	Client      string `json:"client"`
	Scope       string `json:"scope"`
	Path        string `json:"path"`
	Existed     bool   `json:"existed"`
	BackupPath  string `json:"backup_path,omitempty"`
	AfterSHA256 string `json:"after_sha256"`
	Exe         string `json:"exe"`
}

// HookFile is where a client keeps its hooks.
func HookFile(clientName, scope, projectDir string) (string, error) {
	home := config.UserHome()
	if scope == ScopeProject {
		if projectDir == "" {
			return "", errors.New("--project needs a project directory")
		}
		switch clientName {
		case "claude-code":
			return filepath.Join(projectDir, ".claude", "settings.json"), nil
		case "cursor":
			return filepath.Join(projectDir, ".cursor", "hooks.json"), nil
		case "codex":
			return filepath.Join(projectDir, ".codex", "hooks.json"), nil
		case "copilot":
			return filepath.Join(projectDir, ".github", "hooks", "agentwatch.json"), nil
		}
	} else {
		switch clientName {
		case "claude-code":
			return filepath.Join(home, ".claude", "settings.json"), nil
		case "cursor":
			return filepath.Join(home, ".cursor", "hooks.json"), nil
		case "codex":
			if h := os.Getenv("CODEX_HOME"); h != "" {
				return filepath.Join(h, "hooks.json"), nil
			}
			return filepath.Join(home, ".codex", "hooks.json"), nil
		case "copilot":
			if h := os.Getenv("COPILOT_HOME"); h != "" {
				return filepath.Join(h, "hooks", "agentwatch.json"), nil
			}
			return filepath.Join(home, ".copilot", "hooks", "agentwatch.json"), nil
		}
	}
	return "", fmt.Errorf("unknown client %q", clientName)
}

// PlanInstall computes the change without writing anything.
func PlanInstall(o Options) (*Plan, error) {
	if o.Scope == "" {
		o.Scope = ScopeUser
	}
	path, err := HookFile(o.Client, o.Scope, o.ProjectDir)
	if err != nil {
		return nil, err
	}
	before, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := Parse(before)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%v); fix or move it first, nothing was changed", path, err)
	}
	if err := apply(doc, o); err != nil {
		return nil, err
	}
	after := doc.Marshal()
	p := &Plan{Client: o.Client, Scope: o.Scope, Path: path, Exists: exists, Before: string(before),
		After: string(after), Changed: !bytes.Equal(before, after)}
	if o.Scope == ScopeProject {
		p.Warning = "Project scope writes " + path + ", which is usually committed: everyone who clones the repository " +
			"would run these hooks, with their own AgentWatch credentials or none."
	}
	if o.Client == "codex" {
		p.Note = "Codex runs new or changed hooks only after you review and trust them (/hooks in Codex)."
	}
	return p, nil
}

// Install writes the plan, keeping a one-time backup of the original.
func Install(o Options) (*Plan, error) {
	if o.Scope == "" {
		o.Scope = ScopeUser
	}
	if o.Exe == "" {
		return nil, errors.New("no binary path")
	}
	if o.goos() == runtime.GOOS {
		if err := writeShims(filepath.Dir(o.Exe)); err != nil {
			return nil, err
		}
	}
	p, err := PlanInstall(o)
	if err != nil {
		return nil, err
	}
	rec := loadRecord(o.Client, o.Scope, p.Path)
	if rec == nil {
		rec = &Record{Client: o.Client, Scope: o.Scope, Path: p.Path, Existed: p.Exists}
		if p.Exists {
			rec.BackupPath = p.Path + ".agentwatch-backup"
			if _, err := os.Stat(rec.BackupPath); errors.Is(err, os.ErrNotExist) {
				if err := copyFile(p.Path, rec.BackupPath); err != nil {
					return nil, fmt.Errorf("backup: %w", err)
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return nil, err
	}
	perm := os.FileMode(0o644)
	if info, err := os.Stat(p.Path); err == nil {
		perm = info.Mode().Perm()
	}
	if err := config.WriteFileAtomic(p.Path, []byte(p.After), perm); err != nil {
		return nil, err
	}
	rec.AfterSHA256 = sum([]byte(p.After))
	rec.Exe = o.Exe
	if err := saveRecord(rec); err != nil {
		return nil, err
	}
	if o.Scope == ScopeUser {
		state.SetInstalled(o.Client, true)
	}
	return p, nil
}

// UninstallResult says what happened.
type UninstallResult struct {
	Path   string `json:"path"`
	Action string `json:"action"` // restored_backup | removed_file | removed_entries | nothing
	Detail string `json:"detail,omitempty"`
}

// Uninstall restores the backup byte-for-byte when the file is exactly as we
// left it; otherwise it removes only our entries.
func Uninstall(clientName, scope, projectDir string) (*UninstallResult, error) {
	if scope == "" {
		scope = ScopeUser
	}
	path, err := HookFile(clientName, scope, projectDir)
	if err != nil {
		return nil, err
	}
	res := &UninstallResult{Path: path, Action: "nothing"}
	rec := loadRecord(clientName, scope, path)
	cur, rerr := os.ReadFile(path)
	if rec != nil && rerr == nil && sum(cur) == rec.AfterSHA256 {
		switch {
		case rec.Existed && rec.BackupPath != "":
			b, err := os.ReadFile(rec.BackupPath)
			if err != nil {
				return nil, fmt.Errorf("backup missing: %w", err)
			}
			info, _ := os.Stat(path)
			perm := os.FileMode(0o644)
			if info != nil {
				perm = info.Mode().Perm()
			}
			if err := config.WriteFileAtomic(path, b, perm); err != nil {
				return nil, err
			}
			_ = os.Remove(rec.BackupPath)
			res.Action = "restored_backup"
		case !rec.Existed:
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			res.Action = "removed_file"
		}
	} else if rerr == nil {
		doc, err := Parse(cur)
		if err != nil {
			return nil, fmt.Errorf("%s is not valid JSON; remove the agentwatch entries by hand", path)
		}
		if removeOurs(doc, clientName) {
			info, _ := os.Stat(path)
			perm := os.FileMode(0o644)
			if info != nil {
				perm = info.Mode().Perm()
			}
			if err := config.WriteFileAtomic(path, doc.Marshal(), perm); err != nil {
				return nil, err
			}
			res.Action = "removed_entries"
			if rec != nil && rec.BackupPath != "" {
				res.Detail = "the file changed after install, so only AgentWatch's entries were removed; the original is still at " + rec.BackupPath
			}
		}
	}
	deleteRecord(clientName, scope, path)
	if scope == ScopeUser {
		state.SetInstalled(clientName, false)
	}
	return res, nil
}

// Status of one client's user-scope install.
type Status struct {
	Client    string `json:"client"`
	Installed bool   `json:"installed"`
	HooksOK   bool   `json:"hooks_ok"`
	Path      string `json:"path"`
	Scope     string `json:"scope"`
	Problem   string `json:"problem,omitempty"`
}

// Check reports whether the user-scope hooks are present and point at an
// existing binary.
func Check(clientName string) Status {
	path, _ := HookFile(clientName, ScopeUser, "")
	st := Status{Client: clientName, Path: path, Scope: ScopeUser}
	rec := loadRecord(clientName, ScopeUser, path)
	st.Installed = rec != nil
	b, err := os.ReadFile(path)
	if err != nil {
		if st.Installed {
			st.Problem = "hook file missing"
		}
		return st
	}
	doc, err := Parse(b)
	if err != nil {
		st.Problem = "hook file is not valid JSON"
		return st
	}
	exe, found := findOurs(doc, clientName)
	if !found {
		if st.Installed {
			st.Problem = "AgentWatch hooks were removed from " + path
		}
		return st
	}
	st.Installed = true
	if exe != "" {
		if _, err := os.Stat(exe); err != nil {
			st.Problem = "hooks point at a missing binary: " + exe
			return st
		}
	}
	st.HooksOK = true
	return st
}

// ---- per-client rendering -----------------------------------------------------

type hookSpec struct {
	event   string
	matcher bool // tool events take a matcher
	async   bool
	timeout int
	gate    bool // a permission event (fail-closed applies)
}

var claudeEvents = []hookSpec{
	{"SessionStart", false, false, 5, false},
	{"UserPromptSubmit", false, true, 10, false},
	{"PreToolUse", true, false, 5, true},
	{"PostToolUse", true, true, 10, false},
	{"PostToolUseFailure", true, true, 10, false},
	{"Stop", false, true, 10, false},
	{"SubagentStop", false, true, 10, false},
	{"PreCompact", false, true, 10, false},
	{"SessionEnd", false, false, 2, false},
}

var codexEvents = []hookSpec{
	{"SessionStart", false, false, 5, false},
	{"UserPromptSubmit", false, true, 10, false},
	{"PreToolUse", true, false, 5, true},
	{"PostToolUse", true, true, 10, false},
	{"Stop", false, true, 10, false},
	{"SessionEnd", false, false, 2, false},
}

var cursorEvents = []hookSpec{
	{"sessionStart", false, false, 5, false},
	{"sessionEnd", false, false, 5, false},
	{"beforeSubmitPrompt", false, false, 5, false},
	{"preToolUse", false, false, 5, true},
	{"beforeShellExecution", false, false, 5, true},
	{"beforeMCPExecution", false, false, 5, true},
	{"beforeReadFile", false, false, 5, true},
	{"postToolUse", false, false, 5, false},
	{"postToolUseFailure", false, false, 5, false},
	{"afterAgentResponse", false, false, 5, false},
	{"subagentStart", false, false, 5, true},
	{"subagentStop", false, false, 5, false},
	{"preCompact", false, false, 5, false},
	{"stop", false, false, 5, false},
}

var copilotEvents = []hookSpec{
	{"sessionStart", false, false, 5, false},
	{"sessionEnd", false, false, 5, false},
	{"userPromptSubmitted", false, false, 5, false},
	{"preToolUse", false, false, 5, true},
	{"postToolUse", false, false, 5, false},
	{"postToolUseFailure", false, false, 5, false},
	{"agentStop", false, false, 5, false},
}

func strictBlock(o Options) bool {
	return o.Mode == config.ModeEnforce && o.FailMode == config.FailClosed
}

func apply(doc *Obj, o Options) error {
	removeOurs(doc, o.Client)
	switch o.Client {
	case "claude-code":
		return applyNested(doc, o, claudeEvents, func(s hookSpec) map[string]any {
			h := map[string]any{"type": "command", "command": o.Exe, "args": []string{"hook", "claude-code"}, "timeout": s.timeout}
			if s.async {
				h["async"] = true
			}
			if s.gate && strictBlock(o) {
				h["onFailure"] = "block" // Claude Code 2.1.295+
			}
			return h
		})
	case "codex":
		return applyNested(doc, o, codexEvents, func(s hookSpec) map[string]any {
			// On Windows shellCmd is already the PowerShell shim command.
			h := map[string]any{"type": "command", "command": shellCmd(o, "codex", ""), "timeout": s.timeout}
			if s.async {
				h["async"] = true
			}
			return h
		})
	case "cursor":
		if _, ok := doc.Get("version"); !ok {
			doc.Set("version", json.Number("1"))
		}
		hooks, err := doc.ObjAt("hooks")
		if err != nil {
			return err
		}
		for _, s := range cursorEvents {
			h := map[string]any{"command": shellCmd(o, "cursor", ""), "timeout": s.timeout}
			if s.gate && strictBlock(o) {
				h["failClosed"] = true
			}
			if err := appendTo(hooks, s.event, FromMap(h, "command", "timeout")); err != nil {
				return err
			}
		}
		return nil
	case "copilot":
		if _, ok := doc.Get("version"); !ok {
			doc.Set("version", json.Number("1"))
		}
		hooks, err := doc.ObjAt("hooks")
		if err != nil {
			return err
		}
		for _, s := range copilotEvents {
			h := map[string]any{"type": "command", "bash": unixCmd(o.Exe, "copilot", s.event),
				"powershell": psInline(o, "copilot", s.event), "timeoutSec": s.timeout}
			if err := appendTo(hooks, s.event, FromMap(h, "type", "bash", "powershell", "timeoutSec")); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown client %q", o.Client)
}

func applyNested(doc *Obj, o Options, specs []hookSpec, handler func(hookSpec) map[string]any) error {
	hooks, err := doc.ObjAt("hooks")
	if err != nil {
		return err
	}
	for _, s := range specs {
		group := NewObj()
		if s.matcher {
			group.Set("matcher", "*")
		}
		group.Set("hooks", []any{FromMap(handler(s), "type", "command", "args", "timeout", "async", "onFailure")})
		if err := appendTo(hooks, s.event, group); err != nil {
			return err
		}
	}
	return nil
}

func appendTo(hooks *Obj, eventName string, item any) error {
	cur, ok := hooks.Get(eventName)
	if !ok || cur == nil {
		hooks.Set(eventName, []any{item})
		return nil
	}
	arr, ok := cur.([]any)
	if !ok {
		return fmt.Errorf("hooks.%s is not a list", eventName)
	}
	hooks.Set(eventName, append(arr, item))
	return nil
}

// isOurs: a handler whose command runs `agentwatch … hook <client>`.
func isOurs(h *Obj, clientName string) (string, bool) {
	var parts []string
	exe := ""
	for _, k := range []string{"command", "bash", "powershell", "commandWindows"} {
		if s, ok := h.Vals[k].(string); ok {
			parts = append(parts, s)
		}
	}
	if args, ok := h.Vals["args"].([]any); ok {
		for _, a := range args {
			if s, ok := a.(string); ok {
				parts = append(parts, s)
			}
		}
		if s, ok := h.Vals["command"].(string); ok {
			exe = s
		}
	}
	joined := strings.ToLower(strings.Join(parts, " "))
	if strings.Contains(joined, "agentwatch") && strings.Contains(joined, "hook") && strings.Contains(joined, clientName) {
		if exe == "" {
			exe = exeFromShell(firstStr(h, "command", "bash"))
		}
		return exe, true
	}
	return "", false
}

func firstStr(h *Obj, keys ...string) string {
	for _, k := range keys {
		if s, ok := h.Vals[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// exeFromShell pulls the quoted binary path out of "'<exe>' hook …".
func exeFromShell(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, "'") {
		if i := strings.Index(cmd[1:], "'"); i >= 0 {
			return cmd[1 : 1+i]
		}
	}
	if strings.HasPrefix(cmd, `"`) {
		if i := strings.Index(cmd[1:], `"`); i >= 0 {
			return cmd[1 : 1+i]
		}
	}
	return ""
}

// removeOurs strips our handlers (both shapes) and drops groups and events
// left empty by that. Reports whether anything changed.
func removeOurs(doc *Obj, clientName string) bool {
	hv, ok := doc.Get("hooks")
	if !ok {
		return false
	}
	hooks, ok := hv.(*Obj)
	if !ok {
		return false
	}
	changed := false
	for _, ev := range append([]string{}, hooks.Keys...) {
		arr, ok := hooks.Vals[ev].([]any)
		if !ok {
			continue
		}
		var keep []any
		for _, item := range arr {
			o, ok := item.(*Obj)
			if !ok {
				keep = append(keep, item)
				continue
			}
			if inner, ok := o.Vals["hooks"].([]any); ok { // nested matcher group
				var ik []any
				for _, h := range inner {
					if ho, ok := h.(*Obj); ok {
						if _, ours := isOurs(ho, clientName); ours {
							changed = true
							continue
						}
					}
					ik = append(ik, h)
				}
				if len(ik) == 0 && len(inner) > 0 {
					changed = true
					continue
				}
				if len(ik) != len(inner) {
					o.Set("hooks", ik)
				}
				keep = append(keep, o)
				continue
			}
			if _, ours := isOurs(o, clientName); ours {
				changed = true
				continue
			}
			keep = append(keep, item)
		}
		if len(keep) == 0 {
			hooks.Delete(ev)
		} else {
			hooks.Set(ev, keep)
		}
	}
	return changed
}

func findOurs(doc *Obj, clientName string) (string, bool) {
	hv, ok := doc.Get("hooks")
	if !ok {
		return "", false
	}
	hooks, ok := hv.(*Obj)
	if !ok {
		return "", false
	}
	for _, ev := range hooks.Keys {
		arr, _ := hooks.Vals[ev].([]any)
		for _, item := range arr {
			o, ok := item.(*Obj)
			if !ok {
				continue
			}
			if inner, ok := o.Vals["hooks"].([]any); ok {
				for _, h := range inner {
					if ho, ok := h.(*Obj); ok {
						if exe, ours := isOurs(ho, clientName); ours {
							return exe, true
						}
					}
				}
				continue
			}
			if exe, ours := isOurs(o, clientName); ours {
				return exe, true
			}
		}
	}
	return "", false
}

// ---- commands -------------------------------------------------------------------

func shellCmd(o Options, clientName, ev string) string {
	if o.goos() == "windows" {
		return psCmd(o, clientName, ev)
	}
	return unixCmd(o.Exe, clientName, ev)
}

// unixCmd single-quotes the path so spaces and $ are literal.
func unixCmd(exe, clientName, ev string) string {
	s := shQuote(exe) + " hook " + clientName
	if ev != "" {
		s += " " + ev
	}
	return s
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// psCmd runs the PowerShell shim: valid in cmd.exe, PowerShell and Git Bash.
func psCmd(o Options, clientName, ev string) string {
	ps1 := filepath.Join(filepath.Dir(o.Exe), "aw.ps1")
	if o.goos() == "windows" {
		ps1 = winJoin(o.Exe, "aw.ps1")
	}
	s := `powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "` + ps1 + `" hook ` + clientName
	if ev != "" {
		s += " " + ev
	}
	return s
}

// psInline is for Copilot's `powershell` field, which PowerShell runs directly.
func psInline(o Options, clientName, ev string) string {
	ps1 := filepath.Join(filepath.Dir(o.Exe), "aw.ps1")
	if o.goos() == "windows" {
		ps1 = winJoin(o.Exe, "aw.ps1")
	}
	return "& '" + strings.ReplaceAll(ps1, "'", "''") + "' hook " + clientName + " " + ev
}

func winJoin(exe, name string) string {
	i := strings.LastIndexAny(exe, `\/`)
	if i < 0 {
		return name
	}
	return exe[:i+1] + name
}

// ---- records / files --------------------------------------------------------------

func recordPath(clientName, scope, path string) string {
	return filepath.Join(config.StateDir(), "installs", clientName+"-"+scope+"-"+sum([]byte(path))[:12]+".json")
}

func loadRecord(clientName, scope, path string) *Record {
	b, err := os.ReadFile(recordPath(clientName, scope, path))
	if err != nil {
		return nil
	}
	var r Record
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func saveRecord(r *Record) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	return config.WriteFileAtomic(recordPath(r.Client, r.Scope, r.Path), b, 0o600)
}

func deleteRecord(clientName, scope, path string) { _ = os.Remove(recordPath(clientName, scope, path)) }

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
