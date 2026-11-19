import * as os from "os";
import * as vscode from "vscode";
import { Api, ApiError, DEVICE_TOKEN, OPERATOR_KEY } from "./api";
import * as bin from "./binary";
import { dashboardUrl } from "./format";

interface DeviceCode {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  interval: number;
  expires_in: number;
}

/**
 * Device-code sign-in. The token goes to SecretStorage and, through the
 * bundled binary (stdin, never argv), to the OS keychain entry the hooks read.
 */
export async function signIn(ctx: vscode.ExtensionContext, api: Api): Promise<boolean> {
  let dc: DeviceCode;
  try {
    dc = await api.request<DeviceCode>("POST", "/devices/code",
      { device_name: os.hostname(), user_label: os.userInfo().username, os: process.platform + "-" + process.arch }, "none");
  } catch (e) {
    vscode.window.showErrorMessage(`AgentWatch sign-in could not start: ${(e as Error).message}`);
    return false;
  }
  const open = "Open sign-in page";
  void vscode.window.showInformationMessage(`AgentWatch: enter code ${dc.user_code} in the browser to sign this machine in.`,
    open).then((c) => {
    if (c === open) void vscode.env.openExternal(vscode.Uri.parse(dc.verification_uri_complete));
  });
  void vscode.env.openExternal(vscode.Uri.parse(dc.verification_uri_complete));
  return vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, cancellable: true,
    title: `AgentWatch: waiting for code ${dc.user_code}` }, async (_p, cancel) => {
    let interval = Math.max(2, dc.interval || 5) * 1000;
    const deadline = Date.now() + dc.expires_in * 1000;
    while (Date.now() < deadline && !cancel.isCancellationRequested) {
      await new Promise((r) => setTimeout(r, interval));
      try {
        const tok = await api.request<{ access_token: string; device_id: string }>("POST", "/devices/token",
          { device_code: dc.device_code }, "none");
        await ctx.secrets.store(DEVICE_TOKEN, tok.access_token);
        if (bin.hasBinary(ctx)) {
          await bin.run(ctx, ["login", "--token-stdin"], { stdin: tok.access_token });
        }
        await vscode.commands.executeCommand("setContext", "agentwatch.signedIn", true);
        vscode.window.showInformationMessage("AgentWatch: this machine is signed in.");
        return true;
      } catch (e) {
        const code = e instanceof ApiError ? e.code : "";
        if (code === "authorization_pending") continue;
        if (code === "slow_down") { interval += 5000; continue; }
        if (code === "access_denied") { vscode.window.showWarningMessage("AgentWatch: sign-in was refused."); return false; }
        if (code === "expired_token") break;
        if (e instanceof ApiError && e.status === 0) continue; // network blip: keep polling
        vscode.window.showErrorMessage(`AgentWatch sign-in failed: ${(e as Error).message}`);
        return false;
      }
    }
    if (!cancel.isCancellationRequested) vscode.window.showWarningMessage("AgentWatch: the code expired. Try again.");
    return false;
  });
}

/**
 * Operator sign-in: an AgentWatch API key, kept in this extension's
 * SecretStorage only. A device token cannot approve holds or isolate agents
 * (otherwise the watched agent could approve its own calls), so those
 * actions need a person's key.
 */
export async function operatorSignIn(ctx: vscode.ExtensionContext, api: Api): Promise<boolean> {
  const key = await vscode.window.showInputBox({ password: true, ignoreFocusOut: true,
    title: "AgentWatch operator API key",
    prompt: "Used only from this editor for approvals, isolation and evidence export. Stored in the OS keychain.",
    validateInput: (v) => (v.trim().length < 10 ? "That does not look like an API key" : undefined) });
  if (!key) return false;
  await ctx.secrets.store(OPERATOR_KEY, key.trim());
  try {
    await api.request("GET", "/approvals/metrics?days=1", undefined, "operator");
  } catch (e) {
    await ctx.secrets.delete(OPERATOR_KEY);
    vscode.window.showErrorMessage(`AgentWatch: that key was not accepted (${(e as Error).message}).`);
    return false;
  }
  vscode.window.showInformationMessage("AgentWatch: operator actions enabled in this editor.");
  return true;
}

export async function signOut(ctx: vscode.ExtensionContext): Promise<void> {
  await ctx.secrets.delete(DEVICE_TOKEN);
  await ctx.secrets.delete(OPERATOR_KEY);
  if (bin.hasBinary(ctx)) await bin.run(ctx, ["logout"]);
  await vscode.commands.executeCommand("setContext", "agentwatch.signedIn", false);
  const dash = dashboardUrl(vscode.workspace.getConfiguration("agentwatch").get<string>("apiUrl") || "");
  vscode.window.showInformationMessage(`Signed out. To revoke the device token itself, open ${dash}/devices.`);
}
