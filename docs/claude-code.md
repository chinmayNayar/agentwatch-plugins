# Claude Code

Needs Claude Code **2.1.271 or later**: the plugin's `mode` option uses `userConfig.options`.

## Install (plugin)

```bash
claude plugin marketplace add chinmayNayar/agentwatch-plugins
claude plugin install agentwatch@agentwatch
agentwatch login        # or set the plugin's api_key option in /config
```

On the first session start the plugin downloads the `agentwatch` binary for your platform into `${CLAUDE_PLUGIN_DATA}/bin`. It checks the file against the SHA-256 pinned in `scripts/checksums.txt` and refuses a mismatch.

Options, under `/config` → plugins → agentwatch:

| Option | Default | Notes |
|---|---|---|
| `api_url` | `https://api.agentwatch.in` | |
| `api_key` | (empty) | stored by Claude Code in your OS keychain |
| `mode` | `dry_run` | `enforce` blocks |

The plugin option overrides `~/.agentwatch/config.json`.

## Install (settings file, no plugin)

`agentwatch install claude-code` writes exec-form hooks into `~/.claude/settings.json`, merged with your own hooks and with a backup. In `enforce` + `fail_closed` it also sets `onFailure: "block"` (Claude Code 2.1.295+), so a sensor that cannot run blocks the call instead of skipping it.

## What you see

- Every session becomes a run named `claude-code@<you>`, tagged `client:claude-code`.
- Allowed calls show nothing extra in Claude Code.
- **Denied calls** fail with AgentWatch's reason.
- **Held calls** bring up Claude Code's own permission prompt, with the hold id. If you approve it there, AgentWatch records `hold_overridden_locally`.
- Skills: `/agentwatch:status`, `:runs`, `:findings`, `:posture`, `:mode`, `:doctor`. They are read-only; the agent cannot change its own mode.

## Windows

The plugin's hooks use a shell-form command, which Claude Code runs under Git Bash on Windows. Without Git Bash, use `agentwatch install claude-code` or the editor extension instead. Both write hooks that start the `.exe` directly, with no shell.
