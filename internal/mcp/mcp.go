// Package mcp is `agentwatch mcp`: a stdio MCP server (newline-delimited
// JSON-RPC 2.0) that lets the coding agent read its own AgentWatch status,
// runs, findings and posture.
//
// Every tool but one is read-only. In particular the agent cannot change the
// sensor's mode: a tool that could switch enforce to dry_run would let the
// watched agent switch its own guard off. `agentwatch_mode` reports the mode
// and says how a person changes it.
//
// The exception is agentwatch_login (/agentwatch:setup): it starts a device
// sign-in to the API this machine is configured for. Only a person signed in
// to that console can approve it, it takes no URL or key from the agent, and
// it refuses when the machine is already signed in, so the agent cannot swap
// this machine's identity or point it somewhere else.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters"
	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/doctor"
	"github.com/chinmayNayar/agentwatch-plugins/internal/login"
	"github.com/chinmayNayar/agentwatch-plugins/internal/status"
)

const protocolVersion = "2024-11-05"

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	run         func(ctx context.Context, args map[string]any) (string, error)
	timeout     time.Duration
}

// Server holds the tool table and its dependencies.
type Server struct {
	NewClient func(string, string) *client.Client
	tools     []tool

	// the sign-in in progress (agentwatch_login), one at a time
	mu      sync.Mutex
	flow    *login.Flow
	done    chan struct{}
	result  *login.Result
	loginEr error
}

func clientArg() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"client": map[string]any{"type": "string", "enum": adapters.Clients,
			"description": "Which coding agent's AgentWatch identity to look at. Default: claude-code."},
	}}
}

// New builds the server.
func New() *Server {
	s := &Server{NewClient: client.New}
	ro := map[string]any{"readOnlyHint": true, "openWorldHint": false}
	s.tools = []tool{
		{Name: "agentwatch_status", Description: "AgentWatch sensor status on this machine: mode, fail mode, sign-in, passports and queued events.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Annotations: ro, run: s.status},
		{Name: "agentwatch_runs", Description: "Recent AgentWatch runs (sessions) for this coding agent, newest first.",
			InputSchema: clientArg(), Annotations: ro, run: s.runs},
		{Name: "agentwatch_findings", Description: "Recent AgentWatch security findings for this coding agent (runtime detections, blocked calls, sensor events).",
			InputSchema: clientArg(), Annotations: ro, run: s.findings},
		{Name: "agentwatch_posture", Description: "AgentWatch posture checks (AW-P01..P12) for this coding agent, in plain English.",
			InputSchema: clientArg(), Annotations: ro, run: s.posture},
		{Name: "agentwatch_mode", Description: "The sensor's mode (dry_run or enforce) and how a person changes it. Read-only: the agent cannot change its own mode.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Annotations: ro, run: s.mode},
		{Name: "agentwatch_doctor", Description: "Check this machine's AgentWatch setup per client (hooks, sign-in, API reachability).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Annotations: ro, run: s.doctor},
		{Name: "agentwatch_login", Description: "Sign this machine in to AgentWatch with a device code (used by /agentwatch:setup). " +
			"The first call starts the sign-in, opens the approval page in the browser and returns the code to show the person. " +
			"Call again with wait=true to wait (up to 45 s per call) until the person approves it; the token is then stored in the OS keychain. " +
			"Refuses if the machine is already signed in.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"wait": map[string]any{"type": "boolean", "description": "Wait for the person to approve the sign-in already started."},
			}},
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
			run:         s.login, timeout: 55 * time.Second},
	}
	return s
}

// Serve reads requests from r until EOF and writes responses to w.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcErr{-32700, "parse error"}})
			continue
		}
		res, rerr := s.handle(req)
		if len(req.ID) == 0 { // notification
			continue
		}
		out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			out["error"] = rerr
		} else {
			out["result"] = res
		}
		if err := enc.Encode(out); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) handle(req rpcReq) (any, *rpcErr) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agentwatch", "version": client.Version},
		}, nil
	case "notifications/initialized", "initialized":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcErr{-32602, "invalid params"}
		}
		for _, t := range s.tools {
			if t.Name == p.Name {
				d := t.timeout
				if d == 0 {
					d = 10 * time.Second
				}
				ctx, cancel := context.WithTimeout(context.Background(), d)
				defer cancel()
				text, err := t.run(ctx, p.Arguments)
				if err != nil {
					return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}, nil
				}
				return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}, nil
			}
		}
		return nil, &rpcErr{-32601, "unknown tool " + p.Name}
	}
	return nil, &rpcErr{-32601, "method not found: " + req.Method}
}

func (s *Server) api() (*client.Client, *config.Config, error) {
	cfg := config.Load()
	cred, err := config.LoadCredential()
	if err != nil {
		return nil, cfg, fmt.Errorf("this machine is %v to AgentWatch yet. To sign in, %s", signedInWords(err), config.SignInHint())
	}
	return s.NewClient(cfg.APIURL, cred.Token), cfg, nil
}

func signedInWords(err error) string {
	if err == config.ErrNoCredential {
		return "not signed in"
	}
	return "not signed in (" + err.Error() + ")"
}

