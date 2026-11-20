// Package login is the device-code sign-in shared by `agentwatch login` and
// the MCP server's agentwatch_login tool (/agentwatch:setup in Claude Code).
//
// The flow: ask the API for a code, show it and open the approval page, poll
// until a person approves it in the console, then check the token works and
// store it in the OS keychain (a 0600 file where there is no keychain).
//
// Nothing here takes an API URL or a key from its caller beyond the config:
// the agent can start a sign-in to the API this machine is configured for, and
// only a person signed in to that console can approve it.
package login

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/spool"
	"github.com/chinmayNayar/agentwatch-plugins/internal/state"
)

// Pending is what a person needs to approve a sign-in. It holds no secret:
// the user code is shown on screen, the device code stays in memory.
type Pending struct {
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	CompleteURI     string    `json:"verification_uri_complete"`
	APIURL          string    `json:"api_url"`
	ExpiresAt       time.Time `json:"expires_at"`
	PID             int       `json:"pid"`
}

// PendingPath is ~/.agentwatch/state/login-pending.json, written while a
// sign-in waits for approval (status shows it; tests read the code from it).
func PendingPath() string { return filepath.Join(config.StateDir(), "login-pending.json") }

// LoadPending returns the sign-in waiting for approval, if any is still valid.
func LoadPending() *Pending {
	b, err := os.ReadFile(PendingPath())
	if err != nil {
		return nil
	}
	var p Pending
	if json.Unmarshal(b, &p) != nil || time.Now().After(p.ExpiresAt) {
		return nil
	}
	return &p
}

func clearPending() { _ = os.Remove(PendingPath()) }

// Flow is one device sign-in in progress.
type Flow struct {
	api        *client.Client
	cfg        *config.Config
	deviceCode string
	interval   time.Duration
	Pending    Pending
}

// Start asks the API for a code. It does not open a browser (see OpenBrowser).
func Start(ctx context.Context, cfg *config.Config) (*Flow, error) {
	api := client.New(cfg.APIURL, "")
	dc, err := api.StartDeviceCode(ctx, cfg.DeviceName, cfg.UserLabel, config.OSArch())
	if err != nil {
		return nil, fmt.Errorf("could not start sign-in at %s: %w", cfg.APIURL, err)
	}
	f := &Flow{api: api, cfg: cfg, deviceCode: dc.DeviceCode, interval: time.Duration(dc.Interval) * time.Second}
	if f.interval < time.Second {
		f.interval = 5 * time.Second
	}
	exp := time.Duration(dc.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = 15 * time.Minute
	}
	f.Pending = Pending{UserCode: dc.UserCode, VerificationURI: dc.VerificationURI, CompleteURI: dc.VerificationURIComplete,
		APIURL: cfg.APIURL, ExpiresAt: time.Now().Add(exp), PID: os.Getpid()}
	if f.Pending.CompleteURI == "" {
		f.Pending.CompleteURI = f.Pending.VerificationURI
	}
	_ = os.MkdirAll(config.StateDir(), 0o700)
	if b, err := json.Marshal(f.Pending); err == nil {
		_ = config.WriteFileAtomic(PendingPath(), b, 0o600)
	}
	return f, nil
}

// Result of a finished sign-in.
type Result struct {
	Kind     string // device | api_key
	Where    string // keychain | file
	DeviceID string
	Agent    string
	Passport string // policy mode of the agent's passport
}

var (
	ErrDenied  = errors.New("the sign-in was refused in the console")
	ErrExpired = errors.New("the code expired before it was approved")
)

// Wait polls until the code is approved, refused or expired, or ctx ends
// (then it returns ctx.Err() and the flow can be waited on again).
func (f *Flow) Wait(ctx context.Context) (*Result, error) {
	for {
		if time.Now().After(f.Pending.ExpiresAt) {
			clearPending()
			return nil, ErrExpired
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.interval):
		}
		tok, err := f.api.PollDeviceToken(ctx, f.deviceCode)
		if err == nil {
			clearPending()
			return Save(ctx, f.cfg, tok.AccessToken, tok.DeviceID, tok.UserLabel)
		}
		var ae *client.Error
		if errors.As(err, &ae) {
			switch ae.Code {
			case "authorization_pending":
				continue
			case "slow_down":
				f.interval += 5 * time.Second
				continue
			case "access_denied":
				clearPending()
				return nil, ErrDenied
			case "expired_token", "invalid_grant":
				clearPending()
				return nil, ErrExpired
			}
		}
		if client.IsUnavailable(err) || ctx.Err() != nil {
			continue
		}
		return nil, fmt.Errorf("sign-in failed: %w", err)
	}
}

// Save checks the token against the API, stores it, writes the config file
// (keeping the person's own settings) and sends what was queued while the
// machine was signed out.
func Save(ctx context.Context, cfg *config.Config, token, deviceID, userLabel string) (*Result, error) {
	if token == "" {
		return nil, errors.New("no key given")
	}
	fileCfg := &config.Config{}
	if b, err := os.ReadFile(config.Path()); err == nil {
		_ = json.Unmarshal(b, fileCfg)
	}
	// The agent name uses the user label this sign-in will save, so the
	// passport cached here is the one the hooks look up.
	if fileCfg.UserLabel == "" && userLabel != "" {
		cfg.UserLabel = userLabel
	}
	api := client.New(cfg.APIURL, token)
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	agent := cfg.AgentName("claude-code")
	pol, err := api.Policy(vctx, agent)
	if err != nil {
		return nil, fmt.Errorf("that key was not accepted by %s: %w", cfg.APIURL, err)
	}
	state.SavePolicy(pol)
	kind := "api_key"
	if strings.HasPrefix(token, "awdv_") {
		kind = "device"
	}
	where, err := config.SaveCredential(&config.Credential{Token: token, Kind: kind, DeviceID: deviceID, APIURL: cfg.APIURL})
	if err != nil {
		return nil, fmt.Errorf("could not store the credential: %w", err)
	}
	if fileCfg.Mode == "" {
		fileCfg.Mode = config.ModeDryRun
	}
	if fileCfg.FailMode == "" {
		fileCfg.FailMode = config.FailOpen
	}
	if fileCfg.APIURL == "" {
		fileCfg.APIURL = cfg.APIURL
	}
	if deviceID != "" {
		fileCfg.DeviceID = deviceID
	}
	if userLabel != "" && fileCfg.UserLabel == "" {
		fileCfg.UserLabel = userLabel
	}
	if cfg.DeviceName != "" && fileCfg.DeviceName == "" {
		fileCfg.DeviceName = cfg.DeviceName
	}
	if err := fileCfg.Save(); err != nil {
		return nil, fmt.Errorf("could not save config: %w", err)
	}
	// Events queued while signed out go now, not at the next tool call.
	if exe, err := os.Executable(); err == nil && os.Getenv("AGENTWATCH_NO_FLUSH") != "1" {
		_ = spool.StartFlusher(exe)
	}
	return &Result{Kind: kind, Where: where, DeviceID: deviceID, Agent: pol.AgentName, Passport: pol.PolicyMode}, nil
}

// StoreWords names where a credential went, for people.
func StoreWords(where string) string {
	if where == "keychain" {
		return "OS keychain"
	}
	return "file " + config.CredsPath() + " (readable only by you; this machine has no usable keychain)"
}

// OpenBrowser opens the approval page. AGENTWATCH_NO_BROWSER=1 skips it.
func OpenBrowser(u string) bool {
	if u == "" || os.Getenv("AGENTWATCH_NO_BROWSER") == "1" {
		return false
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://localhost") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return false // only open the console's own page
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start() == nil
}
