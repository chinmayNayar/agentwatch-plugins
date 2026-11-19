// Package config holds the sensor's settings, its on-disk layout and the
// precedence rules between the config file, environment and client options.
//
// Precedence (highest first):
//  1. Claude Code plugin options (CLAUDE_PLUGIN_OPTION_<KEY>), set by the user in /config
//  2. AGENTWATCH_* environment variables
//  3. ~/.agentwatch/config.json
//  4. defaults: dry_run, fail_open, 800 ms preflight timeout
//
// A config file that cannot be read never locks the developer out: the
// defaults (dry_run) apply and `agentwatch doctor` reports the problem.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	ModeDryRun  = "dry_run"
	ModeEnforce = "enforce"
	FailOpen    = "fail_open"
	FailClosed  = "fail_closed"

	DefaultAPIURL           = "https://api.agentwatch.in"
	DefaultPreflightTimeout = 800 * time.Millisecond
)

// Config is the persisted sensor configuration.
type Config struct {
	APIURL             string `json:"api_url,omitempty"`
	Mode               string `json:"mode,omitempty"`
	FailMode           string `json:"fail_mode,omitempty"`
	Strict             bool   `json:"strict,omitempty"`
	PreflightTimeoutMS int    `json:"preflight_timeout_ms,omitempty"`
	UserLabel          string `json:"user_label,omitempty"`
	DeviceName         string `json:"device_name,omitempty"`
	DeviceID           string `json:"device_id,omitempty"`
	// RepoNames: "hashed" (default) or "plain".
	RepoNames string `json:"repo_names,omitempty"`
	// ContentCapture sends prompt text and tool output (redacted). Requires Approver.
	ContentCapture bool   `json:"content_capture,omitempty"`
	Approver       string `json:"approver,omitempty"`
	// SelfProtect defaults to true; only the config file (which the agent
	// cannot write) can turn it off.
	SelfProtect *bool `json:"self_protect,omitempty"`
	// AgentNames overrides the "<client>@<user>" name per client, e.g.
	// {"cursor": "cursor"} keeps a pre-sensor agent name.
	AgentNames map[string]string `json:"agent_names,omitempty"`

	// Not persisted: where each value came from, for `doctor` and `status`.
	Sources           map[string]string `json:"-"`
	agentNameOverride string
	LoadErr           error `json:"-"`
	FromFile          bool  `json:"-"`
}

// Home is the sensor's state root (~/.agentwatch, or AGENTWATCH_HOME).
func Home() string {
	if h := os.Getenv("AGENTWATCH_HOME"); h != "" {
		return h
	}
	return filepath.Join(UserHome(), ".agentwatch")
}

// UserHome is the user's home directory (HOME / USERPROFILE).
func UserHome() string {
	if h := os.Getenv("AGENTWATCH_USER_HOME"); h != "" { // tests
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}

func Path() string         { return filepath.Join(Home(), "config.json") }
func StateDir() string     { return filepath.Join(Home(), "state") }
func SpoolDir() string     { return filepath.Join(Home(), "spool") }
func BinDir() string       { return filepath.Join(Home(), "bin") }
func CredsPath() string    { return filepath.Join(Home(), "credentials") }
func LastGoodPath() string { return filepath.Join(StateDir(), "last-good-mode.json") }

// Load reads the config file, then applies env and plugin-option overrides.
func Load() *Config {
	c := &Config{Sources: map[string]string{}}
	b, err := os.ReadFile(Path())
	switch {
	case err == nil:
		var fc Config
		if jerr := json.Unmarshal(b, &fc); jerr != nil {
			c.LoadErr = fmt.Errorf("config.json is not valid JSON: %w", jerr)
			c.applyLastGood()
		} else {
			fc.Sources = c.Sources
			*c = fc
			c.Sources = map[string]string{}
			c.FromFile = true
			for _, k := range []string{"api_url", "mode", "fail_mode"} {
				c.Sources[k] = "config file"
			}
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		c.LoadErr = fmt.Errorf("config.json unreadable: %w", err)
		c.applyLastGood()
	}
	c.applyEnv()
	c.normalise()
	return c
}

// applyLastGood restores the mode/fail mode last written successfully, so a
// corrupted config does not silently downgrade enforce to dry_run.
func (c *Config) applyLastGood() {
	b, err := os.ReadFile(LastGoodPath())
	if err != nil {
		return
	}
	var lg struct{ Mode, FailMode, APIURL string }
	if json.Unmarshal(b, &lg) == nil {
		c.Mode, c.FailMode, c.APIURL = lg.Mode, lg.FailMode, lg.APIURL
		c.Sources["mode"] = "last good state (config.json is broken)"
	}
}

func firstEnv(names ...string) (string, string) {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v, n
		}
	}
	return "", ""
}

