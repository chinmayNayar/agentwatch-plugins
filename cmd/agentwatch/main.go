// Command agentwatch is the AgentWatch sensor for coding agents and editors.
//
//	agentwatch hook <client> [event]   called by the client's hooks (stdin: payload)
//	agentwatch install <client|all>    add hooks (user scope; --project for project files)
//	agentwatch uninstall <client|all>  remove hooks, restoring the original file
//	agentwatch doctor                  check setup per client
//	agentwatch login                   sign this machine in (device code)
//	agentwatch logout                  forget this machine's credential
//	agentwatch status                  mode, sign-in, passports, queue
//	agentwatch link [--dir DIR]        put `agentwatch` on PATH (~/.local/bin)
//	agentwatch mode [dry_run|enforce]  show or change the mode (asks before enforcing)
//	agentwatch mcp                     stdio MCP server (read-only tools)
//	agentwatch version
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters"
	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/doctor"
	"github.com/chinmayNayar/agentwatch-plugins/internal/flush"
	"github.com/chinmayNayar/agentwatch-plugins/internal/hook"
	"github.com/chinmayNayar/agentwatch-plugins/internal/install"
	"github.com/chinmayNayar/agentwatch-plugins/internal/login"
	"github.com/chinmayNayar/agentwatch-plugins/internal/mcp"
	"github.com/chinmayNayar/agentwatch-plugins/internal/status"
)

var commit = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "hook":
		exe, _ := os.Executable()
		return hook.Run(rest, hook.Options{Stdin: stdin, Stdout: stdout, Exe: exe})
	case "flush":
		r := flush.Run(nil)
		if len(rest) > 0 && rest[0] == "--verbose" {
			fmt.Fprintf(stdout, "sent %d, dead %d, heartbeat %v %s\n", r.Sent, r.Dead, r.Heartbeat, r.Err)
		}
		return 0
	case "install":
		return cmdInstall(rest, stdin, stdout, stderr)
	case "uninstall":
		return cmdUninstall(rest, stdout, stderr)
	case "doctor":
		return cmdDoctor(rest, stdout)
	case "login":
		return cmdLogin(rest, stdin, stdout, stderr)
	case "logout":
		config.DeleteCredential()
		fmt.Fprintln(stdout, "Signed out on this machine. To revoke the device token itself, use Devices in the AgentWatch console.")
		return 0
	case "status":
		return cmdStatus(rest, stdout)
	case "link":
		return cmdLink(rest, stdout, stderr)
	case "mode":
		return cmdMode(rest, stdin, stdout, stderr)
	case "mcp":
		if err := mcp.New().Serve(stdin, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "agentwatch %s (%s) %s\n", client.Version, commit, config.OSArch())
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n", cmd)
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `AgentWatch sensor for coding agents

Usage:
  agentwatch login [--api-url URL] [--api-key] [--no-browser]
  agentwatch install <claude-code|cursor|codex|copilot|all> [--project DIR] [--dry-run] [--json] [--yes]
  agentwatch uninstall <client|all> [--project DIR]
  agentwatch doctor [--json]
  agentwatch status [--json]
  agentwatch mode [dry_run|enforce] [--fail-closed|--fail-open] [--strict on|off] [--yes]
  agentwatch link [--dir DIR] [--force]
  agentwatch mcp
  agentwatch logout
  agentwatch version
`)
}

func isTTY(f any) bool {
	if fh, ok := f.(*os.File); ok {
		return term.IsTerminal(int(fh.Fd()))
	}
	return false
}

func clientsArg(name string) ([]string, error) {
	if name == "all" {
		return adapters.Clients, nil
	}
	if adapters.Known(name) {
		return []string{name}, nil
	}
	return nil, fmt.Errorf("unknown client %q (claude-code, cursor, codex, copilot, all)", name)
}

// installedBinary copies the running binary to ~/.agentwatch/bin so hooks do
// not point into an editor extension folder that an update replaces.
func installedBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "agentwatch"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(config.BinDir(), name)
	if rs, _ := filepath.EvalSymlinks(self); rs != "" {
		self = rs
	}
	if rd, _ := filepath.EvalSymlinks(dst); rd == self {
		return dst, nil
	}
	b, err := os.ReadFile(self)
	if err != nil {
		return "", err
	}
	if cur, err := os.ReadFile(dst); err == nil && string(cur) == string(b) {
		return dst, nil
	}
	if err := config.WriteFileAtomic(dst, b, 0o755); err != nil {
		return "", fmt.Errorf("copy binary to %s: %w", dst, err)
	}
	return dst, nil
}

