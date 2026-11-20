// Package status builds the one status report that `agentwatch status`, the
// MCP agentwatch_status tool and /agentwatch:status all show.
package status

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters"
	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/login"
	"github.com/chinmayNayar/agentwatch-plugins/internal/spool"
	"github.com/chinmayNayar/agentwatch-plugins/internal/state"
)

// Passport is the cached passport of one client's agent.
type Passport struct {
	Agent            string `json:"agent"`
	PolicyMode       string `json:"policy_mode"`
	Isolated         bool   `json:"isolated"`
	CachedSecondsAgo int    `json:"cached_seconds_ago"`
}

// Report is the machine-readable status (`agentwatch status --json`).
type Report struct {
	Version     string              `json:"version"`
	Mode        string              `json:"mode"`
	ModeSource  string              `json:"mode_source"`
	FailMode    string              `json:"fail_mode"`
	Strict      bool                `json:"strict"`
	APIURL      string              `json:"api_url"`
	Dashboard   string              `json:"dashboard"`
	SignedIn    bool                `json:"signed_in"`
	Credential  string              `json:"credential,omitempty"`
	DeviceID    string              `json:"device_id,omitempty"`
	SignInHint  string              `json:"sign_in_hint,omitempty"`
	LoginCode   string              `json:"login_pending_code,omitempty"`
	LoginURL    string              `json:"login_pending_url,omitempty"`
	Queued      int                 `json:"queued"`
	Dropped     int                 `json:"dropped"`
	Agents      map[string]Passport `json:"agents"`
	Command     string              `json:"command"`
	OnPath      bool                `json:"on_path"`
	InPlugin    bool                `json:"in_plugin"`
	LastHookAgo int                 `json:"last_hook_seconds_ago,omitempty"`
	ConfigError string              `json:"config_error,omitempty"`
}

// Build reads the local state. It makes no network calls.
func Build(dashboard func(string) string) Report {
	cfg := config.Load()
	cred, cerr := config.LoadCredential()
	sp := spool.New(config.SpoolDir())
	r := Report{
		Version: client.Version, Mode: cfg.Mode, ModeSource: describeSource(cfg.Sources["mode"]), FailMode: cfg.FailMode,
		Strict: cfg.Strict, APIURL: cfg.APIURL, Dashboard: dashboard(cfg.APIURL), SignedIn: cerr == nil,
		Queued: sp.Pending(), Dropped: sp.Dropped(), Agents: map[string]Passport{}, Command: config.Command(),
		InPlugin: config.InPlugin(),
	}
	r.OnPath = r.Command == "agentwatch"
	if cfg.LoadErr != nil {
		r.ConfigError = cfg.LoadErr.Error()
	}
	if cred != nil {
		r.Credential = cred.Kind + " in the " + cred.Source
		r.DeviceID = cred.DeviceID
	} else {
		r.SignInHint = config.SignInHint()
		if cerr != nil && cerr != config.ErrNoCredential {
			r.SignInHint = cerr.Error() + "; " + r.SignInHint
		}
		if p := login.LoadPending(); p != nil && processAlive(p.PID) {
			r.LoginCode, r.LoginURL = p.UserCode, p.CompleteURI
		}
	}
	if cfg.DeviceID != "" && r.DeviceID == "" {
		r.DeviceID = cfg.DeviceID
	}
	for _, c := range adapters.Clients {
		a := cfg.AgentName(c)
		if p, age := state.LoadPolicy(a); p != nil {
			r.Agents[c] = Passport{Agent: a, PolicyMode: p.PolicyMode, Isolated: p.Isolated, CachedSecondsAgo: int(age.Seconds())}
		}
	}
	if fi, err := os.Stat(filepath.Join(config.StateDir(), "active-agents.json")); err == nil {
		r.LastHookAgo = int(time.Since(fi.ModTime()).Seconds())
	}
	return r
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// describeSource turns a config source into words a person recognises.
func describeSource(src string) string {
	switch {
	case src == "" || src == "default":
		return "default"
	case strings.HasPrefix(src, "CLAUDE_PLUGIN_OPTION_"):
		return "Claude Code plugin option" + strings.TrimPrefix(src, "CLAUDE_PLUGIN_OPTION_MODE")
	case strings.HasPrefix(src, "config file"):
		return "~/.agentwatch/config.json" + strings.TrimPrefix(src, "config file")
	}
	return src
}

// Text renders the report for a person (terminal or the agent's answer).
func Text(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AgentWatch %s\n", r.Version)
	modeWhat := map[string]string{
		config.ModeDryRun:  "records what would be blocked; blocks only denied tools and self-protection",
		config.ModeEnforce: "blocks denied, held and isolated calls",
	}[r.Mode]
	fmt.Fprintf(&b, "Mode:       %s (%s): %s. %s, strict %s.\n", r.Mode, r.ModeSource, modeWhat, r.FailMode, onOff(r.Strict))
	if r.ConfigError != "" {
		fmt.Fprintf(&b, "Config:     PROBLEM: %s\n", r.ConfigError)
	}
	if r.SignedIn {
		fmt.Fprintf(&b, "Signed in:  yes (%s", r.Credential)
		if r.DeviceID != "" {
			fmt.Fprintf(&b, ", device %s", r.DeviceID)
		}
		b.WriteString(")\n")
	} else {
		fmt.Fprintf(&b, "Signed in:  no. To sign in, %s\n", r.SignInHint)
		if r.LoginCode != "" {
			fmt.Fprintf(&b, "            A sign-in is waiting for approval: code %s at %s\n", r.LoginCode, r.LoginURL)
		}
	}
	switch {
	case !r.SignedIn:
		b.WriteString("Passport:   sign in to load your passport.\n")
	case len(r.Agents) == 0:
		b.WriteString("Passport:   not loaded yet; it loads at the next session start or tool call.\n")
	default:
		keys := make([]string, 0, len(r.Agents))
		for k := range r.Agents {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			p := r.Agents[k]
			label := "Passport:  "
			if i > 0 {
				label = "           "
			}
			iso := ""
			if p.Isolated {
				iso = ", ISOLATED: every tool call is blocked"
			}
			fmt.Fprintf(&b, "%s %s: %s%s (cached %ds ago)\n", label, p.Agent, p.PolicyMode, iso, p.CachedSecondsAgo)
		}
	}
	queued := fmt.Sprintf("%d", r.Queued)
	if r.Queued > 0 && !r.SignedIn {
		queued += " (kept on this machine; sent after sign-in)"
	} else if r.Queued > 0 {
		queued += " (sending in the background)"
	}
	fmt.Fprintf(&b, "Queued:     %s; dropped %d\n", queued, r.Dropped)
	if r.LastHookAgo > 0 {
		fmt.Fprintf(&b, "Hooks:      running (last recorded %s ago)\n", (time.Duration(r.LastHookAgo) * time.Second).String())
	}
	fmt.Fprintf(&b, "API:        %s   console %s\n", r.APIURL, r.Dashboard)
	if r.OnPath {
		b.WriteString("CLI:        agentwatch (on PATH)\n")
	} else {
		fmt.Fprintf(&b, "CLI:        %s\n", r.Command)
		fmt.Fprintf(&b, "            not on PATH; to add it: %s link (puts agentwatch in ~/.local/bin)\n", r.Command)
	}
	return b.String()
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// LocalBinOnPath reports whether dir is on PATH.
func LocalBinOnPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}
