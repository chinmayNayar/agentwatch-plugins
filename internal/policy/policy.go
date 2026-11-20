// Package policy is the sensor's decision logic, kept free of I/O so every
// branch is unit-testable. The hook engine feeds it the event, the config, the
// cached passport and (when needed) the server's answer or failure.
package policy

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
	"github.com/chinmayNayar/agentwatch-plugins/internal/selfprotect"
)

// Local decides without the network. needServer=true means: ask preflight.
func Local(ev *event.Event, cfg *config.Config, pol *client.Policy, home string) (v event.Verdict, needServer bool) {
	if ev.Kind != event.PreTool || !ev.CanBlock {
		return event.Verdict{Decision: event.NoDecision, Source: event.SrcPassthrough}, false
	}
	if cfg.SelfProtectOn() {
		if reason := selfprotect.Check(ev, home); reason != "" {
			return event.Verdict{Decision: event.Deny, Reason: reason, Source: event.SrcSelfProtect}, false
		}
	}
	if pol != nil {
		names := toolNames(ev)
		if globAny(pol.DeniedTools, names) {
			return event.Verdict{Decision: event.Deny, Source: event.SrcDeniedTool,
				Reason: fmt.Sprintf("AgentWatch blocked %s for %s: this tool is on the agent's denied list", ev.ToolName, pol.AgentName)}, false
		}
		if pol.Isolated {
			return event.Verdict{Decision: event.Deny, Source: event.SrcIsolated,
				Reason: fmt.Sprintf("AgentWatch: %s is isolated. Every tool call is blocked until it is restored in the console.", pol.AgentName)}, false
		}
		if restricted(pol.AllowedTools) && !globAny(pol.AllowedTools, names) {
			return event.Verdict{Decision: event.Deny, Source: event.SrcServer,
				Reason: fmt.Sprintf("AgentWatch blocked %s for %s: this tool is not on the agent's allowed list", ev.ToolName, pol.AgentName)}, false
		}
		if globAny(pol.ApprovalRequiredTools, names) {
			return event.Verdict{}, true // the server creates the hold
		}
	}
	if !cfg.Strict && (ev.Class == event.ClassRead || ev.Class == event.ClassMeta) {
		return event.Verdict{Decision: event.Allow, Source: event.SrcLocal}, false
	}
	return event.Verdict{}, true
}

// toolNames: the tool as the client names it, plus the mcp__server__tool form,
// so one passport pattern matches Claude Code, Codex and Cursor alike.
func toolNames(ev *event.Event) []string {
	out := []string{ev.ToolName}
	if ev.MCPServer != "" && ev.MCPTool != "" {
		out = append(out, "mcp__"+ev.MCPServer+"__"+ev.MCPTool)
	}
	return out
}

func restricted(allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, a := range allowed {
		if a == "*" {
			return false
		}
	}
	return true
}

func globAny(patterns []string, names []string) bool {
	for _, p := range patterns {
		for _, n := range names {
			if n == "" {
				continue
			}
			if ok, _ := path.Match(p, n); ok || p == n {
				return true
			}
			if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(n)); ok {
				return true
			}
		}
	}
	return false
}

