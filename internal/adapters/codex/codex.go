// Package codex adapts OpenAI Codex hook payloads (docs/research/copilot-codex.md).
package codex

import (
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

const Name = "codex"

var kinds = map[string]string{
	"SessionStart":     event.SessionStart,
	"SessionEnd":       event.SessionEnd,
	"UserPromptSubmit": event.Prompt,
	"PreToolUse":       event.PreTool,
	"PostToolUse":      event.PostTool,
	"Stop":             event.Stop,
	"SubagentStart":    event.SubagentStart,
	"SubagentStop":     event.SubagentStop,
	"PreCompact":       event.Compact,
}

// Parse turns a Codex stdin payload into an Event.
func Parse(raw map[string]any) *event.Event {
	he := common.Str(raw, "hook_event_name")
	ev := &event.Event{
		Client:     Name,
		HookEvent:  he,
		Kind:       kinds[he],
		SessionID:  common.Str(raw, "session_id"),
		Cwd:        common.Str(raw, "cwd"),
		Model:      common.Str(raw, "model"),
		ToolName:   common.Str(raw, "tool_name"),
		ToolUseID:  common.Str(raw, "tool_use_id"),
		Prompt:     common.Str(raw, "prompt"),
		DurationMS: common.Int(raw, "duration_ms"),
		EndReason:  common.Str(raw, "reason", "source"),
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
		ev.Output = common.Text(raw["tool_response"])
		ev.OutputBytes = len(ev.Output)
	case event.Stop:
		ev.Output = common.Str(raw, "last_assistant_message")
		ev.OutputBytes = len(ev.Output)
	}
	return ev
}

// Render prints Codex's answer. Only documented fields: an unsupported field
// marks the hook failed and Codex runs the tool anyway. Codex has no `ask`,
// so HOLD is a deny with instructions.
func Render(ev *event.Event, v event.Verdict) []byte {
	if ev.Kind != event.PreTool {
		return nil
	}
	switch v.Decision {
	case event.Deny, event.Hold:
		return common.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": v.Reason,
		}})
	case event.Scrub:
		// updatedInput for Bash / apply_patch must keep a string `command`.
		if v.UpdatedInput != nil {
			if _, ok := v.UpdatedInput["command"].(string); ok || (ev.Class != event.ClassShell && ev.ToolName != "apply_patch") {
				return common.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
					"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": v.UpdatedInput,
				}})
			}
		}
		return common.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": v.Reason,
		}})
	}
	return nil
}
