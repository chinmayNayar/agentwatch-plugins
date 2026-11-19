import * as crypto from "crypto";
import * as vscode from "vscode";

export function nonce(): string {
  return crypto.randomBytes(18).toString("base64").replace(/[^A-Za-z0-9]/g, "");
}

/**
 * The HTML shell for every AgentWatch webview. Strict CSP: no remote origin,
 * no inline script or style, no eval; scripts only with this load's nonce;
 * fonts, styles and images only from the extension's media folder.
 */
export function shell(webview: vscode.Webview, extUri: vscode.Uri, view: string, title: string): string {
  const n = nonce();
  const media = (f: string) => webview.asWebviewUri(vscode.Uri.joinPath(extUri, "media", f)).toString();
  const csp = [
    "default-src 'none'",
    `img-src ${webview.cspSource} data:`,
    `style-src ${webview.cspSource}`,
    `font-src ${webview.cspSource}`,
    `script-src 'nonce-${n}'`,
    "connect-src 'none'",
    "form-action 'none'",
    "base-uri 'none'",
    "frame-src 'none'",
  ].join("; ");
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${csp}">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="${media("app.css")}">
<title>${title}</title>
</head>
<body data-view="${view}">
<main id="root" aria-live="polite"></main>
<script nonce="${n}" src="${media("app.js")}"></script>
</body>
</html>`;
}

export function webviewOptions(extUri: vscode.Uri): vscode.WebviewOptions {
  return {
    enableScripts: true,
    enableCommandUris: false,
    localResourceRoots: [vscode.Uri.joinPath(extUri, "media")],
  };
}
