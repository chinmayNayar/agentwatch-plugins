// Package hook is `agentwatch hook <client> [event]`: read one payload from
// stdin, decide, spool the report, print the client's answer, exit 0.
//
// Invariants (each has a test):
//   - exit status is always 0, whatever happens (Copilot denies on non-zero);
//   - Cursor permission events always get valid JSON;
//   - nothing secret is printed or spooled (redact runs before the spool);
//   - a panic ends in the fail-mode answer, never a crash;
//   - reporting never waits on the network.
package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/chinmayNayar/agentwatch-plugins/internal/lazyre"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters"
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/cursor"
	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
	"github.com/chinmayNayar/agentwatch-plugins/internal/policy"
	"github.com/chinmayNayar/agentwatch-plugins/internal/redact"
	"github.com/chinmayNayar/agentwatch-plugins/internal/spool"
	"github.com/chinmayNayar/agentwatch-plugins/internal/state"
)

const maxStdin = 8 << 20

// PolicyMaxAge is how old a cached passport may be before a refresh.
var PolicyMaxAge = 5 * time.Second

// Options are injected by main (and by tests).
type Options struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Exe        string // path of this binary, for the detached flusher
	NoFlush    bool   // tests: never start a flusher
	Now        func() time.Time
	ParentArgs func() []string // AW-D05 launch-flag detection; nil = read the process table
	NewClient  func(baseURL, token string) *client.Client
}

