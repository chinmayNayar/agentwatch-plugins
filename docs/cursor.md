# Cursor

> **Untested against the real client (as of 0.2.0).** This client has not yet been run against AgentWatch end to end. The sensor's handling is tested with captured and documented Cursor hook payloads (a manual checklist is in [checklists/cursor-manual.md](checklists/cursor-manual.md)). Report problems at https://github.com/chinmayNayar/agentwatch-plugins/issues.

Two ways in:

- **Plugin:** `plugins/cursor`. It adds hooks and the read-only MCP server. On first use it downloads the pinned binary into `~/.agentwatch/plugin-data`.
- **Settings file:** `agentwatch install cursor` (or the editor extension's Connect panel) writes `~/.cursor/hooks.json`, merged with your own hooks and with a backup.

Sign in with `agentwatch login` (plugin install: `~/.agentwatch/plugin-data/bin/agentwatch login`, since the plugin's binary is not on PATH; `... link` adds it to `~/.local/bin`).

## Behaviour

- **One chat is one run** (`conversation_id`).
- **Checked before they run:** `preToolUse`, shell, MCP and file-read calls.
- **Held calls are refused** with instructions: Cursor does not enforce `ask`. Approve the hold in AgentWatch, then retry.
- **fail-closed:** in `enforce` + `fail_closed` the hooks get `failClosed: true`, so a sensor that cannot run blocks.
- **Your Cursor email is dropped.** It arrives in every hook payload and is never sent to AgentWatch.

## Already using Claude Code hooks?

Cursor runs `~/.claude/settings.json` hooks too. With both installed, the sensor sees it is inside Cursor and lets the Cursor install report the call, so nothing is counted twice.

## Not covered

Tab edits, your own edits, and cloud agents (they run only project hooks). See [LIMITS.md](LIMITS.md).
