import * as vscode from "vscode";
import * as bin from "./binary";
import { CLIENTS, parseInbound } from "./messages";
import { shell, webviewOptions } from "./webview";

interface DoctorReport {
  ok: boolean;
  sensor: { name: string; ok: boolean; warn?: boolean; detail: string }[];
  clients: { client: string; detected: boolean; status: string; checks: { ok: boolean; warn?: boolean; detail: string }[] }[];
}

interface Plan { client: string; path: string; exists: boolean; before: string; after: string; changed: boolean; warning?: string; note?: string }

let panel: vscode.WebviewPanel | undefined;

/** The Connect panel: detect clients, preview exact file changes, install
 *  with backup, uninstall, and show doctor's green/red per client. */
export function openConnect(ctx: vscode.ExtensionContext): void {
  if (panel) {
    panel.reveal();
    return;
  }
  panel = vscode.window.createWebviewPanel("agentwatch.connect", "AgentWatch: Connect", vscode.ViewColumn.Active,
    { ...webviewOptions(ctx.extensionUri), retainContextWhenHidden: false });
  panel.iconPath = vscode.Uri.joinPath(ctx.extensionUri, "media", "icon.png");
  panel.webview.html = shell(panel.webview, ctx.extensionUri, "connect", "AgentWatch: Connect");
  panel.onDidDispose(() => (panel = undefined));
  const post = (payload: unknown) => void panel?.webview.postMessage({ type: "data", view: "connect", payload });

  const load = async () => {
    if (!bin.hasBinary(ctx)) {
      post({ error: "This build of the extension has no agentwatch binary for " + process.platform + "-" + process.arch + "." });
      return;
    }
    const doctor = await bin.runJSON<DoctorReport>(ctx, ["doctor", "--json"]);
    const plans: Plan[] = [];
    for (const c of CLIENTS) {
      const r = await bin.runJSON<{ plans: Plan[] }>(ctx, ["install", c, "--dry-run", "--json"]);
      if (r?.plans?.[0]) plans.push(r.plans[0]);
    }
    post({ doctor, plans });
  };

  panel.webview.onDidReceiveMessage(async (raw) => {
    const msg = parseInbound(raw);
    if (!msg) return;
    switch (msg.type) {
      case "ready":
      case "refresh":
      case "doctor":
        return load();
      case "install": {
        const ok = await vscode.window.showWarningMessage(
          `Install AgentWatch hooks for ${msg.client}? The file shown in the preview is changed; a backup of the original is kept and "Uninstall" restores it.`,
          { modal: true }, "Install");
        if (ok !== "Install") return;
        const r = await bin.run(ctx, ["install", msg.client, "--yes"]);
        if (r.code !== 0) vscode.window.showErrorMessage(`AgentWatch install failed: ${r.stderr.trim()}`);
        else vscode.window.showInformationMessage(r.stdout.split("\n")[0]);
        return load();
      }
      case "uninstall": {
        const ok = await vscode.window.showWarningMessage(`Remove AgentWatch hooks for ${msg.client}?`, { modal: true }, "Uninstall");
        if (ok !== "Uninstall") return;
        const r = await bin.run(ctx, ["uninstall", msg.client]);
        if (r.code !== 0) vscode.window.showErrorMessage(`AgentWatch uninstall failed: ${r.stderr.trim()}`);
        else vscode.window.showInformationMessage(r.stdout.trim());
        return load();
      }
      case "signIn":
        await vscode.commands.executeCommand("agentwatch.signIn");
        return load();
      default:
        return;
    }
  });
}
