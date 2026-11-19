package policy

import (
	"testing"

	"github.com/chinmayNayar/agentwatch-plugins/internal/client"
	"github.com/chinmayNayar/agentwatch-plugins/internal/config"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

func cfg(mode, fail string) *config.Config {
	return &config.Config{Mode: mode, FailMode: fail, Sources: map[string]string{}}
}

func pre(tool, class string) *event.Event {
	return &event.Event{Kind: event.PreTool, CanBlock: true, ToolName: tool, Class: class}
}

func TestReadsAreLocalUnlessStrict(t *testing.T) {
	v, need := Local(pre("Read", event.ClassRead), cfg("enforce", "fail_open"), nil, "/h")
	if need || v.Decision != event.Allow || v.Source != event.SrcLocal {
		t.Fatalf("%+v %v", v, need)
	}
	c := cfg("enforce", "fail_open")
	c.Strict = true
	if _, need := Local(pre("Read", event.ClassRead), c, nil, "/h"); !need {
		t.Fatal("strict must ask the server for reads")
	}
	for _, cl := range []string{event.ClassShell, event.ClassWrite, event.ClassNetwork, event.ClassMCP} {
		if _, need := Local(pre("X", cl), cfg("dry_run", "fail_open"), nil, "/h"); !need {
			t.Fatalf("%s must go to the server", cl)
		}
	}
}

func TestDeniedToolsDenyLocallyInEveryMode(t *testing.T) {
	pol := &client.Policy{AgentName: "a", DeniedTools: []string{"Bash", "mcp__github__*"}}
	for _, mode := range []string{"dry_run", "enforce"} {
		c := cfg(mode, "fail_open")
		v, need := Local(pre("Bash", event.ClassShell), c, pol, "/h")
		if need || v.Decision != event.Deny || v.Source != event.SrcDeniedTool {
			t.Fatalf("%s: %+v", mode, v)
		}
		final, _ := ApplyMode(c, v)
		if final.Decision != event.Deny {
			t.Fatalf("%s: denied tool softened to %s", mode, final.Decision)
		}
		mcp := pre("MCP:create_issue", event.ClassMCP)
		mcp.MCPServer, mcp.MCPTool = "github", "create_issue"
		if v, _ := Local(mcp, c, pol, "/h"); v.Decision != event.Deny {
			t.Fatalf("cursor MCP name not matched by mcp__ pattern: %+v", v)
		}
	}
}

func TestIsolatedPassportBlocksOnlyInEnforce(t *testing.T) {
	pol := &client.Policy{AgentName: "a", Isolated: true}
	v, _ := Local(pre("Read", event.ClassRead), cfg("enforce", "fail_open"), pol, "/h")
	if f, _ := ApplyMode(cfg("enforce", "fail_open"), v); f.Decision != event.Deny {
		t.Fatalf("enforce: %+v", f)
	}
	if f, note := ApplyMode(cfg("dry_run", "fail_open"), v); f.Decision != event.Allow || f.WouldHave != event.Deny || note == "" {
		t.Fatalf("dry_run: %+v %q", f, note)
	}
}

func TestAllowedListRestricts(t *testing.T) {
	pol := &client.Policy{AgentName: "a", AllowedTools: []string{"Read", "Grep"}}
	if v, _ := Local(pre("Bash", event.ClassShell), cfg("enforce", "fail_open"), pol, "/h"); v.Decision != event.Deny {
		t.Fatalf("%+v", v)
	}
	pol.AllowedTools = []string{"*"}
	if _, need := Local(pre("Bash", event.ClassShell), cfg("enforce", "fail_open"), pol, "/h"); !need {
		t.Fatal("wildcard allow-list must not restrict")
	}
}

func TestServerVerdicts(t *testing.T) {
	ev := pre("Bash", event.ClassShell)
	ev.Input = map[string]any{"command": "echo sk-xxx", "description": "d"}
	cases := map[string]string{"allow": event.Allow, "warn": event.Allow, "deny": event.Deny, "hold": event.Hold,
		"scrub": event.Scrub, "require_approval": event.Allow}
	for in, want := range cases {
		out := &client.PreflightOut{Decision: in, HoldID: "h1", DisplayReason: "r",
			ScrubbedInput: map[string]any{"command": "echo [REDACTED]"}}
		v := FromServer(ev, out)
		if v.Decision != want {
			t.Fatalf("%s -> %s want %s", in, v.Decision, want)
		}
		if in == "scrub" && (v.UpdatedInput["command"] != "echo [REDACTED]" || v.UpdatedInput["description"] != "d") {
			t.Fatalf("scrub merge: %+v", v.UpdatedInput)
		}
		if in == "require_approval" && v.WouldHave != event.Hold {
			t.Fatal("dry_run approval must record would-have hold")
		}
	}
	v := FromServer(ev, &client.PreflightOut{Decision: "deny", Reasons: []string{"tool_denied"}})
	if v.Source != event.SrcDeniedTool {
		t.Fatal("server tool_denied must keep the always-deny source")
	}
	if f, _ := ApplyMode(cfg("dry_run", "fail_open"), v); f.Decision != event.Deny {
		t.Fatal("tool_denied from the server must deny in dry_run too")
	}
}

func TestFailureFollowsFailMode(t *testing.T) {
	unavailable := &client.Error{Status: 503}
	if v := FromFailure(cfg("enforce", "fail_closed"), unavailable); v.Decision != event.Deny {
		t.Fatal("fail_closed must deny")
	}
	if v := FromFailure(cfg("enforce", "fail_open"), unavailable); v.Decision != event.Allow {
		t.Fatal("fail_open must allow")
	}
	if v := FromFailure(cfg("dry_run", "fail_closed"), unavailable); v.Decision != event.Allow {
		t.Fatal("dry_run + fail_closed: still never blocks")
	}
	if v := FromFailure(cfg("enforce", "fail_open"), &client.Error{Status: 409}); v.Decision != event.Deny || v.Source != event.SrcIsolated {
		t.Fatal("a killed run stops the session in enforce")
	}
}

func TestDryRunNeverTellsTheAgent(t *testing.T) {
	v := event.Verdict{Decision: event.Deny, Reason: "AgentWatch blocked X", Source: event.SrcServer}
	f, note := ApplyMode(cfg("dry_run", "fail_open"), v)
	if f.Reason != "" || f.Decision != event.Allow || note == "" {
		t.Fatalf("%+v %q", f, note)
	}
}
