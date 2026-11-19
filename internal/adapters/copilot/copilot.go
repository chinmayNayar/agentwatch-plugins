// Package copilot adapts GitHub Copilot hook payloads (VS Code agent, Copilot
// CLI; camelCase format, docs/research/copilot-codex.md). The installer
// registers camelCase events only: registering both casings fires twice.
package copilot

import (
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

const Name = "copilot"

var kinds = map[string]string{
	"sessionStart":        event.SessionStart,
	"sessionEnd":          event.SessionEnd,
	"userPromptSubmitted": event.Prompt,
	"preToolUse":          event.PreTool,
	"postToolUse":         event.PostTool,
	"postToolUseFailure":  event.PostToolFailure,
	"errorOccurred":       event.PostToolFailure,
	"agentStop":           event.Stop,
	"subagentStart":       event.SubagentStart,
	"subagentStop":        event.SubagentStop,
	"preCompact":          event.Compact,
	"notification":        event.Notification,
}

// Parse turns a Copilot stdin payload into an Event. Copilot's payload has no
// event name, so the hook command passes it as an argument (hookEvent).
func Parse(raw map[string]any, hookEvent string) *event.Event {
	he := hookEvent
	if he == "" {
		he = common.Str(raw, "hookEventName", "hook_event_name")
	}
	ev := &event.Event{
		Client:     Name,
		HookEvent:  he,
		Kind:       kinds[he],
		SessionID:  common.Str(raw, "sessionId", "session_id"),
		Cwd:        common.Str(raw, "cwd"),
		ToolName:   common.Str(raw, "toolName", "tool_name"),
		Prompt:     common.Str(raw, "prompt", "initialPrompt"),
		EndReason:  common.Str(raw, "reason", "source"),
		DurationMS: common.Int(raw, "durationMs", "duration_ms"),
	}
	if ev.Kind == "" {
		ev.Kind = event.Ignored
	}
	ev.Input = common.Map(raw, "toolArgs")
	if ev.Input == nil {
		ev.Input = common.Map(raw, "tool_input")
	}
	if ev.ToolName != "" {
		ev.Class, ev.Command, ev.Path, ev.MCPServer, ev.MCPTool = common.Classify(ev.ToolName, ev.Input)
	}
	switch ev.Kind {
	case event.PreTool:
		ev.CanBlock = true
	case event.PostTool:
		if r := common.Map(raw, "toolResult"); r != nil {
			ev.Output = common.Str(r, "textResultForLlm")
		}
		ev.OutputBytes = len(ev.Output)
	case event.PostToolFailure:
		if e := common.Map(raw, "error"); e != nil {
			ev.Error = common.Str(e, "message", "value")
		} else {
			ev.Error = common.Str(raw, "error")
		}
		ev.OutputBytes = len(ev.Error)
	}
	return ev
}

// Render prints Copilot's answer: top-level fields; `{}` is no decision.
// Copilot denies on any non-zero exit, so the sensor always exits 0 and puts
// the decision here. HOLD is a deny with instructions (Copilot's ask is a deny
// in cloud/autopilot runs and would bypass the AgentWatch approver otherwise).
func Render(ev *event.Event, v event.Verdict) []byte {
	if ev.Kind != event.PreTool {
		return []byte("{}")
	}
	switch v.Decision {
	case event.Deny, event.Hold:
		return common.Marshal(map[string]any{"permissionDecision": "deny", "permissionDecisionReason": v.Reason})
	case event.Scrub:
		if v.UpdatedInput != nil {
			return common.Marshal(map[string]any{"permissionDecision": "allow", "modifiedArgs": v.UpdatedInput,
				"permissionDecisionReason": v.Reason})
		}
		return common.Marshal(map[string]any{"permissionDecision": "deny", "permissionDecisionReason": v.Reason})
	}
	return []byte("{}")
}
