# AgentWatch for coding agents and editors

> **v0.1 is a preview: Claude Code + VS Code.**
> - Those two are tested end to end.
> - **Cursor, Codex and GitHub Copilot are untested** against the real clients. They are built from captured or documented hook payloads.
> - [LIMITS.md](docs/LIMITS.md#tested-so-far-v01-preview) has the detail.

AgentWatch treats the coding agents on your machine (Claude Code, Cursor, Codex and GitHub Copilot) the way it treats production agents:

- **Every session is a run**, every tool call a step, in the AgentWatch console.
- **Risky calls are checked before they run.** Reads are decided on your machine. Shell, write, network and MCP calls ask AgentWatch first (800 ms timeout).
- **An operator can stop an agent.** Isolating it in the console refuses its next hook-checked tool call.
- **Nothing leaves your machine unredacted**, and file contents, tool output and prompts are not sent at all by default ([PRIVACY.md](PRIVACY.md)).

Everything starts in **`dry_run`**: it records what it *would* block and blocks nothing, except tools on the agent's denied list and edits to AgentWatch's own hook files. [LIMITS.md](docs/LIMITS.md) says exactly what each client can and cannot block.

## Install

**Claude Code** (plugin, needs Claude Code 2.1.271+):

```bash
claude plugin marketplace add chinmayNayar/agentwatch-plugins
claude plugin install agentwatch@agentwatch
```

**VS Code, Cursor, Windsurf, VSCodium:** install **AgentWatch** (`agentwatch.agentwatch`) from the VS Code Marketplace or Open VSX. Then run *AgentWatch: Connect coding agents*. The extension carries the binary and connects every client on the machine.

**Any client, from a terminal:** download `agentwatch` for your platform from the [latest release](https://github.com/chinmayNayar/agentwatch-plugins/releases) and check it against `SHA256SUMS`. Then:

```bash
agentwatch login              # device code: approve it in the AgentWatch console
agentwatch install all        # user scope; prints what it changed, keeps a backup
agentwatch doctor             # green / red per client
```

`agentwatch uninstall all` removes everything it added, restoring the original files when you have not changed them since.

## Modes

| | `dry_run` (default) | `enforce` |
|---|---|---|
| Denied tool (passport) | blocked | blocked |
| Agent edits AgentWatch's own hooks | blocked | blocked |
| Server says deny | allowed, recorded as "would have blocked" | blocked |
| Server holds for approval | allowed, recorded | refused until approved (Claude Code: asks you) |
| Agent isolated in the console | allowed, recorded | every checked call refused |
| AgentWatch unreachable | allowed, recorded | `fail_open` (default): allowed, recorded; `fail_closed`: blocked |

Change it from a terminal, which asks you to confirm: `agentwatch mode enforce [--fail-closed]`. Agents are blocked from running that command.

**Fail-closed needs a settings-file install.** Here, fail-closed means a sensor that crashes or hangs blocks the call. It needs `agentwatch install claude-code` (or the editor's Connect panel) on Claude Code 2.1.295 or later. The plugin path is fail-open: if the sensor crashes or times out, the call runs. See [LIMITS.md](docs/LIMITS.md#fail-closed-needs-a-settings-file-install).

## What's here

| Path | What |
|---|---|
| `cmd/agentwatch`, `internal/` | the sensor (Go, one static binary per platform) |
| `plugins/claude-code` | Claude Code plugin: hooks, MCP server, `/agentwatch:*` skills |
| `plugins/cursor` | Cursor plugin |
| `extensions/vscode` | VS Code / Cursor / Windsurf / VSCodium extension |
| `docs/` | design, per-client guides, limits, research notes |
| `testdata/` | captured and documented hook payloads, golden outputs |

## Measured overhead

Measured, not targets. Real process launches on one Apple silicon Mac, 500 calls each (`tools/bench`; raw numbers in [docs/benchmarks.json](docs/benchmarks.json)):

| Decision | p50 | p99 |
|---|---|---|
| Read, decided locally | 8.3 ms | 10.3 ms |
| Denied tool, decided locally (cached passport) | 11.5 ms | 20.7 ms |
| Shell call checked by the API (local development API) | 46.6 ms | 135.8 ms |

On the same machine, starting a minimal Go program costs 4.5 ms p50 and 7.6 ms p99. The server path depends on your API; most of it is the API's own time. Windows and Linux have not been measured.

## Docs

- [Design](docs/DESIGN.md)
- [Privacy](PRIVACY.md)
- [Limits](docs/LIMITS.md)
- [Security](SECURITY.md)
- Guides: [Claude Code](docs/claude-code.md), [Cursor](docs/cursor.md), [Codex](docs/codex.md), [Copilot](docs/copilot.md)

License: [Apache-2.0](LICENSE).
