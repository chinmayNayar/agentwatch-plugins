# Privacy: exactly what the AgentWatch sensor sends

This page covers the `agentwatch` binary, the Claude Code and Cursor plugins, and the VS Code extension in this repository.

**Where data goes:**
- The sensor talks to **one** host: the AgentWatch API you configure (`api_url`, default `https://api.agentwatch.in`, or your self-hosted URL).
- The plugins also make one HTTPS download from `github.com`, which redirects to `objects.githubusercontent.com` or `release-assets.githubusercontent.com`. This fetches the binary on first use; nothing is uploaded there.
- Nothing else is contacted, including Anthropic, Cursor, OpenAI or GitHub (apart from that download).

## Sent by default

| Field | Example | Notes |
|---|---|---|
| Agent name | `claude-code@dev` | `<client>@<user label>`; the label defaults to your OS user name and can be changed (`user_label` in `~/.agentwatch/config.json`) |
| Session id | the client's own session / conversation id | one session = one run |
| Tool name and class | `Bash`, `shell` | |
| Command | `go test ./...` | **after client-side redaction** (below), truncated to 2,000 characters |
| File path | `/Users/dev/project/main.go` | the path, never the file |
| MCP server and tool name | `github`, `create_issue` | |
| Argument names | `["command", "description"]` | names only, not values |
| Result metadata | exit code, output size in bytes, duration | |
| The sensor's decision | `allow`, `deny`, `hold`, and where it came from | |
| Prompt / reply | **length and the first 12 hex characters of a SHA-256**, not the text | |
| Model name | `claude-sonnet-…` | when the client provides it |
| Tags | `client:claude-code`, `os:darwin-arm64`, `sensor:0.1.0`, `repo:<hash>`, `device:<id>` | `repo` is a hash of the git remote unless you set `repo_names: plain` |
| Heartbeat | sensor version, mode, which clients have hooks installed, spool counts | at most once a minute |
| Sensor security events | e.g. `self_protection_block`, `approval_bypass_launch` | with a redacted summary |

## Never sent by default

- File contents (what `Read` returns, what `Write`/`Edit` writes)
- Tool output (stdout, stderr, MCP results)
- Prompt text and the agent's replies
- Your Cursor login email, which Cursor includes in every hook payload. The sensor drops it.
- Environment variables and the contents of `.env` files

## Content capture (opt-in)

With `content_capture: true` **and** `approver: "<name>"` in `~/.agentwatch/config.json`, the sensor also sends prompt text, replies, tool arguments and tool output (each redacted, then truncated to 4,000 characters). Without an approver name, content capture stays off. In the console the agent's passport shows this as posture check AW-P11.

## Client-side redaction

Before anything is written to the local spool or sent, these patterns are replaced with `[REDACTED:<kind>]`:

- **Known key formats:**
  - AgentWatch keys (`aw_`, `awgk_`, `awdv_`)
  - OpenAI and Anthropic keys
  - Stripe live/test keys
  - AWS access key ids
  - GitHub tokens
  - Slack tokens
  - Google API keys
  - npm tokens
  - JWTs
- **Private keys:** `-----BEGIN … PRIVATE KEY-----` blocks.
- **Credentials in URLs:** passwords in URLs (`postgres://user:secret@host`).
- **Headers:** `Authorization:` and `x-api-key:` values.
- **Secret-named assignments:** `KEY=value` where the key names a secret (`*_SECRET`, `*_TOKEN`, `*_PASSWORD`, `*_API_KEY`, `DATABASE_URL`, `REDIS_URL`, …).
- **Secret flags:** `--password`, `--token`, `--secret`, `--api-key` and similar.
- **`.env` values:** every `KEY=value` value in a command that touches a `.env` file.
- **Secret-named fields:** any JSON field whose name contains `secret`, `token`, `password`, `api_key`, `credential`, `authorization` or `cookie`.

Redaction is pattern-based. It catches these formats, not every possible secret: a password typed as a bare word, with nothing around it that marks it as a secret, is not recognised. Redaction is tested in `internal/redact/redact_test.go`.

## On your machine

| Path | What | Permissions |
|---|---|---|
| `~/.agentwatch/config.json` | settings | 0600 |
| OS keychain, item `agentwatch` | the device token | macOS Keychain / Windows Credential Manager / Secret Service |
| `~/.agentwatch/credentials` | the token, only when no keychain is available | 0600 |
| `~/.agentwatch/spool/` | redacted events waiting to be sent (max 20 MB) | 0600 files |
| `~/.agentwatch/state/` | run ids, cached passport, the last 256 KiB of deny/hold notices for the editor | 0600 files |

`agentwatch logout` deletes the token, and `agentwatch uninstall all` removes the hooks. Deleting `~/.agentwatch` removes everything else.

## What the server keeps

That is the AgentWatch service's policy, not this repository's. With content capture off, the API stores the redacted summary above plus a hash, not raw arguments or output. See [agentwatch.in/trust](https://agentwatch.in/trust).
