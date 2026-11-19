# AgentWatch for VS Code, Cursor, Windsurf and VSCodium

AgentWatch watches your coding agents the way it watches production agents. It works with Claude Code, Cursor, Codex and GitHub Copilot.

- Every session becomes an AgentWatch **run**.
- Risky tool calls are checked **before they run**.
- An operator can **stop an agent** from the console or from this editor.

## What you get

- **Connect.** Finds the coding agents on this machine and shows the exact hook-file change before writing it. It installs with a backup, one click undoes it, and doctor shows green or red per agent.
- **Live session.** Tool calls as they happen, with verdict chips: ALLOW, SCRUB, HOLD, DENY, and DRY RUN for what dry-run would have blocked.
- **Runs and Findings.** Posture checks (AW-P01 to AW-P12) and runtime detections (AW-D01 to AW-D13), each in plain English with why it matters.
- **Approvals.** Approve or deny a held call. You confirm each decision.
- **Gate.** This account's agent passports, never their keys. You can isolate an agent after typing its name, and watch each of the five isolation steps complete.
- **Audit.** Verify the hash-chained audit log, and export the signed evidence bundle.
- **Notifications.** A notification when AgentWatch denies or holds a call, with *Open in AgentWatch*.

## Sign-in and credentials

**Sign in** uses a device code. The device token is stored in VS Code's SecretStorage and in your OS keychain, for the hooks. It can report this machine's sessions and read this machine's own runs and policy, and nothing else.

The **Findings, Approvals, Gate and Audit** views, and approving, isolating and exporting, need an **operator sign-in** with an AgentWatch API key, kept in SecretStorage. A device token cannot do these things, so an agent on this machine cannot approve its own held calls.

## Privacy

The bundled `agentwatch` binary talks only to the API URL you configure (`agentwatch.apiUrl`).

**It sends:**
- tool names
- redacted commands
- file paths
- sizes, exit codes and durations

**It never sends file contents, tool output or prompts** unless you turn content capture on. See [PRIVACY.md](https://github.com/chinmayNayar/agentwatch-plugins/blob/main/PRIVACY.md).

## Limits

Blocking covers actions that go through each agent's hooks. [LIMITS.md](https://github.com/chinmayNayar/agentwatch-plugins/blob/main/docs/LIMITS.md) lists, per agent, what can and cannot be blocked.

License: Apache-2.0. The fonts (Inter, JetBrains Mono) are under the SIL Open Font License; the licence texts are in `media/fonts/`.
