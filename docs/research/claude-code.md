# Claude Code: hooks, plugins, marketplaces

Read 2026-11-19. Local CLI on the build machine: `claude` 2.1.197 (older than
several features below; each version gate is noted).

Sources:
- https://code.claude.com/docs/en/hooks
- https://code.claude.com/docs/en/plugins/manifest-reference
- https://code.claude.com/docs/en/plugins/publish
- https://code.claude.com/docs/en/plugin-marketplaces
- https://claude.com/docs/plugins/platform-support
- https://claude.com/docs/plugins/submit
- https://claude.com/docs/plugins/pre-submission-checklist

## Hook input (stdin JSON)

Common fields on every event: `session_id`, `prompt_id` (absent before first
input), `transcript_path`, `cwd`, `scratchpad_dir` (v2.1.257+),
`permission_mode` (`default|plan|acceptEdits|auto|dontAsk|bypassPermissions`),
`effort` (`{level}`), `hook_event_name`, `agent_id`/`agent_type` (subagents).

| Event | Extra fields |
|---|---|
| SessionStart | `source` (`startup|resume|clear|compact|fork`), optional `model`, `agent_type`, `session_title` |
| UserPromptSubmit | `prompt`, optional `session_title` |
| PreToolUse | `tool_name`, `tool_input`, `tool_use_id` |
| PermissionRequest | `tool_name`, `tool_input` (no `tool_use_id`), optional `permission_suggestions` |
| PostToolUse | `tool_name`, `tool_input`, `tool_response`, `tool_use_id`, optional `duration_ms` |
| PostToolUseFailure | as PostToolUse but `error` (string), optional `is_interrupt`, `duration_ms` |
| Notification | `message`, optional `title`, `notification_type` |
| Stop | `stop_hook_active`, `last_assistant_message`, `background_tasks`, `session_crons` |
| SubagentStop | `stop_hook_active`, `agent_id`, `agent_type`, `agent_transcript_path`, `last_assistant_message` |
| SessionEnd | `reason` (`clear|resume|logout|prompt_input_exit|other`) |

`permission_mode: "bypassPermissions"` is how the sensor sees
`--dangerously-skip-permissions` (AW-D05), without reading the process table.

## Hook output

- Exit 0 + one JSON object on stdout: fields apply. Exit 2: blocking error,
  stderr is the reason (PreToolUse: blocks the call). Any other exit: non-blocking
  error, the action proceeds **unless `onFailure: "block"`** (v2.1.295+).
- PreToolUse `hookSpecificOutput`: `permissionDecision` = `allow|deny|ask|defer`,
  `permissionDecisionReason` (deny: shown to Claude; ask: shown to the user),
  `updatedInput` (replaces the whole input; with allow or ask),
  `additionalContext`. `defer` only in `-p` mode.
- Several PreToolUse hooks: **deny > defer > ask > allow**. Another plugin's
  allow cannot override our deny.
- PostToolUse: top-level `decision: "block"` + `reason`; `additionalContext`.
- Output cap 10,000 chars.

## Hook config

```json
{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [
  {"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/scripts/aw.sh",
   "args": ["hook", "claude-code"], "timeout": 5, "onFailure": "continue"}]}]}}
```

- `args` present = exec form (no shell). Absent = shell form (`sh -c`, Git Bash or PowerShell).
- `timeout` is in seconds. Default **600 s** for command hooks (30 s on
  UserPromptSubmit; SessionEnd hooks share 1.5 s unless raised, max 60 s).
- `async: true` (command hooks only): runs in background; output ignored;
  **in `-p` mode an async hook still running at teardown is killed**.
- Matcher: `*`/empty = all; `Edit|Write` exact list; other chars = JS regex.
- Hooks merge across user/project/local/managed/plugin settings; identical
  handlers dedupe except plugin copies. Matching hooks run in parallel.
- `disableAllHooks: true` turns every non-managed hook off; managed
  `allowManagedHooksOnly` blocks plugin hooks unless the plugin is force-enabled.
  The sensor cannot defeat either. That is a LIMITS.md row, not a bug.
- Env exported to hooks: `CLAUDE_PROJECT_DIR`, `CLAUDE_PLUGIN_ROOT`,
  `CLAUDE_PLUGIN_DATA`, `CLAUDE_PLUGIN_OPTION_<KEY>` (uppercased).

## Plugins

- Manifest `.claude-plugin/plugin.json`; only `name` required. Name is
  permanent (`name@marketplace` is the install id).
- `userConfig.<key>`: `type`, `title`, `description` required; `default`,
  `required`, `sensitive`, `options` (**v2.1.271+; older versions can't load
  the plugin at all if any field declares `options`**). Unknown keys inside an
  option = load failure.
- Sensitive values go to the OS secure store, never `settings.json`.
- `${user_config.KEY}` only in exec-form `args`, MCP config, skills. Shell-form
  hooks may not reference it (error); they read `CLAUDE_PLUGIN_OPTION_<KEY>`.
- `${CLAUDE_PLUGIN_DATA}` = `~/.claude/plugins/data/<id>/`, survives updates,
  deleted on uninstall (unless `--keep-data`). `${CLAUDE_PLUGIN_ROOT}` moves
  on every update: never write state there.
- **`bin/` at plugin root: claude.ai and Cowork refuse to install the plugin.** Not used.
- Hooks load on Claude Code and Cowork; ignored in claude.ai chat.
- Local stdio MCP servers: Claude Code and Cowork (on your computer) only.

## Marketplace

`.claude-plugin/marketplace.json`: `name`, `owner.name`, `plugins[]` with
`name` + `source` (`"./plugins/claude-code"` relative to the repo root).
Entry name must equal the plugin manifest name. Install:
`claude plugin marketplace add <owner>/agentwatch-plugins` then
`claude plugin install agentwatch@agentwatch`.

## Anthropic directory (what the user called "community marketplace")

Correction (2026-11-20, from the maintainer): individual accounts submit at
https://platform.claude.com/plugins/submit. The claude.ai directory portal
(https://claude.ai/directory/manage) needs a Team or Enterprise plan. Notes
below are from that portal's documentation (GitHub account connected; repo
public before going live). One submission per plugin folder; plugin path
`plugins/claude-code`. Steps: Source → Validate → Listing details → Data
handling questions → Compliance (4 acknowledgements) → Review and submit.

Directory checks that shape our design:

| Rule | Result | Our choice |
|---|---|---|
| README ≥ 40 words and LICENSE **inside the plugin folder** | Blocks | both in `plugins/claude-code/` |
| Hook command paths written from `${CLAUDE_PLUGIN_ROOT}` with no other variable | Blocks | `${CLAUDE_PLUGIN_ROOT}/scripts/aw.sh` |
| Scripts a hook runs that use other variables or call other files | Held for reviewer | `aw.sh` reads `CLAUDE_PLUGIN_DATA` and runs the downloaded binary: **will be held**; disclosed in README |
| Compiled binaries in the plugin folder | Held; 256 KiB/file cap | binary is downloaded, not committed |
| Behaviour not described in README (sends data, fetches code) | Security-scan failure | README lists the download URL, the API host, and links PRIVACY.md |
| `.DS_Store` in plugin folder | Blocks | `.gitignore` + CI check |
| No real credentials in any file | Blocks | `api_key` is `userConfig` sensitive |
