# Changelog

## 0.2.0: first full release

The first release that is not marked as a pre-release. The sensor's behaviour is the same as 0.1.2; this release is about the listing, the docs and the release itself.

- **Full GitHub release.** The release workflow no longer marks releases as pre-releases or titles them "preview", and the release notes are this version's section of the changelog rather than the whole file.
- **Privacy page corrected.** PRIVACY.md now lists the device name (your hostname by default) sent at sign-in and with sensor security events. It also says the pre-action check sends the redacted command truncated to 4,000 characters (the recorded step keeps 2,000), and what the VS Code extension sends itself: the sign-in fields, `decided_by` on approvals, and the isolation request.
- **Security contact:** security@agentwatch.in, as well as GitHub private advisories.
- **README:** install steps for Claude Code, VS Code and Cursor, each on its own, plus screenshots.
- **Extension icon** is 128×128.
- **Versions:** 0.2.0 in both plugin manifests, both marketplace manifests and the extension.
- Cursor, Codex and GitHub Copilot are still **untested** against the real clients ([LIMITS.md](docs/LIMITS.md#tested-so-far)).

## 0.1.2: fixes from the first real install

- **Self-protection blocked read-only commands.** `find ~/.claude/plugins/data -name 'agentwatch*' 2>/dev/null` was refused as "would modify ~/.claude/plugins/" because any `>` (here `2>/dev/null`) plus any protected path anywhere counted as a write. The check now parses the command and blocks only commands that write to protected paths. Reads (`find`, `ls`, `stat`, `cat`, `head`, `grep`) are allowed.
- **Agents can no longer read the sensor's credential.** `cat`/`grep`/`cp`/`tar` of `~/.agentwatch/credentials*`, the Read and Grep tools on it, and keychain dumps (`security find-generic-password … agentwatch`, `security dump-keychain`, `secret-tool lookup … agentwatch`) are blocked in every mode. 0.1.1 allowed them.
- **Off switches under another name.** `…/agentwatch-0.1.1-darwin-arm64 mode dry_run` and `sh aw.sh mode …` were not recognised as the sensor and ran. They are blocked now; `agentwatch mode` with no arguments (read-only) is allowed.
- **Sign-in from Claude Code: `/agentwatch:setup`.** It opens the approval page, shows the code, waits for the approval and stores the token in the OS keychain. Messages no longer tell you to run a bare `agentwatch` that is not on your PATH: they name `/agentwatch:setup` or the binary's full path. New `agentwatch link` puts it in `~/.local/bin` (`/agentwatch:setup` offers it and asks first).
- **First session works.** The MCP server and every hook now download the binary if it is missing (one download under a lock, SHA-256 checked, continuing in the background if a short hook times out). 0.1.1 downloaded only at session start, so the MCP server failed on the first session and Claude Code kept that failure for 15 minutes.
- **Not signed in is not an error.** Hooks and the MCP server run without a config file or a sign-in, queue events locally, and say so once at session start. Signing in sends the queue at once.
- **Status:** shows where the mode came from (`dry_run (default)` instead of `dry_run ()`), "sign in to load your passport" instead of nothing, and whether queued events wait for sign-in.
- **Older Claude Code:** the plugin installs on 2.1.197 again (`userConfig.mode: Unrecognized key: options`). The `mode` option is free text now and is read case-insensitively. Tested on 2.1.197 and 2.1.329.
- **Skill descriptions** are quoted: `/agentwatch:status`'s description contained `: ` and older Claude Code dropped its frontmatter.
- A rejected credential (401/403) now says how to sign in again, and the device may have been revoked.
- Tests: a shell-command self-protection table (reads, writes, credential reads; pipes, subshells, chains, quoting) with a fuzz target; shim tests for the first-session race, checksum refusal, stale lock and slow downloads; MCP sign-in tests; a fresh-install E2E (`tests/e2e/fresh_install_e2e.py`, results in `docs/e2e-fresh-install-results.json`).

## 0.1.1: preview, Claude Code + VS Code

First published release. `v0.1.0` was tagged but never released: its release workflow uploaded the VSIX files twice and failed. 0.1.1 fixes that workflow; the code is otherwise the same.

## 0.1.0 (tagged, not released)

First release, published as a **preview**:
- Claude Code and the VS Code extension are tested end to end.
- **Cursor, Codex and GitHub Copilot are untested** against the real clients ([LIMITS.md](docs/LIMITS.md#tested-so-far)).
- Fail-closed `enforce` needs a settings-file install on Claude Code 2.1.295+. The plugin path is fail-open.

- `agentwatch` sensor:
  - Hooks for Claude Code, Cursor, Codex and GitHub Copilot, with local decisions for reads and a server check (`/steps/preflight`) for shell, write, network and MCP calls.
  - `dry_run` and `enforce`, `fail_open` and `fail_closed`.
  - Self-protection, client-side redaction, a bounded spool, device-code sign-in, and an OS keychain store for the token.
- Claude Code plugin: hooks, a read-only MCP server and six skills.
- Cursor plugin.
- VS Code extension: Connect panel, live session, runs, findings, approvals, Gate, audit, notifications and walkthrough.
