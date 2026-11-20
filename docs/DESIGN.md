# AgentWatch for coding agents and editors: design

Status: accepted, 2026-11-19. Implements ADR-001 with the changes listed in
[What research changed](#what-research-changed). Research notes, with URLs and
dates, are in `docs/research/`.

## Goal

Give a developer's coding agents (Claude Code, Cursor, Codex, GitHub Copilot
in VS Code) the same treatment AgentWatch gives production agents:

- every session becomes a run,
- every tool call becomes a step,
- calls that matter are checked before they run,
- an operator can stop the agent from the dashboard.

The cost must stay small enough that people leave it on.

## Components

| Piece | What it is |
|---|---|
| `agentwatch` | One static Go binary (darwin-arm64, darwin-x64, linux-x64, linux-arm64, windows-x64). Subcommands: `hook`, `install`, `uninstall`, `doctor`, `login`, `logout`, `status`, `mode`, `mcp`, `version` |
| Claude Code plugin | `plugins/claude-code/`: hooks, a stdio MCP server (`agentwatch mcp`), six skills. The binary is downloaded on first use and checked against a SHA-256 pinned in the plugin |
| Cursor plugin | `plugins/cursor/`: hooks + MCP, same binary |
| VS Code extension | `extensions/vscode/`: one VSIX per platform with the binary inside, a sign-in flow, a Connect panel that installs hooks for every client on the machine, and views of runs, findings, approvals, Gate and audit |
| AgentWatch API (private repo) | Device enrollment, device-scoped tokens, a sensor policy endpoint, heartbeats, sensor security events, and coding-agent fields on `/steps/preflight` and `/ingest` |

## The decision path

```
hook stdin ──► adapter (detect client, normalise) ──► policy
                                                       │
       self-protection rule hit ───────────────────────┼─► DENY (every mode)
       denied_tools / isolated passport (local cache) ─┼─► DENY (enforce), recorded (dry_run, except denied_tools: always deny)
       read-only tool and not strict ──────────────────┼─► ALLOW now, report async
       shell / write / network / MCP, or strict ───────┴─► POST /steps/preflight (timeout 800 ms)
                                                              ├─ answer   ─► verdict → client output
                                                              └─ timeout/error ─► fail mode + security event
```

- **Passport cache.** `~/.agentwatch/state/policy-<agent>.json` is refreshed by
  `GET /devices/policy`. Refresh triggers: at session start, when the cache is
  older than 5 s at a hook call, and right after any server verdict. The hook
  never waits for a refresh on a read-only call. It uses the cached copy and a
  background refresh updates it.
- **Classes.** Each adapter maps the client's tool names to `read`
  (Read/Grep/Glob/LS/WebSearch-free reads), `write` (Edit/Write/Delete/
  apply_patch/NotebookEdit), `shell` (Bash/Shell/exec_command), `network`
  (WebFetch, curl/wget in shell), `mcp` (`mcp__s__t`, `MCP:`), `meta`
  (TodoWrite, Task/subagents). An unknown tool is treated as `shell`, so it
  gets a server check rather than a free pass.
- **Strict** (`agentwatch mode --strict`): every call, reads included, goes to preflight.

## Verdicts and what each client gets

