# Cursor: manual end-to-end checklist

Cursor (desktop or the `cursor-agent` CLI) was not installed on the build machine, so the automated E2E in `tests/e2e/` covers Claude Code only. The Cursor adapter is tested against payloads captured from Cursor 3.19.7 (golden and chaos tests), but not against a live Cursor. Run this before each release.

Setup: a local AgentWatch API, `agentwatch login --api-url http://localhost:8000`, `agentwatch install cursor`, restart Cursor, open a scratch folder, and use a **local** Agent chat.

| # | Do | Expect |
|---|---|---|
| 1 | Ask the agent to run `echo hello` | Console: a run `cursor@<you>` with a `tool_call` step for Shell; `~/.agentwatch/spool` drains |
| 2 | `agentwatch mode dry_run`. Add a pre-phase guardrail denying dangerous shell for `cursor@<you>`. Ask for `echo delete-me` | Runs. Console: a `sensor_would_block` finding |
| 3 | `agentwatch mode enforce`, same request | Cursor shows AgentWatch's reason; the command does not run |
| 4 | Set the passport's `denied_tools` to `["Write"]` and ask the agent to create a file | Refused in both modes |
| 5 | Isolate `cursor@<you>` in the console (enforce). Ask for `echo hi` | Refused: "isolated" |
| 6 | Ask the agent to edit `~/.cursor/hooks.json` | Refused: self-protection |
| 7 | Add `approval_required_tools: ["Shell"]`. Ask for `ls` | Refused with a hold id. Approve in the console, ask again: it runs once |
| 8 | Install the Claude Code hooks too (`agentwatch install claude-code`), repeat 1 | Exactly one step per call (no duplicates) |
| 9 | Stop the API, `enforce` + `--fail-closed`, ask for `ls` | Refused: "AgentWatch could not be reached; fail-closed" |
| 10 | `agentwatch uninstall cursor` | `~/.cursor/hooks.json` is back to its original bytes |

Record the Cursor version and the results in the release notes.
