# Changelog

## 0.1.0 (unreleased): preview, Claude Code + VS Code

First release, published as a **preview**:
- Claude Code and the VS Code extension are tested end to end.
- **Cursor, Codex and GitHub Copilot are untested** against the real clients ([LIMITS.md](docs/LIMITS.md#tested-so-far-v010-preview)).
- Fail-closed `enforce` needs a settings-file install on Claude Code 2.1.295+. The plugin path is fail-open.

- `agentwatch` sensor:
  - Hooks for Claude Code, Cursor, Codex and GitHub Copilot, with local decisions for reads and a server check (`/steps/preflight`) for shell, write, network and MCP calls.
  - `dry_run` and `enforce`, `fail_open` and `fail_closed`.
  - Self-protection, client-side redaction, a bounded spool, device-code sign-in, and an OS keychain store for the token.
- Claude Code plugin: hooks, a read-only MCP server and six skills.
- Cursor plugin.
- VS Code extension: Connect panel, live session, runs, findings, approvals, Gate, audit, notifications and walkthrough.