func cmdInstall(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "install into this project's files instead of user scope")
	dry := fs.Bool("dry-run", false, "show what would change; write nothing")
	asJSON := fs.Bool("json", false, "machine-readable output")
	yes := fs.Bool("yes", false, "do not ask")
	exeFlag := fs.String("exe", "", "binary path the hooks run (default: ~/.agentwatch/bin/agentwatch)")
	if len(args) == 0 {
		fmt.Fprintln(stderr, "install: name a client (claude-code, cursor, codex, copilot, all)")
		return 2
	}
	target := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	clients, err := clientsArg(target)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg := config.Load()
	scope := install.ScopeUser
	if *project != "" {
		scope = install.ScopeProject
		abs, _ := filepath.Abs(*project)
		*project = abs
	}
	exe := *exeFlag
	if exe == "" && !*dry {
		if exe, err = installedBinary(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else if exe == "" {
		exe = filepath.Join(config.BinDir(), map[bool]string{true: "agentwatch.exe", false: "agentwatch"}[runtime.GOOS == "windows"])
	}
	var plans []*install.Plan
	for _, c := range clients {
		o := install.Options{Client: c, Scope: scope, ProjectDir: *project, Exe: exe, Mode: cfg.Mode, FailMode: cfg.FailMode}
		p, err := install.PlanInstall(o)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", c, err)
			return 1
		}
		if scope == install.ScopeProject && !*dry && !*yes {
			fmt.Fprintln(stderr, "Warning: "+p.Warning)
			if !confirm(stdin, stderr, "Write project hooks anyway? [y/N] ", "y") {
				fmt.Fprintln(stderr, "Nothing changed.")
				return 1
			}
		}
		if !*dry {
			if p, err = install.Install(o); err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", c, err)
				return 1
			}
		}
		plans = append(plans, p)
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"dry_run": *dry, "plans": plans})
		return 0
	}
	for _, p := range plans {
		verb := "Installed"
		if *dry {
			verb = "Would change"
		}
		if !p.Changed {
			verb = "Already up to date:"
		}
		fmt.Fprintf(stdout, "%s %s hooks in %s\n", verb, p.Client, p.Path)
		if *dry && p.Changed {
			fmt.Fprint(stdout, diff(p.Before, p.After))
		}
		if p.Note != "" {
			fmt.Fprintln(stdout, "  Note: "+p.Note)
		}
	}
	if !*dry {
		fmt.Fprintf(stdout, "Mode: %s, %s. Check with: agentwatch doctor\n", cfg.Mode, cfg.FailMode)
		if _, err := config.LoadCredential(); err != nil {
			fmt.Fprintln(stdout, "Not signed in yet. To sign in, "+config.SignInHint())
		}
	}
	return 0
}

// diff is a minimal line diff (enough to show which hooks are added).
func diff(before, after string) string {
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")
	inB := map[string]int{}
	for _, l := range b {
		inB[l]++
	}
	inA := map[string]int{}
	for _, l := range a {
		inA[l]++
	}
	var out strings.Builder
	for _, l := range b {
		if inA[l] == 0 && strings.TrimSpace(l) != "" {
			out.WriteString("  - " + l + "\n")
		}
	}
	for _, l := range a {
		if inB[l] == 0 && strings.TrimSpace(l) != "" {
			out.WriteString("  + " + l + "\n")
		}
	}
	return out.String()
}

func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project directory (for a --project install)")
	if len(args) == 0 {
		fmt.Fprintln(stderr, "uninstall: name a client or all")
		return 2
	}
	target := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	clients, err := clientsArg(target)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	scope := install.ScopeUser
	if *project != "" {
		scope = install.ScopeProject
		*project, _ = filepath.Abs(*project)
	}
	code := 0
	for _, c := range clients {
		r, err := install.Uninstall(c, scope, *project)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", c, err)
			code = 1
			continue
		}
		msg := map[string]string{"restored_backup": "restored the original file", "removed_file": "removed the file it created",
			"removed_entries": "removed AgentWatch's entries", "nothing": "nothing to remove"}[r.Action]
		fmt.Fprintf(stdout, "%s: %s (%s)\n", c, msg, r.Path)
		if r.Detail != "" {
			fmt.Fprintln(stdout, "  "+r.Detail)
		}
	}
	return code
}

func cmdDoctor(args []string, stdout io.Writer) int {
	r := doctor.Run(nil)
	if len(args) > 0 && args[0] == "--json" {
		_ = json.NewEncoder(stdout).Encode(r)
	} else {
		fmt.Fprint(stdout, doctor.Text(r))
	}
	if r.OK {
		return 0
	}
	return 1
}

func cmdStatus(args []string, stdout io.Writer) int {
	r := status.Build(mcp.DashboardURL)
	if len(args) > 0 && args[0] == "--json" {
		_ = json.NewEncoder(stdout).Encode(r)
		return 0
	}
	fmt.Fprint(stdout, status.Text(r))
	return 0
}

