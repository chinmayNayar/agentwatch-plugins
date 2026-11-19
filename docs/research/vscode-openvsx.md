# VS Code extension publishing, Open VSX, webview security

Read 2026-11-19.

Sources:
- https://code.visualstudio.com/api/working-with-extensions/publishing-extension
- https://code.visualstudio.com/api/extension-guides/webview
- https://code.visualstudio.com/api/references/vscode-api#SecretStorage
- https://github.com/eclipse/openvsx/wiki/Publishing-Extensions

## Marketplace (vsce)

- Platform-specific VSIX: `vsce package --target <t>` for `win32-x64`,
  `win32-arm64`, `linux-x64`, `linux-arm64`, `linux-armhf`, `alpine-x64`,
  `alpine-arm64`, `darwin-x64`, `darwin-arm64`, `web`. A package without
  `--target` is the fallback for every other platform. Matching needs VS Code
  1.61+. Publish one file: `vsce publish --packagePath <vsix>`.
- We ship `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`,
  `win32-x64`, each with its own `agentwatch` binary in `bin/`.
- Publisher: https://marketplace.visualstudio.com/manage (ID permanent).
- **PATs are "Not Recommended"; Azure DevOps global PATs retire on
  2026-12-01.** `VSCE_PAT` works today; after that date publishing needs the
  Entra ID path. RELEASE.md says so.
- Icon: PNG ≥ 128×128; SVG icons rejected. README/CHANGELOG images must be
  `https` and not SVG (badges excepted). `LICENSE` at the extension root.
  ≤ 30 keywords. Package from macOS/Linux (exec bit on the bundled binary).

## Open VSX

Eclipse account (with GitHub username) → sign the Publisher Agreement on
open-vsx.org → token at https://open-vsx.org/user-settings/tokens →
`npx ovsx create-namespace agentwatch -p $OVSX_PAT` (once) →
`npx ovsx publish <file.vsix> -p $OVSX_PAT`. Creating a namespace does
**not** make you its verified owner; that is a separate claim
(Namespace Access page). The wiki does not document `--target`; ovsx reads
the target from the VSIX manifest. Cursor, Windsurf and VSCodium install
from Open VSX, so this listing is what those editors see.

## Webview security (what the extension does)

- CSP meta at the top of `<head>`: `default-src 'none'; img-src
  ${webview.cspSource} data:; style-src ${webview.cspSource}; font-src
  ${webview.cspSource}; script-src 'nonce-<random>'`. No inline styles, no
  remote origins, no `unsafe-eval`.
- `enableScripts: true` only on webviews that need it; `localResourceRoots`
  = the extension's `media/` folder only; every local URI through
  `asWebviewUri`.
- Keep the `acquireVsCodeApi()` object private (closure, never `window.*`).
- Messages are validated on both sides against a fixed schema (type +
  fields) before use. Unknown messages are dropped.
- Theme: body classes `vscode-light | vscode-dark | vscode-high-contrast`;
  the brand tokens switch to the dark variant on `vscode-dark` and
  `vscode-high-contrast`.

## SecretStorage

`context.secrets.store/get/delete`: OS keychain-backed, per extension.
The device token lives there; it is passed to the bundled binary through
an environment variable for the one child process, never written to disk
by the extension.