// Run handles one hook invocation. It returns the process exit code (always 0).
func Run(args []string, o Options) (code int) {
	declared := ""
	hookEvent := ""
	if len(args) > 0 {
		declared = args[0]
	}
	if len(args) > 1 {
		hookEvent = args[1]
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewClient == nil {
		o.NewClient = client.New
	}
	cfg := config.Load()
	var out []byte
	var ev *event.Event
	defer func() {
		if r := recover(); r != nil {
			// Never crash the client. Enforce + fail_closed blocks a gated call.
			he := hookEvent
			cl := declared
			if ev != nil {
				he, cl = ev.HookEvent, ev.Client
			}
			if cfg.Mode == config.ModeEnforce && cfg.FailMode == config.FailClosed && (ev == nil || ev.CanBlock) {
				out = adapters.FailClosed(cl, he, "AgentWatch sensor error; fail-closed is on, so the call is blocked.")
			} else {
				out = adapters.Neutral(cl, he)
			}
			writeOut(o.Stdout, out)
			code = 0
		}
	}()

	raw, err := readPayload(o.Stdin)
	if err != nil || raw == nil {
		writeOut(o.Stdout, adapters.Neutral(declared, hookEvent))
		return 0
	}
	cl := adapters.Detect(declared, raw)
	if cl != declared && adapters.Known(declared) && state.IsInstalled(cl) {
		// e.g. Cursor running ~/.claude/settings.json hooks while the Cursor
		// hooks are installed too: let the Cursor install report it once.
		writeOut(o.Stdout, adapters.Neutral(cl, firstNonEmpty(hookEvent, strField(raw, "hook_event_name"))))
		return 0
	}
	ev, err = adapters.Parse(cl, raw, hookEvent)
	if err != nil {
		writeOut(o.Stdout, adapters.Neutral(declared, hookEvent))
		return 0
	}
	e := &engine{cfg: cfg, o: o, ev: ev, agent: cfg.AgentName(ev.Client), sp: spool.New(config.SpoolDir())}
	if ev.Client == cursor.Name && ev.ClientVersion != "" {
		e.clientVersion = ev.ClientVersion
	}
	out = e.handle()
	writeOut(o.Stdout, out)
	return 0
}

func strField(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func readPayload(r io.Reader) (map[string]any, error) {
	if r == nil {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r, maxStdin))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeOut(w io.Writer, b []byte) {
	if w == nil || len(b) == 0 {
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

type engine struct {
	cfg           *config.Config
	o             Options
	ev            *event.Event
	agent         string
	sp            *spool.Spool
	cred          *config.Credential
	credOnce      sync.Once
	clientVersion string
	spooled       bool
	timings       map[string]int64 // phase -> µs, for AGENTWATCH_DEBUG_LOG
}

func (e *engine) mark(phase string, since time.Time) {
	if e.timings == nil {
		e.timings = map[string]int64{}
	}
	e.timings[phase] = time.Since(since).Microseconds()
}

func (e *engine) credential() *config.Credential {
	// sync.Once: session start loads it from two goroutines at once.
	e.credOnce.Do(func() { e.cred, _ = config.LoadCredential() })
	return e.cred
}

func (e *engine) api() *client.Client {
	tok := ""
	if c := e.credential(); c != nil {
		tok = c.Token
	}
	return e.o.NewClient(e.cfg.APIURL, tok)
}

func (e *engine) handle() []byte {
	ev := e.ev
	if ev.SessionID == "" {
		ev.SessionID = "no-session-" + time.Now().Format("20060102")
	}
	state.MarkActive(e.agent)
	defer e.maybeFlush()

	switch ev.Kind {
	case event.SessionStart:
		e.sessionStart()
		if ev.Client == "claude-code" && e.credential() == nil {
			// Say it once per session, where the person sees it. The sensor
			// still runs: it queues events and applies local rules.
			return common.Marshal(map[string]any{"systemMessage": NotSignedInMessage()})
		}
	case event.Prompt:
		e.checkBypass()
		e.report("user_prompt", map[string]any{"prompt": e.textField(ev.Prompt)}, nil, "")
	case event.PreTool:
		return e.preTool()
	case event.PostTool, event.PostToolFailure:
		e.postTool()
	case event.Response, event.Stop, event.SubagentStop:
		typ := map[string]string{event.Response: "llm_call", event.Stop: "turn_end", event.SubagentStop: "subagent_stop"}[ev.Kind]
		e.report(typ, map[string]any{"text": e.textField(ev.Output)}, nil, "")
	case event.SubagentStart:
		e.report("subagent_start", map[string]any{}, nil, "")
	case event.Compact:
		e.report("context_compact", map[string]any{}, nil, "")
	case event.Notification:
		e.report("notification", map[string]any{"text": e.textField(ev.Output)}, nil, "")
	case event.SessionEnd:
		e.endRun()
	}
	return adapters.Render(ev, event.Verdict{Decision: event.NoDecision})
}

// ---- session ---------------------------------------------------------------

func (e *engine) sessionStart() {
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout())
	defer cancel()
	var wg sync.WaitGroup
	goSafe(&wg, func() { _, _ = e.ensureRun(ctx) })
	goSafe(&wg, func() { e.refreshPolicy(ctx) })
	wg.Wait()
	e.checkBypass()
	e.checkParentArgs()
}

func (e *engine) tags() []string {
	t := []string{"client:" + e.ev.Client, "os:" + config.OSArch(), "sensor:" + client.Version}
	if e.clientVersion != "" {
		t = append(t, "client_version:"+e.clientVersion)
	}
	if r := repoTag(e.ev.Cwd, e.cfg.RepoNames == "plain"); r != "" {
		t = append(t, r)
	}
	if e.cfg.DeviceID != "" {
		t = append(t, "device:"+e.cfg.DeviceID)
	} else if c := e.credential(); c != nil && c.DeviceID != "" {
		t = append(t, "device:"+c.DeviceID)
	}
	return t
}

func (e *engine) ensureRun(ctx context.Context) (string, error) {
	s := state.LoadSession(e.ev.Client, e.ev.SessionID)
	if s.RunID != "" {
		return s.RunID, nil
	}
	if e.credential() == nil {
		return "", config.ErrNoCredential
	}
	out, err := e.api().CreateRun(ctx, client.RunIn{AgentName: e.agent, SessionID: e.ev.SessionID, Tags: e.tags()})
	if err != nil {
		return "", err
	}
	state.Update(e.ev.Client, e.ev.SessionID, func(s *state.Session) { s.RunID, s.Agent = out.RunID, e.agent })
	return out.RunID, nil
}

func (e *engine) refreshPolicy(ctx context.Context) *client.Policy {
	if e.credential() == nil {
		return nil
	}
	p, err := e.api().Policy(ctx, e.agent)
	if err != nil {
		return nil
	}
	state.SavePolicy(p)
	return p
}

// NotSignedInMessage is shown at session start until the machine signs in.
func NotSignedInMessage() string {
	return "AgentWatch is installed but this machine is not signed in. Events are kept on this machine and sent " +
		"after sign-in. To sign in, " + config.SignInHint() + "."
}

// AW-D05: Claude Code tells every hook its permission mode.
func (e *engine) checkBypass() {
	if e.ev.PermissionMode != "bypassPermissions" {
		return
	}
	s := state.LoadSession(e.ev.Client, e.ev.SessionID)
	if s.BypassReported {
		return
	}
	state.Update(e.ev.Client, e.ev.SessionID, func(s *state.Session) { s.BypassReported = true })
	e.securityEvent("approval_bypass_launch", map[string]any{"permission_mode": "bypassPermissions",
		"client": e.ev.Client, "summary": e.ev.Client + " is running with approvals bypassed"})
}

var bypassFlags = lazyre.New(`--dangerously-skip-permissions|--dangerously-bypass-approvals-and-sandbox|--yolo\b|--allow-all-tools|--trust-all-tools|--full-auto`)

func (e *engine) checkParentArgs() {
	var args []string
	if e.o.ParentArgs != nil {
		args = e.o.ParentArgs()
	} else {
		args = parentArgs()
	}
	for _, a := range args {
		if m := bypassFlags.FindString(a); m != "" {
			e.securityEvent("approval_bypass_launch", map[string]any{"flag": m, "client": e.ev.Client,
				"summary": e.ev.Client + " was launched with " + m})
			return
		}
	}
}

// parentArgs reads the command lines of the hook's parent and grandparent
// (the client, possibly behind a shell). Unix only; Windows returns nothing.
func parentArgs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	var out []string
	pid := os.Getppid()
	for i := 0; i < 3 && pid > 1; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		b, err := exec.CommandContext(ctx, "ps", "-o", "ppid=,args=", "-p", fmt.Sprint(pid)).Output()
		cancel()
		if err != nil {
			break
		}
		line := strings.TrimSpace(string(b))
		var ppid int
		var rest string
		if _, err := fmt.Sscanf(line, "%d", &ppid); err != nil {
			break
		}
		if i := strings.IndexByte(line, ' '); i > 0 {
			rest = strings.TrimSpace(line[i:])
		}
		out = append(out, rest)
		pid = ppid
	}
	return out
}

// debugLog appends one line to AGENTWATCH_DEBUG_LOG when set (tests, E2E,
// support). It records decisions and their sources, never inputs.
func debugLog(m map[string]any) {
	p := os.Getenv("AGENTWATCH_DEBUG_LOG")
	if p == "" {
		return
	}
	m["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(m)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// notifyLocal appends to ~/.agentwatch/state/notify.jsonl, which the editor
// extension watches to show DENY/HOLD notifications without polling the API.
// Bounded: the file is cut back to its newest half past 256 KiB.
func notifyLocal(m map[string]any) {
	m["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	p := filepath.Join(config.StateDir(), "notify.jsonl")
	_ = os.MkdirAll(config.StateDir(), 0o700)
	if info, err := os.Stat(p); err == nil && info.Size() > 256<<10 {
		if b, err := os.ReadFile(p); err == nil {
			_ = config.WriteFileAtomic(p, b[len(b)/2:], 0o600)
		}
	}
	b, _ := json.Marshal(m)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// goSafe runs fn in a goroutine whose panic is swallowed: a panic outside
// the main goroutine would otherwise kill the process and the client would
// see a crashed hook.
func goSafe(wg *sync.WaitGroup, fn func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		fn()
	}()
}

// ---- pre-tool ----------------------------------------------------------------

func (e *engine) preTool() []byte {
	ev := e.ev
	t0 := time.Now()
	pol, age := state.LoadPolicy(e.agent)
	v, needServer := policy.Local(ev, e.cfg, pol, config.UserHome())
	if needServer {
		v = e.askServer(pol == nil || age > PolicyMaxAge)
	}
	v.LatencyMS = time.Since(t0).Milliseconds()
	final, note := policy.ApplyMode(e.cfg, v)
	debugLog(map[string]any{"event": ev.HookEvent, "client": ev.Client, "tool": ev.ToolName, "class": ev.Class,
		"mode": e.cfg.Mode, "mode_source": e.cfg.Sources["mode"], "fail_mode": e.cfg.FailMode,
		"decision": final.Decision, "source": v.Source, "would_have": final.WouldHave, "server": v.ServerDecision,
		"latency_ms": v.LatencyMS, "timings": e.timings})

	switch v.Source {
	case event.SrcSelfProtect:
		e.securityEvent("self_protection_block", map[string]any{"tool": ev.ToolName, "path": redact.String(ev.Path),
			"summary": "agent tried to modify AgentWatch's own files"})
	case event.SrcFailMode:
		typ := "sensor_failed_open"
		if final.Blocks() {
			typ = "sensor_failed_closed"
		}
		e.securityEvent(typ, map[string]any{"tool": ev.ToolName, "summary": note})
	}
	if final.Blocks() {
		notifyLocal(map[string]any{"client": ev.Client, "agent": e.agent, "tool": ev.ToolName,
			"decision": final.Decision, "reason": redact.Truncate(redact.String(final.Reason), 400),
			"hold_id": final.HoldID, "source": final.Source})
		// The tool will not run: there is no post event, so report it now.
		e.report("tool_call", e.toolInput(final, note), map[string]any{"blocked": true}, "")
	} else if v.Source != event.SrcLocal {
		// A local allow needs no state: the post-tool report infers it from the
		// tool class (keeps the read path to one process start and no writes).
		key := e.pendingKey()
		state.Update(ev.Client, ev.SessionID, func(s *state.Session) {
			s.Pending[key] = state.Pending{Decision: final.Decision, Source: final.Source, WouldHave: final.WouldHave,
				HoldID: final.HoldID, Server: final.ServerDecision, LatencyMS: final.LatencyMS, TS: time.Now().UnixMilli()}
		})
	}
	if final.Decision == event.Hold && ev.Client == "claude-code" {
		// Claude Code asks the person; if the tool then runs, that was a local
		// override of an AgentWatch hold (reported at post-tool).
		key := e.pendingKey()
		state.Update(ev.Client, ev.SessionID, func(s *state.Session) {
			s.Pending[key] = state.Pending{Decision: event.Hold, Source: final.Source, HoldID: final.HoldID,
				Server: final.ServerDecision, TS: time.Now().UnixMilli()}
		})
	}
	return adapters.Render(ev, final)
}

func (e *engine) askServer(refresh bool) event.Verdict {
	ev := e.ev
	if e.credential() == nil {
		return policy.FromFailure(e.cfg, fmt.Errorf("%w: %w", client.ErrUnavailable, config.ErrNoCredential))
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout())
	defer cancel()
	var wg sync.WaitGroup
	if refresh {
		goSafe(&wg, func() { e.refreshPolicy(ctx) })
	}
	defer wg.Wait()
	t0 := time.Now()
	runID, err := e.ensureRun(ctx)
	e.mark("ensure_run_us", t0)
	if err != nil {
		return e.failure(err)
	}
	ih := e.inputHash()
	s := state.LoadSession(ev.Client, ev.SessionID)
	in := client.PreflightIn{
		RunID: runID, StepType: "tool_call", ToolName: ev.ToolName, Client: ev.Client,
		Command: redact.Truncate(redact.String(ev.Command), 4000), Path: redact.String(ev.Path),
		MCPServer: ev.MCPServer, MCPTool: ev.MCPTool, HoldID: s.Holds[ih], SensorMode: e.cfg.Mode,
		InputData: e.serverInput(),
	}
	t1 := time.Now()
	out, err := e.api().Preflight(ctx, in)
	e.mark("preflight_us", t1)
	if err != nil && client.StatusOf(err) == 404 {
		// The run is gone (purged): open a new one once.
		state.Update(ev.Client, ev.SessionID, func(s *state.Session) { s.RunID = "" })
		if runID, err = e.ensureRun(ctx); err == nil {
			in.RunID = runID
			out, err = e.api().Preflight(ctx, in)
		}
	}
	if err != nil {
		return e.failure(err)
	}
	v := policy.FromServer(ev, out)
	t2 := time.Now()
	defer e.mark("state_us", t2)
	state.Update(ev.Client, ev.SessionID, func(s *state.Session) {
		if v.Decision == event.Hold && v.HoldID != "" {
			s.Holds[ih] = v.HoldID
		} else {
			delete(s.Holds, ih)
		}
	})
	return v
}

func (e *engine) failure(err error) event.Verdict {
	if client.StatusOf(err) == 409 {
		state.Update(e.ev.Client, e.ev.SessionID, func(s *state.Session) { s.Killed = true })
	}
	v := policy.FromFailure(e.cfg, err)
	if v.Source == event.SrcFailMode && client.IsUnavailable(err) {
		e.securityEvent("sensor_preflight_unavailable", map[string]any{"tool": e.ev.ToolName, "error": err.Error()})
	}
	return v
}

// serverInput is what preflight sees of the call: the redacted command or
// path, never file contents.
func (e *engine) serverInput() map[string]any {
	in := map[string]any{"class": e.ev.Class}
	if e.ev.Command != "" {
		in["command"] = redact.Truncate(redact.String(e.ev.Command), 4000)
	}
	if e.ev.Path != "" {
		in["path"] = redact.String(e.ev.Path)
	}
	if e.ev.MCPServer != "" {
		in["mcp_server"] = e.ev.MCPServer
	}
	if e.ev.MCPTool != "" {
		in["mcp_tool"] = e.ev.MCPTool
	}
	if u, ok := e.ev.Input["url"].(string); ok {
		in["url"] = redact.String(u)
	}
	return in
}

func (e *engine) inputHash() string {
	h := sha256.Sum256([]byte(e.ev.ToolName + "\x00" + e.ev.Command + "\x00" + e.ev.Path + "\x00" + e.ev.MCPServer + "\x00" + e.ev.MCPTool))
	return hex.EncodeToString(h[:])[:24]
}

func (e *engine) pendingKey() string {
	if e.ev.ToolUseID != "" {
		return "u:" + e.ev.ToolUseID
	}
	return "h:" + e.inputHash()
}

// ---- post-tool / reports -------------------------------------------------------

func (e *engine) postTool() {
	ev := e.ev
	key := e.pendingKey()
	var p state.Pending
	var found bool
	state.Update(ev.Client, ev.SessionID, func(s *state.Session) {
		p, found = s.Pending[key]
		delete(s.Pending, key)
	})
	if found && p.Decision == event.Hold {
		e.securityEvent("hold_overridden_locally", map[string]any{"tool": ev.ToolName, "hold_id": p.HoldID,
			"summary": "a call AgentWatch held was approved in the editor and ran"})
	}
	verdict := map[string]any{}
	if !found && !e.cfg.Strict && (ev.Class == event.ClassRead || ev.Class == event.ClassMeta) {
		verdict = map[string]any{"decision": event.Allow, "source": event.SrcLocal}
	}
	if found {
		verdict = map[string]any{"decision": p.Decision, "source": p.Source, "would_have": p.WouldHave,
			"server": p.Server, "latency_ms": p.LatencyMS, "hold_id": p.HoldID}
	}
	in := e.toolInputFrom(verdict)
	out := map[string]any{"output_bytes": ev.OutputBytes}
	if ev.ExitCode != nil {
		out["exit_code"] = *ev.ExitCode
	}
	if ev.Interrupted {
		out["interrupted"] = true
	}
	if e.captureContent() && ev.Output != "" {
		out["output"] = redact.Truncate(redact.String(ev.Output), 4000)
	}
	// The result metadata also rides in the input summary: with content capture
	// off the server keeps only that summary (args_redacted), not output_data.
	in["result"] = map[string]any{}
	for k, v := range out {
		if k != "output" {
			in["result"].(map[string]any)[k] = v
		}
	}
	errMsg := ""
	if ev.Kind == event.PostToolFailure {
		errMsg = redact.Truncate(redact.String(ev.Error), 300)
	}
	e.report("tool_call", in, out, errMsg)
}

func (e *engine) toolInput(v event.Verdict, note string) map[string]any {
	return e.toolInputFrom(map[string]any{"decision": v.Decision, "source": v.Source, "would_have": v.WouldHave,
		"server": v.ServerDecision, "latency_ms": v.LatencyMS, "hold_id": v.HoldID, "note": redact.String(note)})
}

func (e *engine) toolInputFrom(verdict map[string]any) map[string]any {
	ev := e.ev
	in := map[string]any{"tool": ev.ToolName, "class": ev.Class, "client": ev.Client, "hook_event": ev.HookEvent}
	if ev.Command != "" {
		in["command"] = redact.Truncate(redact.String(ev.Command), 2000)
	}
	if ev.Path != "" {
		in["path"] = redact.String(ev.Path)
	}
	if ev.MCPServer != "" {
		in["mcp_server"] = ev.MCPServer
	}
	if ev.MCPTool != "" {
		in["mcp_tool"] = ev.MCPTool
	}
	if len(ev.Input) > 0 {
		keys := make([]string, 0, len(ev.Input))
		for k := range ev.Input {
			keys = append(keys, k)
		}
		in["arg_keys"] = keys
		if e.captureContent() {
			in["args"] = redact.Value(ev.Input)
		}
	}
	if len(verdict) > 0 {
		clean := map[string]any{}
		for k, v := range verdict {
			// latency_ms is kept even at 0 (a sub-millisecond decision), so a
			// report always has the same shape.
			if k == "latency_ms" || (v != nil && v != "" && v != int64(0)) {
				clean[k] = v
			}
		}
		in["agentwatch"] = clean
	}
	return in
}

// textField: length + hash prefix, or the redacted text when content
// capture is on (and an approver is named: AW-P11).
func (e *engine) textField(s string) map[string]any {
	h := sha256.Sum256([]byte(s))
	out := map[string]any{"length": len(s), "sha256_12": hex.EncodeToString(h[:])[:12]}
	if e.captureContent() && s != "" {
		out["text"] = redact.Truncate(redact.String(s), 4000)
	}
	return out
}

func (e *engine) captureContent() bool { return e.cfg.ContentCapture && e.cfg.Approver != "" }

type ingestRecord struct {
	client.IngestIn
}

func (r ingestRecord) EventID() string { return r.IngestIn.EventID }

func (e *engine) report(stepType string, in, out map[string]any, errMsg string) {
	ev := e.ev
	rec := client.IngestIn{
		SessionID: ev.SessionID, AgentName: e.agent, EventType: stepType, ToolName: ev.ToolName,
		Model: ev.Model, InputData: in, OutputData: out, Tags: e.tags(), EventID: spool.NewID(),
		AgentVersion: e.clientVersion, LatencyMS: ev.DurationMS,
	}
	if errMsg != "" {
		rec.EventType = "error"
		rec.ErrorType = "tool_error"
		rec.ErrorMessage = errMsg
	}
	if rec.ToolName == "" {
		rec.ToolName = stepType
	}
	if err := e.sp.Append("ingest", ingestRecord{rec}); err == nil {
		e.spooled = true
	}
}

func (e *engine) endRun() {
	ev := e.ev
	rec := client.IngestIn{SessionID: ev.SessionID, AgentName: e.agent, EventType: "run_end", EndRun: true,
		EndStatus: "done", Tags: e.tags(), EventID: spool.NewID()}
	if err := e.sp.Append("ingest", ingestRecord{rec}); err == nil {
		e.spooled = true
	}
}

func (e *engine) securityEvent(typ string, detail map[string]any) {
	for k, v := range detail {
		if s, ok := v.(string); ok {
			detail[k] = redact.Truncate(redact.String(s), 500)
		}
	}
	if err := e.sp.Append("event", client.SensorEvent{Type: typ, AgentName: e.agent, SessionID: e.ev.SessionID,
		Detail: detail}); err == nil {
		e.spooled = true
	}
}

func (e *engine) maybeFlush() {
	if !e.spooled || e.o.NoFlush || os.Getenv("AGENTWATCH_NO_FLUSH") == "1" || e.o.Exe == "" {
		return
	}
	if e.sp.Locked() {
		return
	}
	// Starting a process costs a few ms. In a burst of tool calls one flusher
	// started in the last 2 s is enough; session end always starts one.
	if e.ev.Kind != event.SessionEnd && time.Since(state.ReadStamp("flusher-started")) < 2*time.Second {
		return
	}
	state.WriteStamp("flusher-started")
	_ = spool.StartFlusher(e.o.Exe)
}

// repoTag is "repo:<hash>" (or the plain name if opted in) from the git
// remote of cwd, without running git.
func repoTag(cwd string, plain bool) string {
	if cwd == "" {
		return ""
	}
	dir := cwd
	for i := 0; i < 12; i++ {
		b, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
		if err == nil {
			name := filepath.Base(dir)
			if m := lazyre.New(`(?m)^\s*url\s*=\s*(\S+)`).FindStringSubmatch(string(b)); m != nil {
				name = strings.TrimSuffix(filepath.Base(strings.ReplaceAll(m[1], ":", "/")), ".git")
				if !plain {
					name = m[1]
				}
			}
			if plain {
				return "repo:" + name
			}
			return "repo:" + state.Hash(redact.String(name))[:12]
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if plain {
		return "repo:" + filepath.Base(cwd)
	}
	return "repo:" + state.Hash(cwd)[:12]
}