The server's `/steps/preflight` speaks `allow | warn | require_approval |
deny`. This project adds `hold` and `scrub` for sensor callers (the API change
is listed under "API contract"). `warn` is treated as allow plus a note.
`reroute` is a Gate model-routing verdict and never applies to a tool call.

| Verdict | Claude Code | Cursor | Codex | Copilot |
|---|---|---|---|---|
| ALLOW | `permissionDecision: allow`* | `permission: allow` | no output | `{}` |
| SCRUB | `allow` + `updatedInput` | `allow` + `updated_input` (not for HTTP MCP: deny) | `allow` + `updatedInput` | `allow` + `modifiedArgs` |
| HOLD | `ask` + reason with the hold id | **deny** "awaiting approval in AgentWatch (hold …); retry after approval" | deny, same text (`ask` unsupported) | deny, same text |
| DENY | `deny` + reason | `deny` + `user_message`/`agent_message` | `deny` + reason | `deny` + reason |
| isolated (enforce) | deny every call | deny | deny | deny |

\* ALLOW is emitted as *no decision* (empty `hookSpecificOutput`) unless the
server explicitly allowed. A sensor allow must never skip the client's own
permission prompt, which is what `permissionDecision: "allow"` does in
Claude Code. AgentWatch adds checks; it does not remove the user's.

A retry after approval: the call is identical (same tool, same normalised
input hash), the sensor sends `hold_id`, and the server allows once and marks
the approval consumed.

**dry_run** never blocks. The local verdict is computed anyway, the sensor
allows, and the server records `would_have_blocked` with the verdict. The
two exceptions apply in every mode: `denied_tools` (the platform's own rule:
"denied tools are enforced from the start") and self-protection.

**Defaults:** `dry_run` + `fail_open`. `agentwatch mode enforce` and
`--fail-closed` ask for confirmation on a TTY (or `--yes`), and print what
will now be blocked.

## Failure modes (client side)

The sensor never crashes the client, never exits non-zero on a permission
event, and never prints a secret.

- Copilot treats a non-zero exit from `preToolUse` as deny.
- Cursor treats invalid JSON from a permission hook as deny.

So every path ends in a valid, documented JSON object or empty output.

| Fault | dry_run / fail_open | enforce + fail_closed |
|---|---|---|
| API down, timeout (>800 ms), 5xx, 429, invalid JSON | allow; `sensor_preflight_unavailable` event spooled | deny "AgentWatch unreachable (fail-closed)"; event spooled |
| 401 (token revoked) | allow; doctor shows red | deny |
| 403 `agent_isolated` / passport isolated | allow, recorded | deny every call |
| 409 run stopped (killed from dashboard) | allow, recorded | deny every call in that session |
| clock skew | no effect: the sensor sends no signed timestamps; server time is used | same |
| full disk | spool write fails → event dropped, counter kept in memory; decision unaffected | same |
| corrupt config / state | defaults used (dry_run, fail_open), doctor shows red | the last good mode is cached in the state file; if both are unreadable: dry_run (a broken file must not lock the developer out) and doctor shows red |
| keychain missing | file fallback `~/.agentwatch/credentials` (0600) | same |
| binary missing (plugin first run, download failed) | shim prints nothing, exit 0 | shim prints deny JSON |

The hook-level setting on top: the binary decides its own fail mode before
the client's timeout. For settings-file installs in enforce + fail_closed the
installer also writes Claude Code `onFailure: "block"` (v2.1.295+) with
`timeout: 5`, and Cursor `failClosed: true` with `timeout: 5`, so a binary
that cannot even start is a block, not a pass. No install leaves Claude
Code's 600 s default timeout.

## Reporting never blocks

- Each hook appends one event to `~/.agentwatch/spool/` (append-only segment,
  `O_APPEND`, one JSON line, ids are random 128-bit) and, if no flusher holds
  the lock, starts a detached `agentwatch flush` process. The hook returns.
- The flusher sends batches through `POST /ingest`, which carries an
  `event_id`; the server ignores an `event_id` it already has for that run.
  It retries with backoff (5xx, 429, network) and drops into `dead/` on
  other 4xx. Bounds: 20 MB or 20,000 events; oldest segments are dropped
  first and the drop count is reported in the next heartbeat.
- Claude Code reporting hooks (PostToolUse, PostToolUseFailure, Stop,
  SubagentStop, UserPromptSubmit, Notification) are `async: true`. In `-p`
  mode Claude Code kills async hooks at teardown, which is why the hook only
  spools and the detached flusher does the network work.

## Privacy

What the sensor sends is listed field by field in `PRIVACY.md`.

**Sent by default:**
- tool name and class
- the command (redacted)
- the file path
- the MCP server and tool names
- sizes, exit codes and durations
- model name and session id
- the client and its version
- the OS
- a hashed repo name
- the device id

**Never sent by default:**
- file contents
- tool output
- prompt text
- assistant messages

Prompt and assistant text are sent only as a length and a SHA-256 prefix.

Client-side redaction runs before anything is spooled. It covers:
- known key formats (AgentWatch, OpenAI, Anthropic, AWS, GitHub, Slack, Stripe, Google, JWT)
- private-key blocks
- connection strings with credentials
- `KEY=value` pairs whose key looks secret
- `Authorization:` and `--password` style arguments
- `.env` values

`content_capture: true` sends prompt text and tool output (both redacted).
It requires `approver` to be set, which reports as AW-P11 on the dashboard.
`repo_names: plain` opts in to sending repo names unhashed.

## Identity

- `agentwatch login`: device-code flow. It calls `POST /devices/code` and
  shows the user code. The user approves at `/devices/activate` on the
  dashboard. It then polls `POST /devices/token` and receives `awdv_…`, a
  per-device token stored hashed on the server.
- **Device scope.** The token can make these calls, and nothing else:
  - `POST /runs`, `/ingest`, `/devices/heartbeat`, `/devices/events`
  - `POST /steps`, `/steps/preflight` into runs this device created
  - `GET /runs/{id}…` for runs this device created, `GET /devices/runs` (its own runs), and `GET /devices/policy` for the agent names it reports as

  Anything else returns 403 `device_scope`. That includes another device's runs, tenant-wide lists, findings, posture, audit and evidence. A leaked token does not open the account. Two devices never share a session run. The MCP `findings` and `posture` tools, and the extension's findings, approvals, Gate and audit views, need an account key or operator sign-in. (Changed 2026-11-20: until then the token could make any read.)
- **Revocation.** `DELETE /devices/{id}` from the dashboard revokes the token.
- **Storage.** The token is kept in the OS keychain: macOS Keychain, Windows
  Credential Manager, or Secret Service on Linux. If none is available it
  falls back to the 0600 file.
- **API-key paste** (`agentwatch login --api-key`) is the fallback. The
  Claude Code plugin's sensitive `api_key` option also works.
- **Agent name.** `<client>@<user>`, where `<user>` is the device's label
  (default: OS username), e.g. `claude-code@dev`.
- **Run tags.** `client:<c>`, `client_version:<v>`, `os:<os>`,
  `repo:<sha256-12>` (or the name if opted in), `device:<id>`.
- One session / conversation is one run (`session_id` → `/runs`). Cursor
  keys on `conversation_id`, as the existing hook does.

## Self-protection

These are denied in every mode unless `self_protect: false` is set in the
config file, which an agent cannot reach because of the same rule:

- an agent write, edit or delete
- a shell command that writes, moves or deletes there (`>`, `>>`, `tee`,
  `sed -i`, `rm`, `mv`, `cp … <dest>`, `truncate`, `chmod`, `ln`, editors,
  `find -delete` / `-exec rm`, `xargs rm`, `git clean` / `checkout`)
- any agent read of the credential (`~/.agentwatch/credentials*`, the
  keychain entry), so a hijacked agent cannot take the device token

Reads of the other protected paths are allowed. The check parses the
command (internal/selfprotect/shell.go: quoting, pipes, `&&`, subshells,
`$(...)`, heredocs, redirections, `cd`, `sh -c`) and looks at what each
command writes; a command it cannot parse falls back to the old rule
(any write verb plus any protected path blocks).

The protected paths:
- `~/.claude/settings*.json`, `.claude/settings*.json`
- `~/.cursor/hooks.json`, `.cursor/hooks.json`
- `~/.codex/hooks.json`, `.codex/hooks.json`, `~/.codex/config.toml`
- `~/.copilot/hooks/*`, `.github/hooks/*`
- `~/.agentwatch/*`
- `~/.claude/plugins/**/agentwatch*`, `~/.cursor/plugins/**/agentwatch*`
- the VS Code extension directory

This is a tripwire for actions that go through hooks, not a sandbox. A
command that builds the path at runtime (`cd ~ && cd .claude …`, base64,
an interpreter one-liner) can get past it. LIMITS.md says so.

- **Approval-bypass launch.** Claude Code `permission_mode:
  "bypassPermissions"` in any payload, and on macOS/Linux the parent
  process's arguments at session start (`--dangerously-skip-permissions`,
  `--yolo`, `--dangerously-bypass-approvals-and-sandbox`), raise
  `approval_bypass_launch` (AW-D05).
- **Heartbeat.** The flusher sends `POST /devices/heartbeat` at most once a
  minute with each client's installed/missing hooks and the sensor mode. The
  VS Code extension also sends one every 5 minutes while it is open. The
  dashboard shows a device as *silent* when no heartbeat arrived in 24 h, and
  as *hooks missing* when the last heartbeat says so. A closed editor is
  silent too; the dashboard says that, rather than calling it tampering.

## Install

- **User scope by default.**
  - Claude Code: `~/.claude/settings.json`.
  - Cursor: `~/.cursor/hooks.json`.
  - Codex: `~/.codex/hooks.json`.
  - Copilot: `~/.copilot/hooks/agentwatch.json`.
- **`--project`** writes the project files instead, with a warning that these
  files are usually committed.
- **Merge, never replace.** Our entries carry `"agentwatch": true` in Cursor
  and Copilot, or a command containing `agentwatch hook` in Claude Code and
  Codex. Re-install replaces only those entries, so it is idempotent.
- **Backup.** The first install writes `<file>.agentwatch-backup` once.
  Uninstall restores it byte-for-byte when the file hasn't changed since the
  install. Otherwise it removes only our entries and keeps the rest.
- **Paths with spaces.** Exec-form commands with the absolute binary path
  (Claude Code `args`) avoid shell quoting. Shell-string clients (Cursor,
  Codex, Copilot) get a quoted path. On Windows they get
  `powershell -NoProfile -ExecutionPolicy Bypass -File "<…>\aw.ps1"`.
- **Double firing.** Cursor runs `~/.claude/settings.json` hooks by default,
  and Copilot runs a repo's `.claude/settings.json`. The binary detects the
  real client from the payload (`cursor_version`, Copilot camelCase fields)
  and env. If an invocation for one client is really running inside another
  client whose own hooks are installed, it prints nothing and exits 0.

## API contract (added in the private repo)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/devices/code` | public (rate-limited) | start device-code flow |
| POST | `/devices/token` | public (device_code) | poll; `authorization_pending` / `slow_down` / `expired_token` / token |
| POST | `/devices/approve` | tenant | dashboard approves a user code |
| GET | `/devices` | tenant | devices with status active / silent / hooks_missing / revoked |
| DELETE | `/devices/{id}` | tenant | revoke |
| POST | `/devices/heartbeat` | device or tenant | clients, versions, hooks installed, mode, spool drops |
| POST | `/devices/events` | device or tenant | sensor security events (allow-listed types) |
| GET | `/devices/policy?agent_name=` | device or tenant | compact passport: mode, denied/allowed/approval tools, isolation, content capture; auto-provisions a dry_run passport (identity `human`) |
| POST | `/steps/preflight` | + fields | `client`, `command`, `path`, `mcp_server`, `mcp_tool`, `hold_id`, `sensor_mode`; response adds `hold_id`, `display_reason`, `scrubbed_input` |
| POST | `/runs` | + field | `tags` |
| POST | `/ingest` | + fields | `event_id` (dedupe), `latency_ms`, `tags` |

