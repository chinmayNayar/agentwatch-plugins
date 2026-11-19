# Security policy

## Reporting a vulnerability

Please report security issues privately through GitHub: **Security → Report a vulnerability** on this repository (private advisory). Do not open a public issue for a vulnerability.

Include what you found, how to reproduce it, and what an attacker gains. We aim to acknowledge within 3 working days.

## Scope

In scope:
- the `agentwatch` binary
- the Claude Code and Cursor plugins (including the download-and-verify shims)
- the VS Code extension
- the release workflow

**Bypasses of the sensor's own guarantees are in scope.** For example:
- a secret that survives redaction
- a crash that turns a fail-closed deny into an allow
- an agent that can change the sensor's mode
- a way to make a client run an unverified binary

Known, documented limits are not vulnerabilities on their own. For example, self-protection can be evaded by building a path at runtime. See [docs/LIMITS.md](docs/LIMITS.md).

The AgentWatch service itself (`*.agentwatch.in`) has its own disclosure page: https://agentwatch.in/trust.

## How releases are protected

- Each binary is built by GitHub Actions from a tag, and listed in `SHA256SUMS` on the release.
- The plugins pin those checksums in `scripts/checksums.txt` and refuse a binary that does not match.
- Releases are not signed with a code-signing certificate yet: macOS and Windows may warn on first run of a downloaded binary.
- The repository is scanned with gitleaks on every push.
