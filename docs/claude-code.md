# Claude Code

Needs Claude Code **2.1.197 or later** (tested on 2.1.197 and 2.1.329). Plugin 0.1.1 needed 2.1.271+ because its `mode` option used `userConfig.options`, which older versions reject ("userConfig.mode: Unrecognized key: options"); 0.1.2 drops it.

## Install (plugin)

```bash
claude plugin marketplace add chinmayNayar/agentwatch-plugins
claude plugin install agentwatch@agentwatch
```

Then run **`/agentwatch:setup`** in Claude Code. It opens the approval page, shows the code, waits for your approval and stores the token in the OS keychain. Before you sign in the plugin still runs and queues events locally; session start says so once.

From a terminal, the plugin's binary is `~/.claude/plugins/data/agentwatch-agentwatch/bin/agentwatch` (a stable link that follows plugin updates). `~/.claude/plugins/data/agentwatch-agentwatch/bin/agentwatch link` puts `agentwatch` in `~/.local/bin`.

On first use the plugin downloads the `agentwatch` binary for your platform into `${CLAUDE_PLUGIN_DATA}/bin`. Every process that needs it (the MCP server, any hook) can start the download; a lock makes it one download, and each waits as long as it can (MCP server 90 s, session start 50 s, a pre-tool hook 3 s, after which that one call is not checked). It checks the file against the SHA-256 pinned in `scripts/checksums.txt` and refuses a mismatch.

Options, under `/config` → plugins → agentwatch:

| Option | Default | Notes |
|---|---|---|
| `api_url` | `https://api.agentwatch.in` | |
| `api_key` | (empty) | stored by Claude Code in your OS keychain |
| `mode` | `dry_run` | type `enforce` to block (free text: older Claude Code has no option lists) |

The plugin option overrides `~/.agentwatch/config.json`.

## Install (settings file, no plugin)

`agentwatch install claude-code` writes exec-form hooks into `~/.claude/settings.json`, merged with your own hooks and with a backup. In `enforce` + `fail_closed` it also sets `onFailure: "block"` (Claude Code 2.1.295+), so a sensor that cannot run blocks the call instead of skipping it.

## What you see

- Every session becomes a run named `claude-code@<you>`, tagged `client:claude-code`.
- Allowed calls show nothing extra in Claude Code.
- **Denied calls** fail with AgentWatch's reason.
- **Held calls** bring up Claude Code's own permission prompt, with the hold id. If you approve it there, AgentWatch records `hold_overridden_locally`.
- Skills: `/agentwatch:setup`, `:status`, `:runs`, `:findings`, `:posture`, `:mode`, `:doctor`. All but setup are read-only. Setup starts a device sign-in that a person must approve in the console; it refuses when the machine is already signed in. The agent cannot change its own mode.

## Windows

The plugin's hooks use a shell-form command, which Claude Code runs under Git Bash on Windows. Without Git Bash, use `agentwatch install claude-code` or the editor extension instead. Both write hooks that start the `.exe` directly, with no shell.