## What research changed

1. **The server has no HOLD/SCRUB on `/steps/preflight`.** Those are Gate
   verdicts. The sensor adds the fields above, and the server creates an
   approval request for a sensor `require_approval`.
2. **The Claude Code plugin cannot switch `onFailure` with the mode.**
   `hooks.json` is static while the mode is a user option. The plugin
   therefore uses `onFailure: continue` with a 5 s timeout, and the binary
   applies fail_closed itself before that timeout. The hook-level block is
   available only through `agentwatch install claude-code` (settings file),
   which needs Claude Code 2.1.295+ for `onFailure`.
3. **Claude Code floor 2.1.197.** 0.1.1 used `userConfig.options`
   (dry_run/enforce), which made older versions refuse the plugin. 0.1.2
   uses a free-text `mode` that the sensor reads case-insensitively.
4. **Plugin hooks use shell form, not exec form.** An exec-form `.sh` cannot
   start on Windows, while the shell form runs under Git Bash there. Windows
   without Git Bash uses `agentwatch install` or the VS Code extension, which
   write PowerShell commands. Exec form (pointing at the binary itself) is
   used for settings-file installs.
5. **Cursor and Copilot run Claude Code hook files.** This is what led to
   client detection and dedupe.
6. **Copilot denies on a non-zero `preToolUse` exit.** Exit status is always 0.
7. **Codex has no `ask` and re-trusts changed hooks by hash.** HOLD is deny
   with a message, and `install codex` tells the user to trust the hook.
8. **The directory reviewer will hold the Claude Code plugin.** The shim runs
   a downloaded binary from `${CLAUDE_PLUGIN_DATA}`. That is a "held for a
   reviewer" finding, not a block. The README discloses the download, the
   pinned checksum and the API host.
9. **The dashboard's Socket.IO channel has no authentication on `subscribe`.**
   Anyone who knows a run id can join its room. The extension polls the REST
   API instead, and this was reported to the owner as a separate finding.
10. **"Sensor silent" is a device status and a security event, not a 13th
    posture check.** The public site reads the posture-check count live, and
    a new check would change published numbers.
11. **ALLOW never means "skip the user's prompt".** See the verdict table.
