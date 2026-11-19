// Package cursor adapts Cursor hook payloads (docs/research/cursor.md).
package cursor

import (
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

const Name = "cursor"

var kinds = map[string]string{
	"sessionStart":         event.SessionStart,
	"sessionEnd":           event.SessionEnd,
	"beforeSubmitPrompt":   event.Prompt,
	"preToolUse":           event.PreTool,
	"beforeShellExecution": event.PreTool,
	"beforeMCPExecution":   event.PreTool,
	"beforeReadFile":       event.PreTool,
	"postToolUse":          event.PostTool,
	"postToolUseFailure":   event.PostToolFailure,
	"afterAgentResponse":   event.Response,
	"stop":                 event.Stop,
	"subagentStart":        event.SubagentStart,
	"subagentStop":         event.SubagentStop,
	"preCompact":           event.Compact,
	// Reported through postToolUse instead (both fire for the same call), so
	// these are acknowledged and not reported twice.
	"afterShellExecution": event.Ignored,
	"afterMCPExecution":   event.Ignored,
	"afterFileEdit":       event.Ignored,
	"afterAgentThought":   event.Ignored,
}

// controlEvents are Cursor's permission hooks: they must always get valid
// JSON, because invalid output blocks the action even with failClosed off.
var controlEvents = map[string]bool{
	"beforeSubmitPrompt": true, "sessionStart": true, "preToolUse": true, "beforeShellExecution": true,
	"beforeMCPExecution": true, "beforeReadFile": true, "subagentStart": true,
}

// IsControl reports whether a Cursor event needs a permission answer.
func IsControl(hookEvent string) bool { return controlEvents[hookEvent] }

// Parse turns a Cursor stdin payload into an Event. One chat is one run:
// sessions key on conversation_id (Cursor mints a generation per turn).
func Parse(raw map[string]any) *event.Event {
	he := common.Str(raw, "hook_event_name", "hookEventName")
	ev := &event.Event{
		Client:        Name,
		ClientVersion: common.Str(raw, "cursor_version"),
		HookEvent:     he,
		Kind:          kinds[he],
		SessionID:     common.Str(raw, "conversation_id", "session_id"),
		Cwd:           common.Str(raw, "cwd"),
		Model:         common.Str(raw, "model"),
		ToolUseID:     common.Str(raw, "tool_use_id"),
		Prompt:        common.Str(raw, "prompt"),
		DurationMS:    common.Int(raw, "duration", "duration_ms"),
		EndReason:     common.Str(raw, "reason", "status"),
		Interrupted:   common.Bool(raw, "is_interrupt"),
	}
	if ev.Kind == "" {
		ev.Kind = event.Ignored
	}
	if ev.Cwd == "" {
		if roots, ok := raw["workspace_roots"].([]any); ok && len(roots) > 0 {
			ev.Cwd, _ = roots[0].(string)
		}
	}
	switch he {
	case "beforeShellExecution":
		ev.ToolName = "Shell"
		ev.Input = map[string]any{"command": common.Str(raw, "command"), "cwd": common.Str(raw, "cwd")}
	case "beforeMCPExecution":
		ev.ToolName = "MCP:" + common.Str(raw, "tool_name")
		ev.Input = common.Map(raw, "tool_input")
		if ev.Input == nil {
			ev.Input = map[string]any{}
		}
		ev.Input["server"] = common.Str(raw, "mcp_server_name")
	case "beforeReadFile":
		ev.ToolName = "Read"
		ev.Input = map[string]any{"file_path": common.Str(raw, "file_path")}
	default:
		ev.ToolName = common.Str(raw, "tool_name")
		ev.Input = common.Map(raw, "tool_input")
	}
	if ev.ToolName != "" {
		ev.Class, ev.Command, ev.Path, ev.MCPServer, ev.MCPTool = common.Classify(ev.ToolName, ev.Input)
	}
	switch ev.Kind {
	case event.PreTool, event.Prompt, event.SubagentStart:
		ev.CanBlock = controlEvents[he]
	case event.PostTool:
		ev.Output = common.Text(raw["tool_output"])
		ev.OutputBytes = len(ev.Output)
		if m := common.Map(raw, "tool_output"); m != nil {
			ev.ExitCode = common.Int(m, "exitCode", "exit_code")
		}
	case event.PostToolFailure:
		ev.Error = common.Str(raw, "error_message", "error")
		ev.OutputBytes = len(ev.Error)
	case event.Response:
		ev.Output = common.Str(raw, "text")
		ev.OutputBytes = len(ev.Output)
	}
	return ev
}

// Render prints Cursor's answer. Control events always get valid JSON. HOLD
// is a deny with instructions: Cursor accepts `ask` on preToolUse but does not
// enforce it, so an ask would let the call through.
func Render(ev *event.Event, v event.Verdict) []byte {
	if !controlEvents[ev.HookEvent] {
		return []byte("{}")
	}
	if ev.HookEvent == "beforeSubmitPrompt" || ev.HookEvent == "sessionStart" {
		return common.Marshal(map[string]any{"continue": true, "permission": "allow"})
	}
	switch v.Decision {
	case event.Deny, event.Hold:
		return common.Marshal(map[string]any{
			"continue": true, "permission": "deny", "user_message": v.Reason, "agent_message": v.Reason,
		})
	case event.Scrub:
		if v.UpdatedInput != nil && ev.HookEvent == "preToolUse" {
			return common.Marshal(map[string]any{"continue": true, "permission": "allow", "updated_input": v.UpdatedInput,
				"agent_message": v.Reason})
		}
		// Only preToolUse can rewrite input; elsewhere a scrub is a deny.
		return common.Marshal(map[string]any{
			"continue": true, "permission": "deny", "user_message": v.Reason, "agent_message": v.Reason,
		})
	}
	return common.Marshal(map[string]any{"continue": true, "permission": "allow"})
}

// Neutral is the answer when the sensor yields to another installation.
func Neutral(hookEvent string) []byte {
	if controlEvents[hookEvent] {
		return common.Marshal(map[string]any{"continue": true, "permission": "allow"})
	}
	return []byte("{}")
}
