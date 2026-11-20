# What each client can and cannot block

AgentWatch blocks actions **that go through the client's hooks**. That covers most tool calls, but not everything an agent can do.

This page lists, per client, where blocking stops. Read it before you rely on `enforce`.

## Tested so far (v0.1 preview)

| Client | Status |
|---|---|
| Claude Code | **Tested**: end to end with Claude Code 2.1.328 against an AgentWatch API (`tests/e2e/claude_code_e2e.py`, results in `docs/e2e-claude-code-results.json`) |
| VS Code extension | **Tested**: integration tests on VS Code 1.95.3 and 1.147.0 (`extensions/vscode/test/integration`) |
| Cursor | **Untested** against a running Cursor. Covered by captured and documented hook payloads (`testdata/fixtures/cursor`) and a manual checklist (`docs/checklists/cursor-manual.md`) |
| Codex | **Untested** against a running Codex. Covered by payloads built from Codex's documentation only |
| GitHub Copilot | **Untested** against a running Copilot. Covered by payloads built from Copilot's documentation only |

## Fail-closed needs a settings-file install

- **Fail-closed `enforce`** (a sensor that crashes, hangs or times out blocks the call) needs:
  - a **settings-file install** (`agentwatch install claude-code`, or the editor's Connect panel)
  - with `agentwatch mode enforce --fail-closed`
  - on **Claude Code 2.1.295 or later**, which honours the hook's `onFailure: "block"` (older versions ignore it)
- **The plugin path is fail-open.** Claude Code plugin hooks are fixed in the plugin's `hooks/hooks.json` and cannot switch `onFailure` with the mode. So if the sensor crashes, hangs, or runs past the hook timeout, Claude Code runs the call. The same holds for the Cursor plugin (`failClosed` is set only by a settings-file install).
- **What still blocks on the plugin path:**
  - When the sensor runs and AgentWatch is unreachable, `enforce` + `fail_closed` refuses the call, because the sensor itself decides.
  - When the binary is missing or fails its checksum, the plugin's shim refuses the call in `enforce` + `fail_closed`.
  - These are the sensor's and the shim's own decisions. They do not hold if the shim itself cannot run.

## Everywhere

- **Hooks can be switched off above the user.** Admins or managed settings can do it:
  - Claude Code: managed `disableAllHooks`, `allowManagedHooksOnly`
  - Copilot: organisation hook policy
  - Cursor: Enterprise hook settings

  The sensor cannot override them. The console shows the device as *hooks missing* when the sensor can tell, or *silent* when the sensor stops reporting.
- **Self-protection is a tripwire, not a sandbox.** It blocks agent **writes** to hook files, `~/.agentwatch` and the plugin folders (`rm`, `mv`, `cp`/`rsync` into, `>`/`>>`, `tee`, `sed -i`, `chmod`/`chown`, `ln`, `truncate`, editors, `find -delete`/`-exec rm`, `xargs rm`, `git clean`/`checkout` there), and agent **reads of the credential** (`~/.agentwatch/credentials*`, the keychain entry). Reading the rest (`ls`, `find`, `cat`, `grep` on the plugin folders) is allowed. It reads the shell command (pipes, `&&`, subshells, `$(...)`, quoting, `cd` earlier on the line, `sh -c`), but it does not run it: a path built at runtime gets past it (variables other than `$HOME`, base64, a script file, `grep -r` from a parent directory such as `~`). An interpreter one-liner (`python -c`, `node -e`) that names a protected path is blocked whether it reads or writes.
- **Processes outlive tool calls.** A process the agent starts (a dev server, a background job) keeps running after the tool call returns. Only the call that started it is checked.
- **A sensor that cannot run is a pass on the plugin path.** See [Fail-closed needs a settings-file install](#fail-closed-needs-a-settings-file-install).
- **`dry_run` never blocks.** The two exceptions: tools on the passport's denied list, and self-protection.

## Claude Code

| Can block | Cannot block |
|---|---|
| Every tool call that fires `PreToolUse`: Bash, Edit/Write/MultiEdit, Read, WebFetch, MCP tools, subagent tools | Prompts (the sensor reports them; it does not block them) |
| Held calls: Claude Code asks you, with the hold id | A held call you approve in Claude Code's own prompt runs. AgentWatch records it as `hold_overridden_locally` |
| Approval-bypass launches are **reported** (`permission_mode: bypassPermissions`, launch flags) | They are not prevented: the hooks still run and still block in `enforce` |

Requires Claude Code 2.1.197 or later for the plugin (tested on 2.1.197 and 2.1.329; plugin 0.1.1 needed 2.1.271 for `userConfig` options). On the first session, a pre-tool hook waits at most 3 s for the binary download: a call made before it finishes is not checked. `onFailure: "block"` (settings-file install with fail-closed) requires 2.1.295 or later; older versions ignore it.

## Cursor

| Can block | Cannot block |
|---|---|
| `preToolUse`, shell (`beforeShellExecution`), MCP (`beforeMCPExecution`), file reads (`beforeReadFile`), subagent start | **Tab completions and edits you make yourself** |
| | **Cloud agents** run only project-level hooks: a user-scope install does not cover them |
| | Cursor accepts `ask` on `preToolUse` but **does not enforce it**, so a held call is **refused** with a message. You retry it after approval |
| | `updated_input` (scrub) is ignored for HTTP MCP servers, so a scrub there is a deny |

Cursor also runs Claude Code's hook files by default. With both installed, the sensor detects which client is really running and reports each call once.

## Codex

| Can block | Cannot block |
|---|---|
| Shell (`Bash`, `exec_command`), `apply_patch`, MCP tools and local function tools, through `PreToolUse` | Hosted tools such as web search |
| | Codex has no `ask`: a held call is refused with a message |
| | Codex runs a new or changed hook only after **you trust it** in Codex. Until then nothing is checked, and `agentwatch doctor` cannot see that state |

## GitHub Copilot (VS Code agent, Copilot CLI)

Copilot hooks are in **preview**.

| Can block | Cannot block |
|---|---|
| `preToolUse` (any tool the agent calls) | Inline completions |
| | **Cloud** sessions do not copy local hook files |
| | A held call is refused with a message (Copilot's `ask` is a deny in autopilot and cloud runs anyway) |
| | Organisation policy can disable hooks |

Copilot treats a crashed hook as a deny but a **timed-out** hook as an allow. The sensor therefore decides inside its own timeout, before Copilot's.

## Overhead (measured)

These are measurements, not targets. Real process launches, 500 calls per path, on one Apple silicon Mac (darwin/arm64, 10 cores) on 2026-11-19. They come from `tools/bench`; the raw numbers are in [benchmarks.json](benchmarks.json).

| Decision path | p50 | p95 | p99 | max |
|---|---|---|---|---|
| Read, decided locally | 8.3 ms | 9.6 ms | 10.3 ms | 15.0 ms |
| Denied tool, decided locally (cached passport) | 11.5 ms | 15.8 ms | 20.7 ms | 32.9 ms |
| Shell call checked by the API (local development API on the same machine) | 46.6 ms | 94.0 ms | 135.8 ms | 926.4 ms |

- Starting a minimal Go program on the same machine costs 4.5 ms p50 and 7.6 ms p99, so most of the local cost is process start.
- The API path depends on your network and your API. It is capped by the 800 ms preflight timeout, after which the fail mode decides.
- Windows and Linux have not been measured.

## Isolation and the kill switch

When an operator isolates an agent in the console, every later hook-checked call from that agent is refused in `enforce`. The sensor caches the passport for up to 5 seconds, and a killed run stops its session at the next checked call.

Through hooks this is the same as AgentWatch's SDK path: it stops what goes through the hooks. Only Gate-proxied model and MCP traffic is unbypassable. See AgentWatch's kill-switch documentation for the full statement.