func (c *Config) applyEnv() {
	if v, n := firstEnv("CLAUDE_PLUGIN_OPTION_API_URL", "AGENTWATCH_API_URL"); v != "" {
		c.APIURL, c.Sources["api_url"] = v, n
	}
	if v, n := firstEnv("CLAUDE_PLUGIN_OPTION_MODE", "AGENTWATCH_MODE"); v != "" {
		c.Mode, c.Sources["mode"] = v, n
	}
	if v, n := firstEnv("CLAUDE_PLUGIN_OPTION_FAIL_MODE", "AGENTWATCH_FAIL_MODE"); v != "" {
		c.FailMode, c.Sources["fail_mode"] = v, n
	}
	if v, _ := firstEnv("AGENTWATCH_AGENT_NAME"); v != "" {
		c.agentNameOverride = v
	}
	if v, _ := firstEnv("AGENTWATCH_CONTENT_CAPTURE"); v == "1" || strings.EqualFold(v, "true") {
		c.ContentCapture = true
	}
	if v, _ := firstEnv("AGENTWATCH_APPROVER"); v != "" {
		c.Approver = v
	}
	if v, _ := firstEnv("AGENTWATCH_STRICT"); v != "" {
		c.Strict = v == "1" || strings.EqualFold(v, "true")
	}
	if v, _ := firstEnv("AGENTWATCH_PREFLIGHT_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.PreflightTimeoutMS = n
		}
	}
}

func (c *Config) normalise() {
	if c.Sources == nil {
		c.Sources = map[string]string{}
	}
	if c.APIURL == "" {
		c.APIURL = DefaultAPIURL
		c.Sources["api_url"] = "default"
	}
	c.APIURL = strings.TrimRight(c.APIURL, "/")
	if c.Mode != ModeEnforce {
		if c.Mode != "" && c.Mode != ModeDryRun {
			c.Sources["mode"] += " (unknown value; using dry_run)"
		}
		c.Mode = ModeDryRun
	}
	if c.FailMode != FailClosed {
		c.FailMode = FailOpen
	}
	if c.PreflightTimeoutMS <= 0 || c.PreflightTimeoutMS > 10000 {
		c.PreflightTimeoutMS = int(DefaultPreflightTimeout / time.Millisecond)
	}
	if c.RepoNames != "plain" {
		c.RepoNames = "hashed"
	}
	if c.UserLabel == "" {
		c.UserLabel = OSUser()
	}
	if c.DeviceName == "" {
		h, _ := os.Hostname()
		c.DeviceName = firstNonEmpty(h, "device")
	}
}

// ValidAPIURL rejects anything but https, except loopback http for local dev.
func ValidAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("not a URL: %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	h := u.Hostname()
	if u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1") {
		return nil
	}
	return fmt.Errorf("api_url must be https (http only for localhost): %q", raw)
}

func (c *Config) Timeout() time.Duration {
	return time.Duration(c.PreflightTimeoutMS) * time.Millisecond
}

// SelfProtectOn reports whether self-protection is active (default on).
func (c *Config) SelfProtectOn() bool { return c.SelfProtect == nil || *c.SelfProtect }

// Save writes the config file atomically (0600) and the last-good copy.
func (c *Config) Save() error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(Path(), append(b, '\n'), 0o600); err != nil {
		return err
	}
	_ = os.MkdirAll(StateDir(), 0o700)
	lg, _ := json.Marshal(map[string]string{"Mode": c.Mode, "FailMode": c.FailMode, "APIURL": c.APIURL})
	return WriteFileAtomic(LastGoodPath(), lg, 0o600)
}

// WriteFileAtomic writes via a temp file in the same directory and renames it.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// OSUser is the login name, reduced to characters valid in an agent name.
func OSUser() string {
	u := firstNonEmpty(os.Getenv("USER"), os.Getenv("USERNAME"), os.Getenv("LOGNAME"), "user")
	var b strings.Builder
	for _, r := range strings.ToLower(u) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// AgentName is "<client>@<user>" unless overridden in agent_names.
func (c *Config) AgentName(client string) string {
	if c.agentNameOverride != "" { // AGENTWATCH_AGENT_NAME, e.g. a repo keeping its old agent name
		return c.agentNameOverride
	}
	if n := c.AgentNames[client]; n != "" {
		return n
	}
	return client + "@" + c.UserLabel
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// OSArch is "<os>-<arch>" in the release naming (darwin-arm64, windows-x64).
func OSArch() string {
	a := runtime.GOARCH
	if a == "amd64" {
		a = "x64"
	}
	return runtime.GOOS + "-" + a
}