// deviceCredential: the sensor signed in with a device token, which reads only
// this device's runs and policy (the API answers 403 device_scope otherwise).
func deviceCredential() bool {
	c, err := config.LoadCredential()
	return err == nil && c.Kind == "device"
}

// accountOnly explains a read a device token may not make, and where to find it.
func accountOnly(cfg *config.Config, what, page string) string {
	return fmt.Sprintf("%s are tenant-wide, and this machine is signed in with a device token, which reads only its own runs and policy. "+
		"See them in the console: %s%s (or sign this machine in with an account key: `%s login --api-key` in a terminal).",
		what, dashboardURL(cfg.APIURL), page, config.Command())
}

func isDeviceScope(err error) bool {
	var e *client.Error
	return errors.As(err, &e) && e.Status == 403 && strings.Contains(e.Body, "device_scope")
}

func agentFor(cfg *config.Config, args map[string]any) string {
	c, _ := args["client"].(string)
	if !adapters.Known(c) {
		c = "claude-code"
	}
	return cfg.AgentName(c)
}

func (s *Server) status(ctx context.Context, _ map[string]any) (string, error) {
	return status.Text(status.Build(dashboardURL)), nil
}

func (s *Server) runs(ctx context.Context, args map[string]any) (string, error) {
	api, cfg, err := s.api()
	if err != nil {
		return "", err
	}
	agent := agentFor(cfg, args)
	var out struct {
		Runs []map[string]any `json:"runs"`
	}
	var raw any
	path := "/runs?limit=10&agent_name=" + url.QueryEscape(agent)
	if deviceCredential() {
		path = "/devices/runs?limit=10&agent_name=" + url.QueryEscape(agent) // this device's runs only
	}
	if err := api.Do(ctx, "GET", path, nil, &raw); err != nil {
		return "", err
	}
	switch t := raw.(type) {
	case map[string]any:
		rs, ok := t["items"].([]any)
		if !ok {
			rs, ok = t["runs"].([]any)
		}
		if ok {
			for _, r := range rs {
				if m, ok := r.(map[string]any); ok {
					out.Runs = append(out.Runs, m)
				}
			}
		}
	case []any:
		for _, r := range t {
			if m, ok := r.(map[string]any); ok {
				out.Runs = append(out.Runs, m)
			}
		}
	}
	if len(out.Runs) == 0 {
		return "No runs for " + agent + " yet.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Recent runs for %s:\n", agent)
	for _, r := range out.Runs {
		fmt.Fprintf(&b, "- %v  %v  steps=%v  started %v  %s/runs/%v\n", r["id"], r["status"], r["total_steps"], r["started_at"],
			dashboardURL(cfg.APIURL), r["id"])
	}
	return b.String(), nil
}

func (s *Server) findings(ctx context.Context, args map[string]any) (string, error) {
	api, cfg, err := s.api()
	if err != nil {
		return "", err
	}
	agent := agentFor(cfg, args)
	if deviceCredential() {
		return accountOnly(cfg, "Findings", "/findings"), nil
	}
	var evs []map[string]any
	if err := api.Do(ctx, "GET", "/security/events?limit=200", nil, &evs); err != nil {
		if isDeviceScope(err) {
			return accountOnly(cfg, "Findings", "/findings"), nil
		}
		return "", err
	}
	var b strings.Builder
	n := 0
	for _, e := range evs {
		if e["agent_name"] != agent {
			continue
		}
		n++
		if n > 20 {
			break
		}
		fmt.Fprintf(&b, "- [%v] %v at %v: %v\n", e["severity"], e["event_type"], e["created_at"], summary(e))
	}
	if n == 0 {
		return "No findings for " + agent + ".", nil
	}
	return "Findings for " + agent + ":\n" + b.String(), nil
}

func summary(e map[string]any) string {
	if ev, ok := e["evidence"].(map[string]any); ok {
		if s, ok := ev["summary"].(string); ok {
			return s
		}
	}
	return ""
}

func (s *Server) posture(ctx context.Context, args map[string]any) (string, error) {
	api, cfg, err := s.api()
	if err != nil {
		return "", err
	}
	agent := agentFor(cfg, args)
	if deviceCredential() {
		return accountOnly(cfg, "Posture results", "/posture"), nil
	}
	var raw map[string]any
	if err := api.Do(ctx, "GET", "/posture/agents/"+url.PathEscape(agent), nil, &raw); err != nil {
		if isDeviceScope(err) {
			return accountOnly(cfg, "Posture results", "/posture"), nil
		}
		return "", err
	}
	items, _ := raw["items"].([]any)
	if items == nil {
		items, _ = raw["checks"].([]any)
	}
	var lines []string
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %v %v: %v. %v", m["check_id"], m["title"], m["status"], m["plain"]))
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return "No posture results for " + agent + " yet. They appear after AgentWatch has seen some sessions and posture is evaluated in the console: " +
			dashboardURL(cfg.APIURL) + "/posture", nil
	}
	return "Posture for " + agent + ":\n" + strings.Join(lines, "\n"), nil
}