// cmdLink puts `agentwatch` on PATH: a symlink in ~/.local/bin (or --dir) to
// this binary, through the plugin's stable link when there is one so it
// follows plugin updates. It never replaces a file it did not create.
func cmdLink(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", filepath.Join(config.UserHome(), ".local", "bin"), "directory on PATH")
	force := fs.Bool("force", false, "replace an existing agentwatch symlink that points elsewhere")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(stderr, "link is for macOS and Linux. On Windows, add this folder to PATH instead:", filepath.Dir(selfPath()))
		return 1
	}
	target := selfPath()
	stable := filepath.Join(filepath.Dir(target), "agentwatch")
	if r, err := filepath.EvalSymlinks(stable); err == nil && r == target {
		target = stable
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dst := filepath.Join(*dir, "agentwatch")
	if fi, err := os.Lstat(dst); err == nil {
		cur, _ := os.Readlink(dst)
		switch {
		case fi.Mode()&os.ModeSymlink == 0:
			fmt.Fprintf(stderr, "%s exists and is not a symlink; not replacing it.\n", dst)
			return 1
		case cur == target:
			fmt.Fprintf(stdout, "Already linked: %s -> %s\n", dst, target)
			return linkPathNote(*dir, stdout)
		case !*force:
			fmt.Fprintf(stderr, "%s already points to %s; run with --force to replace it.\n", dst, cur)
			return 1
		}
		if err := os.Remove(dst); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err := os.Symlink(target, dst); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Linked %s -> %s\n", dst, target)
	return linkPathNote(*dir, stdout)
}

func linkPathNote(dir string, stdout io.Writer) int {
	if status.LocalBinOnPath(dir) {
		fmt.Fprintln(stdout, "You can now run: agentwatch status")
		return 0
	}
	fmt.Fprintf(stdout, "%s is not on your PATH yet. Add this line to ~/.zshrc (or ~/.bashrc), then open a new terminal:\n    export PATH=\"%s:$PATH\"\n", dir, dir)
	return 0
}

func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "agentwatch"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		return r
	}
	return exe
}

func confirm(stdin io.Reader, w io.Writer, prompt, want string) bool {
	if !isTTY(stdin) {
		return false
	}
	fmt.Fprint(w, prompt)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), want)
}

func cmdMode(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	failClosed := fs.Bool("fail-closed", false, "block when AgentWatch cannot answer (enforce only)")
	failOpen := fs.Bool("fail-open", false, "allow when AgentWatch cannot answer (default)")
	strict := fs.String("strict", "", "on|off: send every call, reads too, to preflight")
	timeoutMS := fs.Int("timeout-ms", 0, "preflight timeout in milliseconds")
	yes := fs.Bool("yes", false, "confirm without a prompt (for scripts run by a person)")
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg := config.Load()
	if target == "" && !*failClosed && !*failOpen && *strict == "" && *timeoutMS == 0 {
		fmt.Fprintf(stdout, "mode %s (%s), %s, strict %v, preflight timeout %d ms\n", cfg.Mode, cfg.Sources["mode"], cfg.FailMode, cfg.Strict, cfg.PreflightTimeoutMS)
		return 0
	}
	if target != "" && target != config.ModeDryRun && target != config.ModeEnforce {
		fmt.Fprintln(stderr, "mode must be dry_run or enforce")
		return 2
	}
	// Persist from the file's own values, not env overrides.
	fileCfg := &config.Config{}
	if b, err := os.ReadFile(config.Path()); err == nil {
		_ = json.Unmarshal(b, fileCfg)
	}
	if fileCfg.Mode == "" {
		fileCfg.Mode = config.ModeDryRun
	}
	if fileCfg.FailMode == "" {
		fileCfg.FailMode = config.FailOpen
	}
	newMode, newFail := fileCfg.Mode, fileCfg.FailMode
	if target != "" {
		newMode = target
	}
	if *failClosed {
		newFail = config.FailClosed
	}
	if *failOpen {
		newFail = config.FailOpen
	}
	tightening := (newMode == config.ModeEnforce && fileCfg.Mode != config.ModeEnforce) ||
		(newFail == config.FailClosed && fileCfg.FailMode != config.FailClosed)
	if tightening && !*yes {
		fmt.Fprintln(stderr, "This turns on blocking:")
		if newMode == config.ModeEnforce {
			fmt.Fprintln(stderr, "  - enforce: denied, held and isolated tool calls are refused in every installed client.")
		}
		if newFail == config.FailClosed && newMode == config.ModeEnforce {
			fmt.Fprintln(stderr, "  - fail_closed: when AgentWatch cannot answer in time, shell, write, network and MCP calls are refused.")
		}
		if !confirm(stdin, stderr, "Type 'enforce' to confirm: ", "enforce") {
			fmt.Fprintln(stderr, "Not changed. (Needs a terminal to confirm, or --yes.)")
			return 1
		}
	}
	fileCfg.Mode, fileCfg.FailMode = newMode, newFail
	switch *strict {
	case "on":
		fileCfg.Strict = true
	case "off":
		fileCfg.Strict = false
	}
	if *timeoutMS > 0 {
		fileCfg.PreflightTimeoutMS = *timeoutMS
	}
	if fileCfg.APIURL == "" {
		fileCfg.APIURL = cfg.APIURL
	}
	if err := fileCfg.Save(); err != nil {
		fmt.Fprintln(stderr, "save:", err)
		return 1
	}
	// Re-render installed hooks so Claude Code onFailure / Cursor failClosed match.
	if exe, err := installedBinary(); err == nil {
		for _, c := range adapters.Clients {
			if st := install.Check(c); st.Installed {
				_, _ = install.Install(install.Options{Client: c, Scope: install.ScopeUser, Exe: exe, Mode: newMode, FailMode: newFail})
			}
		}
	}
	fmt.Fprintf(stdout, "mode %s, %s, strict %v\n", newMode, newFail, fileCfg.Strict)
	if src := cfg.Sources["mode"]; strings.HasPrefix(src, "CLAUDE_PLUGIN_OPTION") || strings.HasPrefix(src, "AGENTWATCH_") {
		fmt.Fprintf(stdout, "Note: %s overrides this file while it is set.\n", src)
	}
	return 0
}

