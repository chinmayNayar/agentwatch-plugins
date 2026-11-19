// Package adapters picks the adapter for a hook invocation and detects the
// client that is really running it.
//
// Detection matters because Cursor runs Claude Code's hook files by default
// and Copilot runs a repository's .claude/settings.json. A `hook claude-code`
// invocation can therefore be Cursor (with a Cursor-shaped payload). The
// payload and environment decide, never argv alone.
package adapters

import (
	"fmt"
	"os"

	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/claudecode"
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/codex"
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/common"
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/copilot"
	"github.com/chinmayNayar/agentwatch-plugins/internal/adapters/cursor"
	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

// Clients in a stable order.
var Clients = []string{claudecode.Name, cursor.Name, codex.Name, copilot.Name}

// Known reports whether name is a supported client.
func Known(name string) bool {
	for _, c := range Clients {
		if c == name {
			return true
		}
	}
	return false
}

// Detect returns the client actually running this hook. declared is the
// client named on the command line.
func Detect(declared string, raw map[string]any) string {
	// Only a claude-code invocation is ambiguous: Cursor and Copilot import
	// Claude Code's hook files, never the other way round.
	switch declared {
	case cursor.Name, codex.Name, copilot.Name:
		return declared
	}
	if _, ok := raw["cursor_version"]; ok || os.Getenv("CURSOR_VERSION") != "" {
		return cursor.Name
	}
	// Copilot's own format is camelCase with no hook_event_name.
	if _, ok := raw["sessionId"]; ok {
		if _, ok2 := raw["hook_event_name"]; !ok2 {
			return copilot.Name
		}
	}
	if _, ok := raw["turn_id"]; ok {
		return codex.Name
	}
	return claudecode.Name
}

// Parse dispatches to the client's parser. hookEvent is only used by
// Copilot, whose payload does not name its event.
func Parse(client string, raw map[string]any, hookEvent string) (*event.Event, error) {
	switch client {
	case claudecode.Name:
		return claudecode.Parse(raw), nil
	case cursor.Name:
		return cursor.Parse(raw), nil
	case codex.Name:
		return codex.Parse(raw), nil
	case copilot.Name:
		return copilot.Parse(raw, hookEvent), nil
	}
	return nil, fmt.Errorf("unknown client %q", client)
}

// Render dispatches to the client's output format.
func Render(ev *event.Event, v event.Verdict) []byte {
	switch ev.Client {
	case claudecode.Name:
		return claudecode.Render(ev, v)
	case cursor.Name:
		return cursor.Render(ev, v)
	case codex.Name:
		return codex.Render(ev, v)
	case copilot.Name:
		return copilot.Render(ev, v)
	}
	return nil
}

// Neutral is the client's "no opinion" answer for an event the sensor does
// not handle (unparseable input, yielding to another install). For Cursor's
// permission hooks it must still be valid JSON with a permission.
func Neutral(client, hookEvent string) []byte {
	switch client {
	case cursor.Name:
		return cursor.Neutral(hookEvent)
	case copilot.Name:
		return []byte("{}")
	}
	return nil
}

// FailClosed is the deny a client gets when the sensor itself cannot decide
// (binary missing, unreadable input) in enforce + fail_closed.
func FailClosed(client, hookEvent, reason string) []byte {
	ev := &event.Event{Client: client, HookEvent: hookEvent, Kind: event.PreTool, CanBlock: true}
	if client == cursor.Name && !cursor.IsControl(hookEvent) {
		return []byte("{}")
	}
	return Render(ev, event.Verdict{Decision: event.Deny, Reason: reason})
}

var _ = common.Str
