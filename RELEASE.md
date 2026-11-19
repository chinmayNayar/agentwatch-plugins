# Releasing

## Every release

1. **Bump the version** in four places:
   - `plugins/*/.claude-plugin/plugin.json` and `plugins/cursor/.cursor-plugin/plugin.json`
   - `extensions/vscode/package.json`
   - `.claude-plugin/marketplace.json` if it names a version
   - `CHANGELOG.md`
2. **Build reproducibly and pin the checksums** the plugins trust:
   ```bash
   sh scripts/build-release.sh <version> dist
   sh scripts/pin-checksums.sh dist/SHA256SUMS <version>
   git commit -am "Pin v<version> checksums"
   ```
   The release workflow rebuilds with the same Go version (`GO_VERSION` in `.github/workflows/`). It refuses to publish if its binaries do not match the pinned checksums.
3. **Run the checks:**
   ```bash
   go test -race ./...
   python3 scripts/guard_sweep.py
   claude plugin validate --strict ./plugins/claude-code
   claude plugin validate --strict .
   ```
   Also run gitleaks over the tree and history.
4. **Tag and push:** `git tag -a v<version> -m "v<version>: <one-line scope>"`, then `git push origin v<version>`. For v0.1.0 the message is `v0.1.0: preview: Claude Code + VS Code`, and the release is marked as a pre-release. `release.yml` then does the following:
   - builds the five binaries
   - writes `SHA256SUMS`
   - packages the five platform VSIXs
   - creates the GitHub Release
   - publishes the VSIXs to the VS Code Marketplace (if the `VSCE_PAT` secret is set) and to Open VSX (if `OVSX_PAT` is set)
5. **Verify the Claude Code install from GitHub:**
   ```bash
   claude plugin marketplace add chinmayNayar/agentwatch-plugins
   claude plugin install agentwatch@agentwatch
   ```
   Start a session and run `/agentwatch:doctor`.
6. Run the manual Cursor checklist (`docs/checklists/cursor-manual.md`).

## Tokens

These come only from environment variables or Actions secrets, and are never printed:
- `VSCE_PAT`: Azure DevOps PAT with Marketplace → Manage. Microsoft retires global PATs on **2026-12-01**, after which `vsce` needs Entra ID sign-in instead.
- `OVSX_PAT`: open-vsx.org access token.

Store them for CI with `gh secret set VSCE_PAT < /dev/stdin` (or from the env var), so they never appear in shell history.

## Directory listings (submitted by a person)

### Anthropic plugin directory (Claude)

- **Individual account:** submit at https://platform.claude.com/plugins/submit.
- **Team or Enterprise organization:** the claude.ai directory portal (https://claude.ai/directory/manage) is for those plans only.

Use the values below in whichever form you get. If the form asks for something not listed here, answer it from [PRIVACY.md](PRIVACY.md) and [docs/LIMITS.md](docs/LIMITS.md).

| Field | Value |
|---|---|
| Repository | `chinmayNayar/agentwatch-plugins` |
| Plugin path | `plugins/claude-code` |
| Branch or tag | `main` |

**Data handling answers:**

| Question | Answer |
|---|---|
| Reads or stores personal data? | **Yes.** It sends tool names, redacted shell commands, file paths, MCP server/tool names, sizes, exit codes and durations to the AgentWatch API the user configures. File contents, tool output and prompts are not sent unless the user turns on content capture. |
| Sends data to services other than declared connectors? | **Yes.** It sends to the user's AgentWatch API (default `api.agentwatch.in`). It downloads its binary once from this repository's GitHub Releases, checked against a pinned SHA-256. |
| Retention | Governed by the user's AgentWatch account. The plugin keeps a local queue of at most 20 MB. |
| Intended for under-18s | No. |

**Expect a reviewer hold.** The hook script runs a binary it downloads into `${CLAUDE_PLUGIN_DATA}`, which the pre-submission checklist holds for review. The plugin README discloses the download, the checksum pinning and the API host.

### Cursor marketplace

Submit at https://cursor.com/marketplace/publish with the repository link `https://github.com/chinmayNayar/agentwatch-plugins`. The plugin is at `plugins/cursor`, listed in `.cursor-plugin/marketplace.json`.
