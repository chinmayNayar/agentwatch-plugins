// Package claudecode adapts Claude Code hook payloads (docs/research/claude-code.md).
package claudecode

import (
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

const Name = "claude-code"

var kinds = map[string]string{
	"SessionStart":       event.SessionStart,
	"UserPromptSubmit":   event.Prompt,
	"PreToolUse":         event.PreTool,
	"PostToolUse":        event.PostTool,
	"PostToolUseFailure": event.PostToolFailure,
	"Stop":               event.Stop,
	"SubagentStop":       event.SubagentStop,
	"SubagentStart":      event.SubagentStart,
	"SessionEnd":         event.SessionEnd,
	"Notification":       event.Notification,
	"PreCompact":         event.Compact,
}

// Parse turns a Claude Code stdin payload into an Event.
func Parse(raw map[string]any) *event.Event {
	he := common.Str(raw, "hook_event_name")
	ev := &event.Event{
		Client:         Name,
		HookEvent:      he,
		Kind:           kinds[he],
		SessionID:      common.Str(raw, "session_id"),
		Cwd:            common.Str(raw, "cwd"),
		Model:          common.Str(raw, "model"),
		PermissionMode: common.Str(raw, "permission_mode"),
		ToolName:       common.Str(raw, "tool_name"),
		ToolUseID:      common.Str(raw, "tool_use_id"),
		Prompt:         common.Str(raw, "prompt"),
		DurationMS:     common.Int(raw, "duration_ms"),
		EndReason:      common.Str(raw, "reason", "source"),
		Interrupted:    common.Bool(raw, "is_interrupt"),
	}
	if ev.Kind == "" {
		ev.Kind = event.Ignored
	}
	ev.Input = common.Map(raw, "tool_input")
	if ev.ToolName != "" {
		ev.Class, ev.Command, ev.Path, ev.MCPServer, ev.MCPTool = common.Classify(ev.ToolName, ev.Input)
	}
	switch ev.Kind {
	case event.PreTool:
		ev.CanBlock = true
	case event.PostTool:
		resp := raw["tool_response"]
		ev.Output = common.Text(resp)
		ev.OutputBytes = len(ev.Output)
		if m, ok := resp.(map[string]any); ok {
			ev.ExitCode = common.Int(m, "exit_code", "exitCode", "returncode")
			ev.Interrupted = ev.Interrupted || common.Bool(m, "interrupted")
		}
	case event.PostToolFailure:
		ev.Error = common.Str(raw, "error")
		ev.OutputBytes = len(ev.Error)
	case event.Stop, event.SubagentStop:
		ev.Output = common.Str(raw, "last_assistant_message")
		ev.OutputBytes = len(ev.Output)
	case event.Notification:
		ev.Output = common.Str(raw, "message")
	}
	return ev
}

// Render prints Claude Code's answer. ALLOW is no decision: an AgentWatch
// allow must never skip Claude Code's own permission prompt.
func Render(ev *event.Event, v event.Verdict) []byte {
	if ev.Kind != event.PreTool {
		return nil
	}
	out := map[string]any{"hookEventName": "PreToolUse"}
	switch v.Decision {
	case event.Deny:
		out["permissionDecision"] = "deny"
		out["permissionDecisionReason"] = v.Reason
	case event.Hold:
		// Claude Code can ask the person; the hold id lets the console match it.
		out["permissionDecision"] = "ask"
		out["permissionDecisionReason"] = v.Reason
	case event.Scrub:
		if v.UpdatedInput == nil {
			out["permissionDecision"] = "deny"
			out["permissionDecisionReason"] = v.Reason
		} else {
			// "ask" + updatedInput shows the person the redacted call; it does not
			// auto-approve anything the user would otherwise be asked about.
			out["permissionDecision"] = "ask"
			out["permissionDecisionReason"] = v.Reason
			out["updatedInput"] = v.UpdatedInput
		}
	default:
		if v.Reason == "" {
			return nil
		}
		// A note (dry_run would-have) goes to Claude as context, not a decision.
		out["additionalContext"] = v.Reason
	}
	return common.Marshal(map[string]any{"hookSpecificOutput": out})
}
