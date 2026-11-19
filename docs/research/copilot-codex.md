# GitHub Copilot (VS Code) and OpenAI Codex hooks

Read 2026-11-19. Neither client is installed on the build machine, so their
fixtures in `testdata/fixtures/{copilot,codex}/` are built from the documented
schemas below and marked `"_source": "docs"`. They are not captured payloads.

## Copilot (VS Code agent, Copilot CLI, Copilot app share one harness)

Sources:
- https://code.visualstudio.com/docs/agent-customization/hooks
- https://code.visualstudio.com/docs/agents/reference/hooks-reference (the "Local" harness, deprecated, removed February 2027)
- https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-hooks-reference

Status: **Preview** ("might change before it becomes generally available").

Locations: workspace `.github/hooks/*.json`; user `~/.copilot/hooks/*.json`
(or `$COPILOT_HOME/hooks/`); inline `hooks` in `~/.copilot/settings.json`;
policy `/etc/github-copilot/policy.d/*.json`. The Copilot harness also reads
**repository** `.claude/settings.json` and `.claude/settings.local.json`
(not `~/.claude/settings.json`). A user-scope Claude Code install is
therefore not picked up by Copilot; a `--project` Claude install would be,
and the client detection described in `cursor.md` applies.

File: `{"version": 1, "hooks": {"preToolUse": [{"type": "command",
"bash": "...", "powershell": "...", "timeoutSec": 30}]}}`. `exec`+`args`
(no shell) is CLI-only. Default `timeoutSec` 30.

camelCase events and payloads:

| Event | Input |
|---|---|
| sessionStart | `sessionId`, `timestamp`, `cwd`, `source` (`startup|resume|new`), `initialPrompt` |
| userPromptSubmitted | `prompt` |
| preToolUse | `toolName`, `toolArgs` (type unknown: object or JSON string) |
| postToolUse | `toolName`, `toolArgs`, `toolResult` (`resultType`, `textResultForLlm`) |
| postToolUseFailure | `toolName`, `toolArgs`, `error` |
| errorOccurred | `error` (`message`, `name`, `stack`), `errorContext`, `recoverable` |
| sessionEnd | `reason` (`complete|error|abort|timeout|user_exit`) |

The PascalCase aliases (`PreToolUse`) receive snake_case Claude-style fields
instead. **Do not register both casings: both fire.** The installer writes
camelCase only.

preToolUse output (top level): `permissionDecision` (`allow|deny|ask`),
`permissionDecisionReason` (required for deny), `modifiedArgs`. `{}` = no
decision. Cloud agent and CLI autopilot treat `ask` as deny.

Failure: **command preToolUse fails closed on crash or any non-zero exit**,
but **timeouts fail open** for every event. The sensor must never exit
non-zero in dry_run, and must apply fail_closed itself (print deny) before
the client's timeout.

Organisation policy can restrict hooks; hooks "are not a substitute for
enforced permissions, sandboxing, or enterprise controls".

## Codex

Source: https://developers.openai.com/codex/hooks (redirects to
https://learn.chatgpt.com/docs/hooks).

Locations: user `$CODEX_HOME/hooks.json` (default `~/.codex/hooks.json`,
Windows `%USERPROFILE%\.codex\hooks.json`); project `.codex/hooks.json`
(trusted projects); system `/etc/codex/hooks.json`; inline `[hooks]` in
`config.toml`. On by default; `[features] hooks = false` turns them off.

Schema: event → matcher group → handlers (`type: "command"`, `command`
string, `timeout` seconds, default 600; `commandWindows`; `async`).

Events: SessionStart, SessionEnd (timeout default 1 s, max 3), PreToolUse,
PermissionRequest, PostToolUse, UserPromptSubmit, Stop, SubagentStart/Stop,
PreCompact/PostCompact, PreModelRequest/PostModelRequest, Interrupt.

Input: common `session_id`, `transcript_path`, `cwd`, `hook_event_name`,
`model`; PreToolUse adds `turn_id`, `tool_name`, `tool_use_id`, `tool_input`
(Bash and `apply_patch` put the command in `tool_input.command`; MCP tools
are `mcp__server__tool`).

Coverage: shell (`Bash`), `exec_command`, `apply_patch`
(`apply_patch|Edit|Write`), MCP tools, local function tools. **Not hosted
tools such as WebSearch.**

PreToolUse output: `hookSpecificOutput.permissionDecision: "deny"` +
reason; `allow` + `updatedInput` (must keep a string `command` for Bash and
apply_patch); **`ask` not supported**. An unsupported field marks the hook
failed and the tool call continues: the sensor emits only documented fields.

**Trust:** non-managed hooks must be reviewed and trusted in Codex; trust is
tied to the hook's hash, so a changed hook is skipped until re-trusted.
`agentwatch install codex` tells the user to trust it, and `doctor` cannot
see Codex's trust state (LIMITS.md).
Codex calls tool hooks "a useful guardrail, not a complete enforcement boundary."
