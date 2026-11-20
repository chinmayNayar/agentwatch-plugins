# AgentWatch for Claude Code

AgentWatch treats your Claude Code sessions like production agents:

- Every session becomes an AgentWatch **run** and every tool call a **step**.
- Shell, write, network and MCP calls are checked **before they run**.
- An operator can **stop the agent** from the AgentWatch console.

The plugin starts in `dry_run`. In this mode it records what it *would* have blocked and blocks nothing, with two exceptions: tools on your passport's denied list, and edits to AgentWatch's own hook files.

## Install

```bash
claude plugin marketplace add chinmayNayar/agentwatch-plugins
claude plugin install agentwatch@agentwatch
```

Then, in Claude Code, run **`/agentwatch:setup`**: it opens the AgentWatch approval page, shows the code, waits while you approve it and stores the token in your OS keychain. (Or set the plugin's `api_key` option.) Until you sign in, the plugin still runs: it queues events on your machine and sends them after sign-in.

Needs Claude Code 2.1.197 or later (tested on 2.1.197 and 2.1.329).

The binary is not put on your PATH. To use it from a terminal, run it by its full path, `~/.claude/plugins/data/agentwatch-agentwatch/bin/agentwatch`, or let `/agentwatch:setup` link it into `~/.local/bin` (it asks first).

## What it runs and sends

On first use (the first session start, MCP server start or hook, whichever comes first), `scripts/aw.sh` downloads the `agentwatch` binary for your platform from this repository's GitHub Release (`github.com/chinmayNayar/agentwatch-plugins/releases`). One download serves every process that starts at once. It checks the file against the SHA-256 in `scripts/checksums.txt`, which ships inside this plugin, and refuses a file that does not match.

The binary talks only to your AgentWatch API (`api_url`, default `https://api.agentwatch.in`).

**It sends:**
- tool names
- commands, after secrets are redacted on your machine
- file paths
- MCP server and tool names
- sizes, exit codes and durations

**It never sends file contents, tool output or your prompts** unless you turn content capture on. [PRIVACY.md](https://github.com/chinmayNayar/agentwatch-plugins/blob/main/PRIVACY.md) lists every field.

## Options

| Option | Default | Meaning |
|---|---|---|
| `api_url` | `https://api.agentwatch.in` | Your AgentWatch API |
| `api_key` | empty | Optional: an AgentWatch API key, kept in your OS keychain |
| `mode` | `dry_run` | type `enforce` to block denied, held and isolated calls; anything else is `dry_run` |

## Skills

`/agentwatch:setup` (sign in), `/agentwatch:status`, `/agentwatch:runs`, `/agentwatch:findings`, `/agentwatch:posture`, `/agentwatch:mode`, `/agentwatch:doctor`.

## What it cannot do

Blocking covers actions that go through Claude Code's hooks. The plugin cannot stop:
- anything a managed `disableAllHooks` or `allowManagedHooksOnly` setting turns off,
- a process the agent starts and that keeps running after the tool call returns.

See [LIMITS.md](https://github.com/chinmayNayar/agentwatch-plugins/blob/main/docs/LIMITS.md).

License: Apache-2.0.
