# Cursor: hooks, third-party hooks, plugins

Read 2026-11-19. Cursor is not installed on the build machine. Real payloads
from Cursor 3.19.7 (macOS) were captured by an earlier private hook and are
sanitised into `testdata/fixtures/cursor/` (paths, emails, prompts, ids replaced).

Sources:
- https://cursor.com/docs/hooks
- https://cursor.com/docs/reference/third-party-hooks
- https://cursor.com/docs/reference/plugins
- Forum: https://forum.cursor.com/t/cursor-agent-imported-claude-code-plugin-hooks-get-a-cursor-shaped-payload-so-pretooluse-gates-fail-open/173171
- Forum: https://forum.cursor.com/t/cursor-agent-windows-imported-claude-code-hooks-are-composed-as-powershell-but-executed-with-bash-silently-blocking-every-tool-call/168129
- Forum: https://forum.cursor.com/t/duplicate-notifications-when-using-claude-hooks-alongside-cursor/158420

## Hook files and precedence

Enterprise (`/Library/Application Support/Cursor/hooks.json`,
`/etc/cursor/hooks.json`, `C:\ProgramData\Cursor\hooks.json`) > Team >
Project (`.cursor/hooks.json`, trusted workspaces only) > User
(`~/.cursor/hooks.json`). Plugin hooks run after all of these. All matching
hooks run; **deny > ask > allow**. Files are watched and reloaded.

Schema: `{"version": 1, "hooks": {"<event>": [{"command": "...", "timeout": <s>,
"failClosed": false, "matcher": "<regex>"}]}}`. `command` is a shell string.
User hooks run from `~/.cursor/`; plugin hooks from the plugin directory
(`stop`/`subagentStop` from the workspace root), with `CURSOR_PLUGIN_ROOT` set.

## Exit / failure semantics

- Exit 0: JSON used. **For permission hooks, invalid JSON or a schema mismatch
  blocks the action even with `failClosed` off.** The sensor must always print
  valid JSON on permission events.
- Exit 2: deny.
- Other exits, crash, timeout: fail open, unless `failClosed: true` (then
  blocks on crash, timeout, non-zero exit and empty output).

## Events the sensor handles

| Event | Can block | Output |
|---|---|---|
| sessionStart | no | `env`, `additional_context` |
| sessionEnd | no | none |
| beforeSubmitPrompt | yes (`continue:false`) | `continue`, `user_message` |
| preToolUse | yes | `permission` allow/deny (**`ask` accepted but not enforced**), `user_message`, `agent_message`, `updated_input` (ignored for HTTP MCP) |
| postToolUse / postToolUseFailure | no | `additional_context` |
| beforeShellExecution | yes | `permission` allow/deny/ask, `user_message`, `agent_message` |
| afterShellExecution | no | none |
| beforeMCPExecution | yes | `permission`, `user_message`, `agent_message` |
| afterMCPExecution | no | none |
| beforeReadFile | yes | `permission` allow/deny, `user_message` |
| afterFileEdit | no | none |
| subagentStart | yes (`ask` = deny) | `permission`, `user_message` |
| stop | no (follow-up only) | `followup_message` |

Common input: `conversation_id`, `generation_id`, `model`, `hook_event_name`,
`cursor_version`, `workspace_roots`, `user_email`, `transcript_path`.
Env: `CURSOR_PROJECT_DIR`, `CURSOR_VERSION`, `CURSOR_USER_EMAIL`,
`CURSOR_PLUGIN_ROOT` (plugin hooks), `CLAUDE_PROJECT_DIR` (alias).

`user_email` arrives on every payload. The sensor never forwards it; the
agent name uses the AgentWatch account, not the Cursor login.

## Cloud agents

Only project `.cursor/hooks.json` runs in cloud agents (no user hooks), and
not `sessionStart`/`sessionEnd`/MCP hooks. A user-scope install covers local
agents only. LIMITS.md says so.

## Third-party (Claude Code) hooks: double-firing risk

Cursor reads `.claude/settings.local.json`, `.claude/settings.json` and
`~/.claude/settings.json` by default ("Include Third-Party Plugins, Skills,
and Other Configs", on by default). Mapped events: PreToolUse, PostToolUse,
UserPromptSubmit→beforeSubmitPrompt, Stop, SubagentStop, SessionStart,
SessionEnd, PreCompact. Output fields are mapped
(`permissionDecision`→`permission`, `updatedInput`→`updated_input`).

Forum reports: the imported hook receives a **Cursor-shaped payload** (so a
Claude-shaped parser fails open), and on Windows the imported command was run
with the wrong shell, blocking every call.

**Design consequence:** the binary detects the client from the payload and
environment (`cursor_version`, `CURSOR_VERSION`), never from argv alone. A
`hook claude-code` invocation that is really running inside Cursor yields to
the Cursor adapter when the Cursor hooks are installed (no double events), and
handles the event as Cursor otherwise.

## Plugins and marketplace

`.cursor-plugin/plugin.json`: `name` (kebab-case, required), `description`,
`version`, `author`, `homepage`, `repository`, `license`, `keywords`, `logo`
(relative path), `hooks`, `mcpServers`, `rules`, `skills`, `commands`.
`hooks/hooks.json` and `mcp.json` at the plugin root are found automatically.
MCP fields expand `${CURSOR_PLUGIN_ROOT}` / `${CLAUDE_PLUGIN_ROOT}`.
Multi-plugin repo: `.cursor-plugin/marketplace.json` with `name`, `owner`,
`plugins[]` (`name`, `source`).

Submission: public repo, then https://cursor.com/marketplace/publish with the
repository link. Requirements: valid manifest, README, relative paths only,
logo optional.