func (s *Server) mode(ctx context.Context, _ map[string]any) (string, error) {
	cfg := config.Load()
	cmd := config.Command()
	return fmt.Sprintf("AgentWatch sensor mode: %s (set by: %s), %s.\n"+
		"dry_run records what would be blocked and never blocks (except denied tools and self-protection).\n"+
		"enforce blocks denied, held and isolated calls.\n"+
		"Only a person can change this, in a terminal: `%s mode enforce` (asks for confirmation), "+
		"`%s mode dry_run`, or the Claude Code plugin's `mode` option (/plugin, then agentwatch, then configure). "+
		"Agents are blocked from running those commands.", cfg.Mode, cfg.Sources["mode"], cfg.FailMode, cmd, cmd), nil
}

func (s *Server) doctor(ctx context.Context, _ map[string]any) (string, error) {
	return doctor.Text(doctor.Run(s.NewClient)), nil
}

// login runs the device sign-in for /agentwatch:setup. The first call starts
// it and returns at once with the code (so the person sees it); later calls
// with wait=true block up to 45 s for the approval.
func (s *Server) login(ctx context.Context, args map[string]any) (string, error) {
	cfg := config.Load()
	if cred, err := config.LoadCredential(); err == nil {
		dev := ""
		if cred.DeviceID != "" {
			dev = ", device " + cred.DeviceID
		}
		return fmt.Sprintf("Already signed in (%s in the %s%s). Nothing to do.\n"+
			"To sign in as someone else, a person first runs `%s logout` in a terminal.", cred.Kind, cred.Source, dev, config.Command()), nil
	}
	wait, _ := args["wait"].(bool)

	s.mu.Lock()
	if s.flow == nil {
		f, err := login.Start(ctx, cfg)
		if err != nil {
			s.mu.Unlock()
			return "", err
		}
		done := make(chan struct{})
		s.flow, s.done, s.result, s.loginEr = f, done, nil, nil
		go func() {
			wctx, cancel := context.WithDeadline(context.Background(), f.Pending.ExpiresAt.Add(30*time.Second))
			defer cancel()
			r, err := f.Wait(wctx)
			s.mu.Lock()
			s.result, s.loginEr = r, err
			s.mu.Unlock()
			close(done)
		}()
		opened := login.OpenBrowser(f.Pending.CompleteURI)
		s.mu.Unlock()
		return startedText(f.Pending, opened), nil
	}
	f, done := s.flow, s.done
	s.mu.Unlock()

	if wait {
		t := time.NewTimer(45 * time.Second)
		defer t.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-t.C:
		}
	}
	select {
	case <-done:
		s.mu.Lock()
		r, err := s.result, s.loginEr
		s.flow = nil
		s.mu.Unlock()
		if err != nil {
			return "", fmt.Errorf("the sign-in did not complete: %v. Call agentwatch_login again for a new code", err)
		}
		return signedInText(r, cfg), nil
	default:
		left := time.Until(f.Pending.ExpiresAt).Round(time.Minute)
		return fmt.Sprintf("Still waiting for approval. Code: %s  Page: %s  (the code expires in %s). "+
			"Call agentwatch_login with wait=true to keep waiting.", f.Pending.UserCode, f.Pending.CompleteURI, left), nil
	}
}

func startedText(p login.Pending, opened bool) string {
	var b strings.Builder
	if opened {
		b.WriteString("Opened the AgentWatch approval page in the browser.\n")
	} else {
		b.WriteString("Open the AgentWatch approval page in a browser.\n")
	}
	fmt.Fprintf(&b, "Page: %s\nCode: %s\n", p.CompleteURI, p.UserCode)
	fmt.Fprintf(&b, "Sign in to the console if asked, check that the page shows the code %s, and approve it. "+
		"The code expires at %s.\n", p.UserCode, p.ExpiresAt.Local().Format("15:04"))
	b.WriteString("Next: call agentwatch_login with wait=true to wait for the approval.")
	return b.String()
}

func signedInText(r *login.Result, cfg *config.Config) string {
	dev := ""
	if r.DeviceID != "" {
		dev = " as device " + r.DeviceID
	}
	return fmt.Sprintf("Signed in%s. The token is stored in the %s.\n"+
		"Passport %s: %s. Mode: %s (%s).\n"+
		"Events queued while signed out are being sent now. Runs appear at %s/runs.",
		dev, login.StoreWords(r.Where), r.Agent, r.Passport, cfg.Mode, cfg.Sources["mode"], dashboardURL(cfg.APIURL))
}

// dashboardURL maps api.<host> to app.<host>; localhost:8000 to :3000.
func dashboardURL(api string) string {
	u, err := url.Parse(api)
	if err != nil {
		return api
	}
	switch {
	case strings.HasPrefix(u.Host, "api."):
		u.Host = "app." + strings.TrimPrefix(u.Host, "api.")
	case u.Port() == "8000":
		u.Host = u.Hostname() + ":3000"
	}
	u.Path = ""
	return strings.TrimRight(u.String(), "/")
}

// DashboardURL is exported for the CLI.
func DashboardURL(api string) string { return dashboardURL(api) }
