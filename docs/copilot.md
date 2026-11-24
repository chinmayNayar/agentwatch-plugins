# GitHub Copilot (VS Code agent, Copilot CLI)

> **Untested against the real client (as of 0.2.0).** This client has not yet been run against AgentWatch end to end. The sensor's handling is tested with payloads built from Copilot's documentation. Report problems at https://github.com/chinmayNayar/agentwatch-plugins/issues.

Copilot hooks are in **preview**.

```bash
agentwatch login
agentwatch install copilot    # writes ~/.copilot/hooks/agentwatch.json
```

The file registers the camelCase events (`preToolUse`, …) only. Registering both casings would make Copilot fire every hook twice.

- **Crashes vs timeouts.** Copilot denies a call when a hook exits non-zero, but allows it when a hook times out. The sensor always exits 0, and decides inside its own timeout (800 ms by default) before Copilot's.
- **Held calls are refused** with instructions.
- **Cloud sessions do not copy local hooks**, and your organisation can disable hooks.
