// Package selfprotect stops an agent from changing the files that keep it
// watched (hook configs, the sensor's own state, the plugin installs) and
// from reading the sensor's credential, so a hijacked agent can neither switch
// AgentWatch off nor steal this machine's device token.
//
// Reading the protected places is allowed (ls, find, cat, grep on the plugin
// folders): only writes are blocked there. The one read that is blocked is the
// credential: ~/.agentwatch/credentials* and the OS keychain entry.
//
// It is a tripwire for actions that go through the client's hooks, not a
// sandbox. A command that builds a path at runtime (variables, base64, an
// interpreter one-liner that assembles a path) can get past it. LIMITS.md says so.
package selfprotect

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
	"github.com/chinmayNayar/agentwatch-plugins/internal/lazyre"
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

// Keychain stores: reading these files directly is a credential read.
var keychainDirs = []string{"~/Library/Keychains/", "~/.local/share/keyrings/"}

var (
	// Fallback when a command cannot be parsed: any write verb anywhere plus
	// any protected path anywhere blocks (the pre-0.1.2 rule).
	writeVerbs = lazyre.New(`(?i)(>>?|\btee\b|\bsed\s+(-[a-z]*i|--in-place)|\brm\b|\bmv\b|\bcp\b|\btruncate\b|\bchmod\b|\bchown\b|\bln\b|\bunlink\b|\bdd\b|\bperl\s+-[a-z]*i|\bpython3?\s+-c|\bnode\s+-e|Set-Content|Add-Content|Out-File|Remove-Item|Move-Item|Copy-Item|\bdel\b|\berase\b|\bjq\b.*>|\bsponge\b|\bpatch\b|\bgit\s+(checkout|restore|apply|clean|reset)\b)`)
	offSwitch  = lazyre.New(`(?i)\b(agentwatch[\w.-]*|aw\.(sh|ps1))["']?\s+(mode|uninstall|logout|login|install)\b`)
	pluginOff  = lazyre.New(`(?i)\bclaude\s+plugin\s+(uninstall|disable|remove)\b[^\n]*\bagentwatch|\bclaude\s+plugin\s+marketplace\s+remove\b[^\n]*\bagentwatch|disableAllHooks`)
	extOff     = lazyre.New(`(?i)\b(code|cursor|codium|windsurf)\s+--(uninstall|disable)-extension\s+agentwatch`)
	keychainOS = lazyre.New(`(?i)(PasswordVault|CredRead|Get-StoredCredential|cmdkey\s+/list|Get-Secret\b)`)
	subTokens  = lazyre.New(`[\s'"(),;=@\[\]{}]+`)
)

const msgOff = "AgentWatch self-protection: agents may not change or remove AgentWatch. Run that command yourself."

func credMsg(what string) string {
	return "AgentWatch self-protection: agents may not read AgentWatch's credential (" + what +
		"). Use /agentwatch:status to see whether this machine is signed in."
}

func modifyMsg(hit string) string {
	return "AgentWatch self-protection: this command would modify " + hit + "."
}

// Check returns a reason when the event writes a protected file or reads the
// credential, or "".
func Check(ev *event.Event, home string) string {
	if ev.Kind != event.PreTool {
		return ""
	}
	s := &scope{home: home, cwd: ev.Cwd, awHomes: agentwatchHomes(home)}
	switch ev.Class {
	case event.ClassWrite:
		paths := []string{ev.Path}
		if ev.ToolName == "apply_patch" {
			paths = append(paths, common.PatchPaths(ev.Command)...)
		}
		for _, p := range paths {
			if hit := s.protectedHit(p, ev.Cwd); hit != "" {
				return "AgentWatch self-protection: agents may not modify " + hit +
					". Change AgentWatch settings yourself with the agentwatch CLI or the editor extension."
			}
		}
		return ""
	case event.ClassRead:
		if ev.Path != "" {
			k := s.credKind(ev.Path, ev.Cwd)
			lt := strings.ToLower(ev.ToolName)
			recursive := lt == "grep" || strings.Contains(lt, "search")
			if k == credFile || (k == credDir && recursive) {
				return credMsg(ev.Path)
			}
		}
		return ""
	}
	if r := s.checkCommand(ev.Command); r != "" {
		return r
	}
	// An MCP tool (a filesystem server, say) that names the credential file.
	if ev.Path != "" && s.credKind(ev.Path, ev.Cwd) == credFile {
		return credMsg(ev.Path)
	}
	return ""
}