func cmdLogin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", "", "AgentWatch API (default https://api.agentwatch.in)")
	apiKey := fs.Bool("api-key", false, "paste an API key instead of the device flow (read from stdin)")
	tokenStdin := fs.Bool("token-stdin", false, "store a device token read from stdin (used by the editor extension)")
	noBrowser := fs.Bool("no-browser", false, "do not open a browser")
	name := fs.String("name", "", "device name shown in the console (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg := config.Load()
	if *apiURL != "" || *name != "" {
		// Persist the person's choices before signing in, so the flow uses them.
		fileCfg := &config.Config{}
		if b, err := os.ReadFile(config.Path()); err == nil {
			_ = json.Unmarshal(b, fileCfg)
		}
		if *apiURL != "" {
			if err := config.ValidAPIURL(*apiURL); err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			cfg.APIURL = strings.TrimRight(*apiURL, "/")
			fileCfg.APIURL = cfg.APIURL
		}
		if *name != "" {
			cfg.DeviceName, fileCfg.DeviceName = *name, *name
		}
		if err := fileCfg.Save(); err != nil {
			fmt.Fprintln(stderr, "could not save config:", err)
			return 1
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	if *apiKey || *tokenStdin {
		var tok string
		if isTTY(stdin) {
			fmt.Fprint(stderr, "Paste the key (input hidden): ")
			if f, ok := stdin.(*os.File); ok {
				b, err := term.ReadPassword(int(f.Fd()))
				fmt.Fprintln(stderr)
				if err != nil {
					return 1
				}
				tok = string(b)
			}
		} else {
			b, _ := io.ReadAll(io.LimitReader(stdin, 4096))
			tok = string(b)
		}
		r, err := login.Save(ctx, cfg, strings.TrimSpace(tok), "", "")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printSignedIn(stdout, r, cfg)
		return 0
	}

	f, err := login.Start(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "To sign in this machine, open:\n\n    %s\n\nand enter the code:  %s\n\n", f.Pending.VerificationURI, f.Pending.UserCode)
	if !*noBrowser && login.OpenBrowser(f.Pending.CompleteURI) {
		fmt.Fprintln(stdout, "(Opened it in your browser.) Waiting for approval...")
	} else {
		fmt.Fprintln(stdout, "Waiting for approval...")
	}
	r, err := f.Wait(ctx)
	if err != nil {
		switch {
		case errors.Is(err, login.ErrDenied):
			fmt.Fprintln(stderr, "Sign-in was refused in the console.")
		case errors.Is(err, login.ErrExpired), errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintf(stderr, "The code expired. Run %s login again.\n", config.Command())
		default:
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	printSignedIn(stdout, r, cfg)
	return 0
}

func printSignedIn(stdout io.Writer, r *login.Result, cfg *config.Config) {
	fmt.Fprintf(stdout, "Signed in (%s, stored in the %s). Passport %s: %s. Mode: %s.\n", r.Kind, login.StoreWords(r.Where), r.Agent, r.Passport, cfg.Mode)
	if config.InPlugin() {
		fmt.Fprintln(stdout, "The Claude Code plugin uses this sign-in from the next tool call. Check with /agentwatch:status.")
		return
	}
	cmd := config.Command()
	fmt.Fprintf(stdout, "Next: %s install all   then   %s doctor\n", cmd, cmd)
}
