# AgentWatch for Cursor

> **Untested in the v0.1 preview.** This client has not yet been run against AgentWatch end to end. The sensor's handling is tested with captured and documented Cursor hook payloads (a manual checklist is in `docs/checklists/cursor-manual.md`). Report problems at https://github.com/chinmayNayar/agentwatch-plugins/issues.

AgentWatch treats your Cursor agent chats like production agents:

- Every chat becomes an AgentWatch **run** and every tool call a **step**.
- Shell, MCP, file-read and write calls are checked **before they run**.
- An operator can **stop the agent** from the AgentWatch console.

The plugin starts in `dry_run`. In this mode it records what it *would* have blocked and blocks nothing, except tools on the passport's denied list and edits to AgentWatch's own hook files.

## Install

1. Install the plugin from the Cursor marketplace, or add this repository as a plugin source.
2. In a terminal, sign the machine in with `agentwatch login`.
3. Run `agentwatch doctor` to check that everything is green.

## What it runs and sends

On the first session start, `scripts/aw.sh` downloads the `agentwatch` binary for your platform from this repository's GitHub Release. It checks the file against the SHA-256 in `scripts/checksums.txt`, which ships inside this plugin, and refuses a file that does not match.

The binary talks only to your AgentWatch API (default `https://api.agentwatch.in`).

**It sends:**
- tool names
- commands, after secrets are redacted on your machine
- file paths
- MCP server and tool names
- sizes, exit codes and durations

**It never sends file contents, tool output, your prompts or your Cursor email address** unless you turn content capture on. Every field is listed in [PRIVACY.md](https://github.com/chinmayNayar/agentwatch-plugins/blob/main/PRIVACY.md).

## Limits

- **`ask` is not enforced.** Cursor accepts `ask` on `preToolUse` but does not enforce it, so a call AgentWatch holds for approval is refused with a message. You retry it after it is approved.
- **Cloud agents.** They run only project-level hooks, so this user-level plugin covers local agents only.

See [LIMITS.md](https://github.com/chinmayNayar/agentwatch-plugins/blob/main/docs/LIMITS.md).

License: Apache-2.0.