type scope struct {
	home    string
	cwd     string
	awHomes []string // normalised sensor homes (~/.agentwatch, AGENTWATCH_HOME)
}

func agentwatchHomes(home string) []string {
	out := []string{normalise("~/.agentwatch", "", home)}
	if h := os.Getenv("AGENTWATCH_HOME"); h != "" {
		out = append(out, normalise(h, "", home))
	}
	return out
}

// checkCommand judges a shell (or PowerShell) command line.
func (s *scope) checkCommand(cmd string) string {
	if strings.TrimSpace(cmd) == "" {
		return ""
	}
	if pluginOff.MatchString(cmd) || extOff.MatchString(cmd) {
		return msgOff
	}
	if keychainOS.MatchString(cmd) && strings.Contains(strings.ToLower(cmd), "agentwatch") {
		return credMsg("the OS credential store entry")
	}
	cmds, err := parseShell(cmd)
	if err != nil {
		return s.fallback(cmd)
	}
	if r := s.judge(cmds, s.cwd, 0); r != "" {
		return r
	}
	if strings.Contains(cmd, `\`) {
		// The same line read with literal backslashes (Windows paths).
		if lit, err := parseWith(cmd, true); err == nil {
			return s.judge(lit, s.cwd, 0)
		}
	}
	return ""
}

// fallback: the command could not be read, so be conservative.
func (s *scope) fallback(cmd string) string {
	if offSwitch.MatchString(cmd) {
		return msgOff
	}
	toks := candidates(cmd)
	for _, t := range toks {
		if s.credKind(t, s.cwd) == credFile {
			return credMsg(t)
		}
	}
	if writeVerbs.MatchString(cmd) {
		for _, t := range toks {
			if hit := s.protectedHit(t, s.cwd); hit != "" {
				return modifyMsg(hit)
			}
		}
	}
	return ""
}

func (s *scope) judge(cmds []simpleCmd, base string, depth int) string {
	if depth > 8 {
		return ""
	}
	for _, c := range cmds {
		cwd := base
		if c.cwd != "" {
			cwd = normalise(c.cwd, base, s.home)
		}
		if r := s.judgeOne(c.args, c.redirs, cwd, cmds, depth); r != "" {
			return r
		}
	}
	return ""
}

// judgeOne looks at one simple command running in cwd. all is the whole line
// (for xargs, whose arguments come from the pipe).
func (s *scope) judgeOne(args []string, redirs []redir, cwd string, all []simpleCmd, depth int) string {
	for _, r := range redirs {
		if r.target == "" {
			continue
		}
		if r.writes() {
			if hit := s.protectedHit(r.target, cwd); hit != "" {
				return "AgentWatch self-protection: this command would write " + hit + "."
			}
		} else if s.credKind(r.target, cwd) == credFile {
			return credMsg(r.target)
		}
	}
	args = stripWrappers(args)
	if len(args) == 0 {
		return ""
	}
	name := cmdName(args[0])

	// The sensor's own off switches, under any name (versioned asset, shim).
	if isSensor(args) {
		rest := sensorArgs(args)
		sub := ""
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				sub = a
				break
			}
		}
		switch sub {
		case "uninstall", "logout", "login", "install":
			return msgOff
		case "mode":
			if len(rest) > 1 { // `agentwatch mode` alone only shows it
				return msgOff
			}
		}
	}

	// A nested shell: judge the script it runs, in this command's directory.
	if script, ok := shellScript(name, args); ok {
		inner, err := parseShell(script)
		if err != nil {
			return s.fallback(script)
		}
		return s.judge(inner, cwd, depth+1)
	}

	if r := s.keychainRead(name, args); r != "" {
		return r
	}

	for _, t := range writeTargets(args, cwd) {
		if hit := s.protectedHit(t, cwd); hit != "" {
			return modifyMsg(hit)
		}
	}

	// Credential reads: a command that is not a pure listing and names the
	// credential, or the directory holding it.
	if !listing[name] {
		for _, a := range args[1:] {
			if k := s.credKind(a, cwd); k != credNone {
				return credMsg(a)
			}
			// f=@path (curl -F), --file=path, open('path') in a one-liner
			for _, t := range subTokens.Split(a, -1) {
				if t != a && s.credKind(t, cwd) == credFile {
					return credMsg(t)
				}
			}
		}
	}

	switch name {
	case "find":
		return s.judgeFind(args, cwd, all, depth)
	case "xargs", "parallel":
		sub := xargsCommand(args)
		if len(sub) == 0 {
			return ""
		}
		subName := cmdName(sub[0])
		writes := writesArgs(sub, cwd) || interpreter[subName]
		for _, c := range all {
			for _, w := range c.args {
				if writes {
					if hit := s.protectedHit(w, cwd); hit != "" {
						return modifyMsg(hit)
					}
				}
				if !listing[subName] {
					if k := s.credKind(w, cwd); k != credNone {
						return credMsg(w)
					}
				}
			}
		}
		return s.judgeOne(sub, nil, cwd, all, depth+1)
	}

	if interpreter[name] {
		// An interpreter one-liner can write anything: block when it names a
		// protected place at all (the pre-0.1.2 rule for python -c / node -e).
		for _, a := range args[1:] {
			for _, t := range subTokens.Split(a, -1) {
				if hit := s.protectedHit(t, cwd); hit != "" {
					return "AgentWatch self-protection: this command could modify " + hit + "."
				}
			}
		}
	}
	return ""
}

// judgeFind: find only reads, unless it deletes, writes a file, or -exec's a
// command that writes. The -exec command is judged with {} as each start path.
func (s *scope) judgeFind(args []string, cwd string, all []simpleCmd, depth int) string {
	var starts []string
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		if a == "-H" || a == "-L" || a == "-P" || a == "-E" || a == "-X" || a == "-d" || a == "-s" || a == "-x" {
			continue
		}
		if strings.HasPrefix(a, "-") || a == "(" || a == `\(` || a == "!" {
			break
		}
		starts = append(starts, a)
	}
	if len(starts) == 0 {
		starts = []string{"."}
	}
	for ; i < len(args); i++ {
		switch args[i] {
		case "-delete":
			for _, st := range starts {
				if hit := s.protectedHit(st, cwd); hit != "" {
					return "AgentWatch self-protection: this command would delete files in " + hit + "."
				}
			}
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			if i+1 < len(args) {
				if hit := s.protectedHit(args[i+1], cwd); hit != "" {
					return "AgentWatch self-protection: this command would write " + hit + "."
				}
			}
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			var sub []string
			for ; j < len(args) && args[j] != ";" && args[j] != `\;` && args[j] != "+"; j++ {
				sub = append(sub, args[j])
			}
			i = j
			if len(sub) == 0 {
				continue
			}
			for _, st := range starts {
				inst := make([]string, len(sub))
				for k, w := range sub {
					inst[k] = strings.ReplaceAll(w, "{}", st)
				}
				if r := s.judgeOne(inst, nil, cwd, all, depth+1); r != "" {
					return r
				}
				real := stripWrappers(inst)
				if len(real) == 0 {
					continue
				}
				subName := cmdName(real[0])
				// {} stands for everything below a start path: a write to any of it counts.
				if len(writeTargets(real, cwd)) > 0 || writesArgs(real, cwd) || interpreter[subName] {
					if hit := s.protectedHit(st, cwd); hit != "" {
						return "AgentWatch self-protection: this command would modify files in " + hit + "."
					}
				}
				if !listing[subName] {
					if k := s.credKind(st, cwd); k != credNone {
						return credMsg(st)
					}
				}
			}
		}
	}
	return ""
}

func (s *scope) keychainRead(name string, args []string) string {
	joined := strings.ToLower(strings.Join(args, " "))
	switch name {
	case "security":
		if len(args) < 2 {
			return ""
		}
		switch args[1] {
		case "dump-keychain", "export":
			return credMsg("the keychain")
		case "find-generic-password", "find-internet-password", "find-key":
			if strings.Contains(joined, "agentwatch") || !hasAny(args[2:], "-s", "-l", "-a", "-c") {
				return credMsg("the keychain entry")
			}
		}
	case "secret-tool", "keyring", "kwallet-query":
		if strings.Contains(joined, "agentwatch") || hasAny(args, "search", "--all") {
			return credMsg("the keyring entry")
		}
	}
	return ""
}

func hasAny(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w || strings.HasPrefix(a, w+"=") {
				return true
			}
		}
	}
	return false
}

// ---- command tables ---------------------------------------------------------

// listing commands read names and metadata, never file contents.
var listing = set("ls", "stat", "find", "du", "file", "test", "[", "[[", "realpath", "readlink", "dirname",
	"basename", "tree", "cd", "pushd", "popd", "echo", "printf", "exa", "eza", "lsd", "wc", "true", ":",
	"get-childitem", "gci", "dir", "test-path", "get-item", "gi", "mkdir", "type", "which", "where",
	"command", "pwd", "df")

// interpreter one-liners can do anything with a path they mention.
var interpreter = set("python", "python2", "python3", "node", "nodejs", "deno", "bun", "perl", "ruby", "php",
	"osascript", "lua", "tclsh", "awk", "gawk", "mawk", "nawk", "jshell", "swift", "groovy")

// allArgs: every non-flag argument is written (or removed, or re-permissioned).
var allArgs = set("rm", "rmdir", "unlink", "shred", "srm", "trash", "truncate", "touch", "mv", "chmod",
	"chown", "chgrp", "chflags", "setfacl", "tee", "sponge", "patch", "vi", "vim", "nvim", "view", "nano",
	"pico", "micro", "emacs", "ed", "ex", "code", "cursor", "subl", "mate", "kate", "gedit",
	"remove-item", "ri", "del", "erase", "rd", "move-item", "mi", "move", "rename-item", "ren", "rni",
	"set-content", "sc", "add-content", "ac", "out-file", "clear-content", "clc", "new-item", "ni",
	"set-itemproperty", "sp", "mkfifo", "mknod", "gio")

// lastArg: the last non-flag argument (or -t DIR) is the destination.
var lastArg = set("cp", "ln", "install", "rsync", "scp", "ditto", "copy-item", "copy", "cpi", "rclone")

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// cmdName is the lower-cased base name of argv[0] ("/bin/rm" -> "rm").
func cmdName(a string) string {
	a = strings.ReplaceAll(a, `\`, "/")
	if i := strings.LastIndexByte(a, '/'); i >= 0 {
		a = a[i+1:]
	}
	a = strings.ToLower(a)
	return strings.TrimSuffix(a, ".exe")
}

// stripWrappers removes env assignments and runners that only start the real
// command (sudo, env, nohup, time, nice, timeout, command, exec, builtin).
func stripWrappers(args []string) []string {
	for len(args) > 0 {
		a := args[0]
		if i := strings.IndexByte(a, '='); i > 0 && isName(a[:i]) {
			args = args[1:]
			continue
		}
		switch cmdName(a) {
		case "sudo", "doas":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				f := args[0]
				args = args[1:]
				if (f == "-u" || f == "-g" || f == "-C" || f == "-D" || f == "-h" || f == "-p" || f == "-U") && len(args) > 0 {
					args = args[1:]
				}
			}
		case "env":
			args = args[1:]
		envFlags:
			for len(args) > 0 {
				f := args[0]
				switch {
				case f == "-S" && len(args) > 1: // env -S "cmd args": split the string
					args = append(strings.Fields(args[1]), args[2:]...)
					break envFlags
				case f == "-u" || f == "-C":
					args = args[min(2, len(args)):]
				case strings.HasPrefix(f, "-"):
					args = args[1:]
				case strings.IndexByte(f, '=') > 0 && isName(f[:strings.IndexByte(f, '=')]):
					args = args[1:]
				default:
					break envFlags
				}
			}
		case "nohup", "command", "builtin", "exec", "caffeinate", "noglob", "unbuffer", "chronic", "time":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				args = args[1:]
			}
		case "nice", "ionice", "stdbuf", "chrt", "taskset":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				f := args[0]
				args = args[1:]
				if len(f) == 2 && len(args) > 0 { // "-n 10"
					args = args[1:]
				}
			}
		case "timeout", "gtimeout":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				f := args[0]
				args = args[1:]
				if (f == "-s" || f == "-k") && len(args) > 0 {
					args = args[1:]
				}
			}
			if len(args) > 0 {
				args = args[1:] // the duration
			}
		default:
			return args
		}
	}
	return args
}

func isName(s string) bool {
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return s != ""
}

// shellScript returns the script of `sh -c SCRIPT`, `eval ...`, `pwsh -c ...`.
func shellScript(name string, args []string) (string, bool) {
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "ash", "mksh":
		for i := 1; i < len(args); i++ {
			a := args[i]
			if a == "-c" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c")) {
				if i+1 < len(args) {
					return args[i+1], true
				}
			}
		}
	case "eval":
		return strings.Join(args[1:], " "), true
	case "pwsh", "powershell":
		for i := 1; i < len(args); i++ {
			if l := strings.ToLower(args[i]); l == "-c" || l == "-command" {
				return strings.Join(args[i+1:], " "), true
			}
		}
	case "cmd":
		for i := 1; i < len(args); i++ {
			if l := strings.ToLower(args[i]); l == "/c" || l == "/k" {
				return strings.Join(args[i+1:], " "), true
			}
		}
	}
	return "", false
}

func sensorName(a string) bool {
	n := cmdName(a)
	return n == "agentwatch" || strings.HasPrefix(n, "agentwatch-") || n == "aw.sh" || n == "aw.ps1"
}

// isSensor: argv names the agentwatch binary (any versioned asset name) or
// the plugin shim, directly or as `sh aw.sh` / `pwsh -File aw.ps1`.
func isSensor(args []string) bool { return sensorArgs(args) != nil }

func sensorArgs(args []string) []string {
	for i, a := range args {
		if i > 2 {
			break
		}
		if sensorName(a) {
			return append([]string{}, args[i+1:]...)
		}
		n := cmdName(a)
		if i > 0 && !(n == "-file" || strings.HasPrefix(a, "-")) {
			break
		}
		if i == 0 && n != "sh" && n != "bash" && n != "zsh" && n != "pwsh" && n != "powershell" {
			break
		}
	}
	return nil
}

// xargsCommand is the command xargs runs, without xargs's own flags.
func xargsCommand(args []string) []string {
	valued := set("-n", "-i", "-I", "-P", "-L", "-l", "-d", "-E", "-e", "-s", "-a", "-J", "-R", "-S",
		"--max-args", "--max-procs", "--delimiter", "--arg-file", "--replace", "-j", "--jobs")
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return args[i+1:]
		}
		if strings.HasPrefix(a, "-") {
			if valued[a] {
				i++
			}
			continue
		}
		return args[i:]
	}
	return nil
}

// positional drops flags (and the values of the few flags known to take one).
func positional(args []string, valued map[string]bool) []string {
	var out []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if valued[a] {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// writesArgs: the command writes the paths it is given (xargs and find -exec
// append paths the line does not show).
func writesArgs(args []string, cwd string) bool {
	probe := append(append([]string{}, args...), "/aw-probe")
	for _, t := range writeTargets(probe, cwd) {
		if t == "/aw-probe" {
			return true
		}
	}
	return false
}

// writeTargets lists the paths a simple command writes, removes or
// re-permissions. Commands not listed here are treated as read-only.
func writeTargets(args []string, cwd string) []string {
	if len(args) == 0 {
		return nil
	}
	name := cmdName(args[0])
	switch {
	case allArgs[name]:
		if name == "gio" && (len(args) < 2 || !set("trash", "remove", "move", "copy", "rename", "set", "save")[args[1]]) {
			return nil
		}
		var out []string
		for i := 1; i < len(args); i++ {
			a := args[i]
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				// PowerShell named parameters: -Path X, -Destination X, -LiteralPath X
				l := strings.ToLower(a)
				if (l == "-path" || l == "-literalpath" || l == "-destination" || l == "-filepath") && i+1 < len(args) {
					out = append(out, args[i+1])
					i++
				}
				continue
			}
			out = append(out, a)
		}
		return out
	case lastArg[name]:
		for i := 1; i < len(args); i++ {
			a := args[i]
			if (a == "-t" || strings.EqualFold(a, "-destination")) && i+1 < len(args) {
				return []string{args[i+1]}
			}
			if strings.HasPrefix(a, "--target-directory=") {
				return []string{strings.TrimPrefix(a, "--target-directory=")}
			}
		}
		pos := positional(args, set("-S", "--suffix", "-m", "--mode", "-o", "--owner", "-g", "--group", "-e", "--rsh", "-f", "--filter"))
		if len(pos) == 0 {
			return nil
		}
		if name == "rsync" && hasAny(args, "--remove-source-files", "--delete") {
			return pos
		}
		return pos[len(pos)-1:]
	}
	switch name {
	case "sed", "gsed":
		inPlace := false
		for _, a := range args[1:] {
			if a == "--in-place" || strings.HasPrefix(a, "--in-place=") ||
				(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "i")) {
				inPlace = true
			}
		}
		if !inPlace {
			return nil
		}
		pos := positional(args, set("-e", "-f", "--expression", "--file", "-l"))
		if !hasAny(args, "-e", "-f", "--expression", "--file") && len(pos) > 0 {
			pos = pos[1:] // the first positional is the script
		}
		return pos
	case "perl":
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "i") {
				return positional(args, set("-e", "-E", "-M", "-I"))
			}
		}
	case "xattr":
		if hasAny(args, "-w", "-d", "-c", "-r") {
			return positional(args, nil)
		}
	case "dd":
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "of=") {
				return []string{strings.TrimPrefix(a, "of=")}
			}
		}
	case "curl":
		for i := 1; i < len(args); i++ {
			a := args[i]
			if (a == "-o" || a == "--output" || a == "--output-dir" || a == "-c" || a == "--cookie-jar" || a == "-D" || a == "--dump-header") && i+1 < len(args) {
				return []string{args[i+1]}
			}
			if a == "-O" || a == "--remote-name" || a == "-J" {
				return []string{cwd}
			}
		}
	case "wget":
		for i := 1; i < len(args); i++ {
			a := args[i]
			if (a == "-O" || a == "-P" || a == "--output-document" || a == "--directory-prefix" || a == "-o") && i+1 < len(args) {
				return []string{args[i+1]}
			}
			if strings.HasPrefix(a, "--output-document=") || strings.HasPrefix(a, "--directory-prefix=") {
				return []string{a[strings.IndexByte(a, '=')+1:]}
			}
		}
		return []string{cwd}
	case "tar", "bsdtar", "gtar":
		return tarTargets(args, cwd)
	case "unzip", "7z", "7za":
		for i := 1; i < len(args); i++ {
			if args[i] == "-d" && i+1 < len(args) {
				return []string{args[i+1]}
			}
			if strings.HasPrefix(args[i], "-o") && len(args[i]) > 2 { // 7z -oDIR
				return []string{args[i][2:]}
			}
		}
		return []string{cwd}
	case "zip":
		if pos := positional(args, nil); len(pos) > 0 {
			return pos[:1]
		}
	case "git":
		return gitTargets(args, cwd)
	case "mkdir":
		return positional(args, set("-m", "--mode"))
	}
	return nil
}

func tarTargets(args []string, cwd string) []string {
	mode := ""
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		mode = args[1] // tar xzf ...
	}
	for _, a := range args[1:] {
		switch {
		case a == "--extract" || a == "--get":
			mode += "x"
		case a == "--create" || a == "--append" || a == "--update":
			mode += "c"
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
			mode += a
		}
	}
	var out []string
	if strings.Contains(mode, "x") {
		dir := cwd
		for i := 1; i < len(args); i++ {
			if (args[i] == "-C" || args[i] == "--directory") && i+1 < len(args) {
				dir = args[i+1]
			}
		}
		out = append(out, dir)
	}
	if strings.ContainsAny(mode, "cru") {
		for i := 1; i < len(args); i++ {
			a := args[i]
			if a == "--file" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.HasSuffix(a, "f")) ||
				(i == 1 && !strings.HasPrefix(a, "-") && strings.HasSuffix(a, "f")) {
				if i+1 < len(args) {
					out = append(out, args[i+1])
				}
			}
		}
	}
	return out
}

var gitReadOnly = set("status", "log", "diff", "show", "rev-parse", "ls-files", "ls-tree", "ls-remote", "blame",
	"grep", "describe", "shortlog", "cat-file", "reflog", "whatchanged", "version", "help", "rev-list",
	"name-rev", "for-each-ref", "show-ref", "count-objects", "check-ignore", "var", "archive", "bundle")

// gitTargets: a git command that changes the work tree or the repository
// writes the repository directory.
func gitTargets(args []string, cwd string) []string {
	dir := cwd
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		if (a == "-C" || a == "--git-dir" || a == "--work-tree") && i+1 < len(args) {
			dir = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") {
			dir = a[strings.IndexByte(a, '=')+1:]
			continue
		}
		if a == "-c" && i+1 < len(args) {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		break
	}
	if i >= len(args) {
		return nil
	}
	sub, rest := args[i], args[i+1:]
	if gitReadOnly[sub] {
		return nil
	}
	switch sub {
	case "branch", "tag", "remote", "stash", "config", "worktree", "notes":
		writes := false
		for _, a := range rest {
			switch a {
			case "-d", "-D", "-m", "-M", "-f", "--delete", "--move", "--force", "-a", "add", "remove", "rm",
				"rename", "set-url", "push", "pop", "drop", "apply", "clear", "--unset", "--add", "--replace-all", "prune":
				writes = true
			}
		}
		if sub == "config" && len(positional(append([]string{"config"}, rest...), nil)) >= 2 {
			writes = true
		}
		if sub == "stash" && len(rest) == 0 {
			writes = true
		}
		if !writes {
			return nil // listing forms are read-only
		}
	case "clone":
		pos := positional(append([]string{"clone"}, rest...), set("-b", "--branch", "-o", "--origin", "--depth", "-c", "--config"))
		if len(pos) >= 2 {
			return pos[len(pos)-1:]
		}
		return []string{cwd}
	}
	return []string{dir}
}

// ---- path matching -------------------------------------------------------------

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

// protectedHit returns the protected pattern p falls under, or "". A glob
// ("~/.agentwatch/*") counts when it could match a protected path.
func (s *scope) protectedHit(p, cwd string) string {
	if p == "" {
		return ""
	}
	if hit := match(p, cwd, s.home); hit != "" {
		return hit
	}
	n := normalise(p, cwd, s.home)
	for _, h := range s.awHomes {
		if n == h || strings.HasPrefix(n, h+"/") {
			return "~/.agentwatch/"
		}
	}
	if strings.ContainsAny(p, "*?[") {
		h := normalise(s.home, "", s.home)
		for _, pat := range protected {
			if !strings.HasPrefix(pat, "~/") {
				continue
			}
			full := strings.TrimSuffix(h+"/"+strings.TrimPrefix(pat, "~/"), "/")
			if ok, _ := path.Match(n, full); ok {
				return pat
			}
			if ok, _ := path.Match(n, full+"/x"); ok && strings.HasSuffix(pat, "/") {
				return pat
			}
		}
	}
	return ""
}

type credKindT int

const (
	credNone credKindT = iota
	credFile           // the credential itself (or a keychain store)
	credDir            // the directory that holds it
)

// credKind says whether p names the sensor credential (a file, or a glob
// that matches one) or the directory holding it.
func (s *scope) credKind(p, cwd string) credKindT {
	if p == "" {
		return credNone
	}
	n := normalise(p, cwd, s.home)
	glob := strings.ContainsAny(p, "*?[")
	for _, h := range s.awHomes {
		cred := h + "/credentials"
		if n == cred || (path.Dir(n) == h && isCredName(path.Base(n))) {
			return credFile
		}
		if glob {
			for _, c := range []string{cred, h + "/credentials.json", h + "/.tmp-credentials-x"} {
				if ok, _ := path.Match(n, c); ok {
					return credFile
				}
			}
			if ok, _ := path.Match(n, h); ok {
				return credDir
			}
		}
		if n == h {
			return credDir
		}
	}
	hn := normalise(s.home, "", s.home)
	for _, k := range keychainDirs {
		if strings.HasPrefix(n+"/", hn+"/"+strings.TrimPrefix(k, "~/")) {
			return credFile
		}
	}
	return credNone
}

func isCredName(b string) bool {
	b = strings.ToLower(b)
	return strings.HasPrefix(b, "credentials") || strings.HasPrefix(b, ".tmp-credentials")
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
		c := strings.ReplaceAll(cwd, `\`, "/")
		if c == "~" || strings.HasPrefix(c, "~/") {
			c = strings.ReplaceAll(home, `\`, "/") + c[1:]
		}
		p = msysToWin(c) + "/" + p
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
