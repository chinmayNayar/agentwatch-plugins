// Package doctor checks a machine's AgentWatch setup and says, per client,
// what works and what does not.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters"
	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/install"
	"github.com/chinmayNayar/agentwatch-plugins/internal/spool"
)

// Check is one line of the report.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail"`
}

// ClientReport groups checks per client.
type ClientReport struct {
	Client   string  `json:"client"`
	Detected bool    `json:"detected"`
	Status   string  `json:"status"` // green | red | not_installed
	Checks   []Check `json:"checks"`
}

// Report is the whole doctor output.
type Report struct {
	Sensor  []Check        `json:"sensor"`
	Clients []ClientReport `json:"clients"`
	OK      bool           `json:"ok"`
}

// Run builds the report. newClient is injectable for tests.
func Run(newClient func(string, string) *client.Client) Report {
	if newClient == nil {
		newClient = client.New
	}
	cfg := config.Load()
	r := Report{OK: true}
	add := func(c Check) {
		r.Sensor = append(r.Sensor, c)
		if !c.OK && !c.Warn {
			r.OK = false
		}
	}
	add(Check{Name: "version", OK: true, Detail: client.Version + " " + config.OSArch()})
	if cfg.LoadErr != nil {
		add(Check{Name: "config", OK: false, Detail: cfg.LoadErr.Error() + " (using " + cfg.Mode + ")"})
	} else {
		add(Check{Name: "config", OK: true, Detail: fmt.Sprintf("mode %s (%s), %s, preflight timeout %d ms, api %s",
			cfg.Mode, cfg.Sources["mode"], cfg.FailMode, cfg.PreflightTimeoutMS, cfg.APIURL)})
	}
	if err := config.ValidAPIURL(cfg.APIURL); err != nil {
		add(Check{Name: "api_url", OK: false, Detail: err.Error()})
	}
	cred, cerr := config.LoadCredential()
	if cerr != nil {
		add(Check{Name: "credentials", OK: false, Detail: cerr.Error() + ". To sign in, " + config.SignInHint()})
	} else {
		add(Check{Name: "credentials", OK: true, Detail: cred.Kind + " in " + cred.Source})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	api := newClient(cfg.APIURL, "")
	t0 := time.Now()
	if err := api.Do(ctx, "GET", "/health", nil, nil); err != nil {
		add(Check{Name: "api", OK: false, Detail: "cannot reach " + cfg.APIURL + ": " + err.Error()})
	} else {
		add(Check{Name: "api", OK: true, Detail: fmt.Sprintf("%s reachable in %d ms", cfg.APIURL, time.Since(t0).Milliseconds())})
	}
	if cred != nil {
		api.Token = cred.Token
		if p, err := api.Policy(ctx, cfg.AgentName("claude-code")); err != nil {
			d := "credentials rejected or no access: " + err.Error()
			if st := client.StatusOf(err); st == 401 || st == 403 {
				d += ". The device may have been revoked in the console: sign in again (" + config.SignInHint() + ")"
			}
			add(Check{Name: "auth", OK: false, Detail: d})
		} else {
			add(Check{Name: "auth", OK: true, Detail: fmt.Sprintf("signed in; passport %s is %s%s", p.AgentName, p.PolicyMode,
				map[bool]string{true: ", ISOLATED", false: ""}[p.Isolated])})
		}
	}
	sp := spool.New(config.SpoolDir())
	pending, dropped := sp.Pending(), sp.Dropped()
	add(Check{Name: "spool", OK: true, Warn: pending > 1000 || dropped > 0,
		Detail: fmt.Sprintf("%d queued, %d dropped", pending, dropped)})
	if exe, err := os.Executable(); err == nil && !strings.HasPrefix(exe, config.BinDir()) && !config.InPlugin() {
		add(Check{Name: "binary", OK: true, Warn: true, Detail: "running from " + exe + "; installs copy it to " + config.BinDir()})
	}
	if cmd := config.Command(); cmd == "agentwatch" {
		add(Check{Name: "cli", OK: true, Detail: "agentwatch is on PATH"})
	} else {
		add(Check{Name: "cli", OK: true, Warn: true, Detail: "agentwatch is not on PATH; run it as " + cmd +
			", or add it to PATH with: " + cmd + " link"})
	}

	for _, c := range adapters.Clients {
		cr := ClientReport{Client: c, Detected: detected(c)}
		st := install.Check(c)
		switch {
		case st.HooksOK:
			cr.Status = "green"
			cr.Checks = append(cr.Checks, Check{Name: "hooks", OK: true, Detail: st.Path})
		case st.Installed:
			cr.Status = "red"
			cr.Checks = append(cr.Checks, Check{Name: "hooks", OK: false, Detail: st.Problem})
			r.OK = false
		default:
			cr.Status = "not_installed"
			d := "not installed (agentwatch install " + c + ")"
			if c == "claude-code" && pluginInstalled() {
				cr.Status = "green"
				d = "Claude Code plugin agentwatch@agentwatch is installed"
			}
			cr.Checks = append(cr.Checks, Check{Name: "hooks", OK: cr.Status == "green", Warn: cr.Status != "green", Detail: d})
		}
		if c == "codex" && st.HooksOK {
			cr.Checks = append(cr.Checks, Check{Name: "trust", OK: true, Warn: true,
				Detail: "Codex runs changed hooks only after you trust them in Codex; doctor cannot see that state"})
		}
		r.Clients = append(r.Clients, cr)
	}
	return r
}

func detected(c string) bool {
	home := config.UserHome()
	switch c {
	case "claude-code":
		return exists(filepath.Join(home, ".claude")) || onPath("claude")
	case "cursor":
		return exists(filepath.Join(home, ".cursor")) || onPath("cursor") || onPath("cursor-agent")
	case "codex":
		return exists(filepath.Join(home, ".codex")) || onPath("codex")
	case "copilot":
		return exists(filepath.Join(home, ".copilot")) || onPath("copilot") || exists(filepath.Join(home, ".vscode"))
	}
	return false
}

func pluginInstalled() bool {
	b, err := os.ReadFile(filepath.Join(config.UserHome(), ".claude", "plugins", "installed_plugins.json"))
	return err == nil && strings.Contains(string(b), "agentwatch@agentwatch")
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func onPath(name string) bool {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// Text renders the report for a terminal.
func Text(r Report) string {
	var b strings.Builder
	mark := func(c Check) string {
		switch {
		case c.OK && !c.Warn:
			return "✓"
		case c.Warn:
			return "!"
		}
		return "✗"
	}
	b.WriteString("AgentWatch sensor\n")
	for _, c := range r.Sensor {
		fmt.Fprintf(&b, "  %s %-12s %s\n", mark(c), c.Name, c.Detail)
	}
	b.WriteString("\nClients\n")
	for _, cr := range r.Clients {
		icon := map[string]string{"green": "●", "red": "✗", "not_installed": "○"}[cr.Status]
		det := ""
		if cr.Detected {
			det = " (found on this machine)"
		}
		fmt.Fprintf(&b, "  %s %s%s\n", icon, cr.Client, det)
		for _, c := range cr.Checks {
			fmt.Fprintf(&b, "      %s %s\n", mark(c), c.Detail)
		}
	}
	if r.OK {
		b.WriteString("\nAll required checks passed.\n")
	} else {
		b.WriteString("\nSome checks failed (✗).\n")
	}
	return b.String()
}
