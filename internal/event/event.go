// Package event is the client-neutral shape of one hook invocation and of
// the sensor's answer to it. Adapters translate each client's payload into an
// Event and a Verdict back into that client's output format.
package event

// Kinds of hook event, normalised across clients.
const (
	SessionStart    = "session_start"
	Prompt          = "prompt"
	PreTool         = "pre_tool"
	PostTool        = "post_tool"
	PostToolFailure = "post_tool_failure"
	Response        = "response"
	Stop            = "stop"
	SessionEnd      = "session_end"
	SubagentStart   = "subagent_start"
	SubagentStop    = "subagent_stop"
	Notification    = "notification"
	Compact         = "compact"
	Ignored         = "ignored"
)

// Tool classes. Unknown tools are classed Shell so they get a server check.
const (
	ClassRead    = "read"
	ClassWrite   = "write"
	ClassShell   = "shell"
	ClassNetwork = "network"
	ClassMCP     = "mcp"
	ClassMeta    = "meta"
)

// Event is one hook invocation, normalised.
type Event struct {
	Client        string // claude-code | cursor | codex | copilot
	ClientVersion string
	HookEvent     string // the client's own event name, verbatim
	Kind          string
	SessionID     string
	ToolName      string
	ToolUseID     string
	Class         string
	Command       string // raw: never leaves the machine before redaction
	Path          string
	MCPServer     string
	MCPTool       string
	Input         map[string]any // raw tool input (local only)
	Cwd           string
	Model         string
	// PermissionMode is Claude Code's permission_mode (bypassPermissions = AW-D05).
	PermissionMode string
	Prompt         string // local only; hashed + length unless content capture is on
	Output         string // tool output; local only unless content capture is on
	OutputBytes    int
	ExitCode       *int
	DurationMS     *int
	Error          string
	Interrupted    bool
	EndReason      string
	// CanBlock is true when the client honours a deny for this event.
	CanBlock bool
}

// Decisions.
const (
	Allow      = "allow"
	Scrub      = "scrub"
	Hold       = "hold"
	Deny       = "deny"
	NoDecision = "" // print nothing / the client's neutral answer
)

// Sources of a decision, for the report and for dry_run exceptions.
const (
	SrcLocal       = "local"        // read-only, decided without the server
	SrcServer      = "server"       // POST /steps/preflight answered
	SrcDeniedTool  = "denied_tool"  // passport denied_tools: always enforced
	SrcSelfProtect = "self_protect" // always enforced
	SrcIsolated    = "isolated"     // passport isolated / run killed
	SrcFailMode    = "fail_mode"    // server unavailable
	SrcPassthrough = "passthrough"  // event cannot block / nothing to decide
)

// Verdict is the sensor's answer.
type Verdict struct {
	Decision     string
	Reason       string // shown to the developer / agent
	Source       string
	HoldID       string
	UpdatedInput map[string]any
	// WouldHave is what enforce would have done when dry_run let it through.
	WouldHave string
	// ServerDecision is the raw server answer (allow|warn|require_approval|hold|scrub|deny).
	ServerDecision string
	LatencyMS      int64
}

// Blocks reports whether the verdict stops the call.
func (v Verdict) Blocks() bool { return v.Decision == Deny || v.Decision == Hold }
