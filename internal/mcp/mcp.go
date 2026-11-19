// Package mcp is `agentwatch mcp`: a stdio MCP server (newline-delimited
// JSON-RPC 2.0) that lets the coding agent read its own AgentWatch status,
// runs, findings and posture.
//
// Every tool is read-only. In particular the agent cannot change the sensor's
// mode: a tool that could switch enforce to dry_run would let the watched
// agent switch its own guard off. `agentwatch_mode` reports the mode and says
// how a person changes it.
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
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters"
	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/doctor"
	"github.com/chinmayNayar/agentwatch-plugins/internal/spool"
	"github.com/chinmayNayar/agentwatch-plugins/internal/state"
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
}

// Server holds the tool table and its dependencies.
type Server struct {
	NewClient func(string, string) *client.Client
	tools     []tool
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
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		return nil, cfg, err
	}
	return s.NewClient(cfg.APIURL, cred.Token), cfg, nil
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
		"See them in the console: %s%s (or sign this machine in with an account key: `agentwatch login --api-key`).",
		what, dashboardURL(cfg.APIURL), page)
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
	cfg := config.Load()
	var b strings.Builder
	fmt.Fprintf(&b, "Mode: %s (%s), %s; strict: %v; API: %s\n", cfg.Mode, cfg.Sources["mode"], cfg.FailMode, cfg.Strict, cfg.APIURL)
	if cred, err := config.LoadCredential(); err != nil {
		fmt.Fprintf(&b, "Signed in: no (%s)\n", err)
	} else {
		fmt.Fprintf(&b, "Signed in: yes (%s, stored in %s)\n", cred.Kind, cred.Source)
	}
	for _, c := range adapters.Clients {
		agent := cfg.AgentName(c)
		if p, age := state.LoadPolicy(agent); p != nil {
			fmt.Fprintf(&b, "%s: passport %s, isolated=%v, cached %s ago\n", agent, p.PolicyMode, p.Isolated, age.Round(time.Second))
		}
	}
	sp := spool.New(config.SpoolDir())
	fmt.Fprintf(&b, "Queued events: %d (dropped: %d)\n", sp.Pending(), sp.Dropped())
	return b.String(), nil
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
		return "No posture results for " + agent + " yet (run `agentwatch` for a while, then evaluate posture in the console).", nil
	}
	return "Posture for " + agent + ":\n" + strings.Join(lines, "\n"), nil
}

func (s *Server) mode(ctx context.Context, _ map[string]any) (string, error) {
	cfg := config.Load()
	return fmt.Sprintf("AgentWatch sensor mode: %s (%s), %s.\n"+
		"dry_run records what would be blocked and never blocks (except denied tools and self-protection).\n"+
		"enforce blocks denied, held and isolated calls.\n"+
		"Only a person can change this, in a terminal: `agentwatch mode enforce` (asks for confirmation), "+
		"`agentwatch mode dry_run`, or the Claude Code plugin's `mode` option in /config. "+
		"Agents are blocked from running those commands.", cfg.Mode, cfg.Sources["mode"], cfg.FailMode), nil
}

func (s *Server) doctor(ctx context.Context, _ map[string]any) (string, error) {
	return doctor.Text(doctor.Run(s.NewClient)), nil
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
