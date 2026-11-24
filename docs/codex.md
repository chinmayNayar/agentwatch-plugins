# Codex

> **Untested against the real client (as of 0.2.0).** This client has not yet been run against AgentWatch end to end. The sensor's handling is tested with payloads built from Codex's documentation. Report problems at https://github.com/chinmayNayar/agentwatch-plugins/issues.

```bash
agentwatch login
agentwatch install codex      # writes ~/.codex/hooks.json (or $CODEX_HOME/hooks.json)
```

**Then open Codex and trust the new hooks.** Codex runs a new or changed hook only after you review it, so nothing is checked until then. `agentwatch doctor` cannot see Codex's trust state.

- **Covered:** shell (`Bash`, `exec_command`), `apply_patch` and MCP tools, through `PreToolUse`.
- **Not covered:** hosted tools such as web search.
- **Held calls are refused** with instructions: Codex has no `ask`.