// HoldReason is what the developer and the agent see for a held call.
func HoldReason(holdID, display string) string {
	msg := fmt.Sprintf("AgentWatch is holding this call for approval (hold %s). An approver can approve it in "+
		"AgentWatch → Approvals; then retry the same call.", short(holdID))
	if display != "" {
		msg += " " + display
	}
	return msg
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// FromServer maps a preflight answer to a verdict.
func FromServer(ev *event.Event, out *client.PreflightOut) event.Verdict {
	v := event.Verdict{Source: event.SrcServer, ServerDecision: out.Decision}
	for _, r := range out.Reasons {
		switch r {
		case "tool_denied":
			v.Source = event.SrcDeniedTool
		case "passport_isolated":
			v.Source = event.SrcIsolated
		}
	}
	switch out.Decision {
	case "deny":
		v.Decision, v.Reason = event.Deny, out.DisplayReason
	case "hold":
		v.Decision, v.HoldID = event.Hold, out.HoldID
		v.Reason = HoldReason(out.HoldID, "")
	case "scrub":
		v.Decision, v.Reason = event.Scrub, out.DisplayReason
		v.UpdatedInput = mergeScrub(ev.Input, out.ScrubbedInput)
	case "require_approval":
		// Only a dry_run sensor gets this (an enforcing one gets hold).
		v.Decision, v.WouldHave = event.Allow, event.Hold
		v.Reason = out.DisplayReason
	default: // allow, warn
		v.Decision = event.Allow
		if out.WouldHave == "deny" || out.WouldHave == "require_approval" {
			v.WouldHave = out.WouldHave
		}
	}
	if v.Reason == "" && v.Decision != event.Allow {
		v.Reason = "AgentWatch policy"
	}
	return v
}

// mergeScrub writes the server's redacted fields back into the client's own
// input shape. Nothing mapped: nil (the adapter then denies instead).
func mergeScrub(orig map[string]any, scrubbed map[string]any) map[string]any {
	if orig == nil || scrubbed == nil {
		return nil
	}
	out := make(map[string]any, len(orig))
	for k, v := range orig {
		out[k] = v
	}
	changed := false
	for k, v := range scrubbed {
		if _, ok := out[k]; ok {
			if fmt.Sprint(out[k]) != fmt.Sprint(v) {
				out[k] = v
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return out
}

// FromFailure is the verdict when the server could not answer.
func FromFailure(cfg *config.Config, err error) event.Verdict {
	switch client.StatusOf(err) {
	case 409:
		return event.Verdict{Decision: event.Deny, Source: event.SrcIsolated,
			Reason: "AgentWatch stopped this session (its run was killed or ended in the console)."}
	case 403:
		var e *client.Error
		if asErr(err, &e) && (e.Code == "agent_isolated" || e.Code == "spawn_denied") {
			return event.Verdict{Decision: event.Deny, Source: event.SrcIsolated,
				Reason: "AgentWatch: this agent is isolated. Every tool call is blocked until it is restored."}
		}
	}
	what := "AgentWatch could not be reached"
	if errors.Is(err, config.ErrNoCredential) {
		what = "This machine is not signed in to AgentWatch (to sign in, " + config.SignInHint() + ")"
	} else if st := client.StatusOf(err); st == 401 || st == 403 {
		what = "AgentWatch rejected this machine's credentials (sign in again: " + config.SignInHint() + ")"
	}
	if cfg.Mode == config.ModeEnforce && cfg.FailMode == config.FailClosed {
		return event.Verdict{Decision: event.Deny, Source: event.SrcFailMode,
			Reason: what + "; fail-closed is on, so the call is blocked."}
	}
	return event.Verdict{Decision: event.Allow, Source: event.SrcFailMode, WouldHave: "",
		Reason: what + "; fail-open, so the call went ahead."}
}

func asErr(err error, target **client.Error) bool {
	e, ok := err.(*client.Error)
	if ok {
		*target = e
	}
	return ok
}

// ApplyMode softens a blocking verdict in dry_run. Self-protection and
// denied_tools are enforced in every mode. In dry_run the agent is not told
// (no reason in the output): it must behave as it would without AgentWatch.
func ApplyMode(cfg *config.Config, v event.Verdict) (out event.Verdict, note string) {
	if cfg.Mode == config.ModeEnforce || !v.Blocks() && v.Decision != event.Scrub {
		if v.Decision == event.Allow {
			// Allow never carries a message to the agent.
			return event.Verdict{Decision: event.Allow, Source: v.Source, WouldHave: v.WouldHave,
				ServerDecision: v.ServerDecision, LatencyMS: v.LatencyMS}, v.Reason
		}
		return v, v.Reason
	}
	if v.Source == event.SrcSelfProtect || v.Source == event.SrcDeniedTool {
		return v, v.Reason
	}
	note = fmt.Sprintf("dry_run: would have %s. %s", v.Decision, v.Reason)
	return event.Verdict{Decision: event.Allow, Source: v.Source, WouldHave: v.Decision, HoldID: v.HoldID,
		ServerDecision: v.ServerDecision, LatencyMS: v.LatencyMS}, note
}
