import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import * as vscode from "vscode";
import { Api } from "./api";
import { operatorSignIn, signIn, signOut } from "./auth";
import * as bin from "./binary";
import { openConnect } from "./connect";
import { AgentWatchView, Context, ViewName } from "./views";

const VIEWS: ViewName[] = ["live", "runs", "findings", "approvals", "gate", "audit"];

export function activate(ext: vscode.ExtensionContext): void {
  const api = new Api(ext.secrets);
  const ctx = new Context(ext, api);
  const views = VIEWS.map((v) => new AgentWatchView(ctx, v));
  for (const v of views) {
    ext.subscriptions.push(vscode.window.registerWebviewViewProvider(`agentwatch.${v.name}`, v,
      { webviewOptions: { retainContextWhenHidden: false } }));
  }
  const refreshAll = async () => {
    await ctx.loadStatus();
    await updateStatus();
    await Promise.all(views.map((v) => v.refresh()));
  };

  // Status bar: AgentWatch ● dry_run | enforce | offline | isolated
  const item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 50);
  item.command = "agentwatch.connect";
  ext.subscriptions.push(item);
  const updateStatus = async () => {
    const st = ctx.status;
    let label = "offline";
    let tip = "AgentWatch cannot reach its API or is not signed in.";
    let bg: vscode.ThemeColor | undefined;
    try {
      await api.request("GET", "/health", undefined, "none", 4000);
      if (st && !st.signed_in) {
        label = "signed out";
        tip = "Sign in: AgentWatch: Sign in (device code)";
      } else {
        const isolated = Object.values(st?.agents ?? {}).some((a) => a.isolated);
        label = isolated ? "isolated" : st?.mode ?? "dry_run";
        tip = isolated ? "An agent on this machine is isolated: its tool calls are refused." :
          label === "enforce" ? "Enforcing: denied, held and isolated calls are blocked." :
          "dry_run: AgentWatch records what it would block and blocks nothing.";
        if (isolated) bg = new vscode.ThemeColor("statusBarItem.errorBackground");
        else if (label === "enforce") bg = new vscode.ThemeColor("statusBarItem.warningBackground");
      }
    } catch { /* offline */ }
    item.text = `$(shield) AgentWatch ● ${label}`;
    item.tooltip = tip + "\nClick to connect coding agents.";
    item.backgroundColor = bg;
    item.show();
  };

  ext.subscriptions.push(
    vscode.commands.registerCommand("agentwatch.signIn", async () => { await signIn(ext, api); await refreshAll(); }),
    vscode.commands.registerCommand("agentwatch.operatorSignIn", async () => { await operatorSignIn(ext, api); await refreshAll(); }),
    vscode.commands.registerCommand("agentwatch.signOut", async () => { await signOut(ext); await refreshAll(); }),
    vscode.commands.registerCommand("agentwatch.connect", () => openConnect(ext)),
    vscode.commands.registerCommand("agentwatch.doctor", () => openConnect(ext)),
    vscode.commands.registerCommand("agentwatch.refresh", refreshAll),
    vscode.commands.registerCommand("agentwatch.openDashboard", () => vscode.env.openExternal(vscode.Uri.parse(ctx.dashboard("/dashboard")))),
  );

  // DENY / HOLD notifications from the local feed the hook writes.
  watchNotifications(ext, ctx);

  // Heartbeat: the binary's flusher sends it at most once a minute; nudge it
  // every 5 minutes while the editor is open (also detects removed hooks).
  const beat = setInterval(() => { if (bin.hasBinary(ext)) void bin.run(ext, ["flush"]); }, 5 * 60 * 1000);
  ext.subscriptions.push({ dispose: () => clearInterval(beat) });

  void (async () => {
    await ctx.loadStatus();
    await vscode.commands.executeCommand("setContext", "agentwatch.signedIn", Boolean(ctx.status?.signed_in));
    await updateStatus();
    if (bin.hasBinary(ext)) void bin.run(ext, ["flush"]);
    if (!ctx.status?.signed_in && !ext.globalState.get("agentwatch.walkthroughShown")) {
      await ext.globalState.update("agentwatch.walkthroughShown", true);
      void vscode.commands.executeCommand("workbench.action.openWalkthrough", "agentwatch.agentwatch#agentwatch.start", false);
    }
  })();
  const statusTimer = setInterval(() => void ctx.loadStatus().then(updateStatus), 30000);
  ext.subscriptions.push({ dispose: () => clearInterval(statusTimer) });
}

function watchNotifications(ext: vscode.ExtensionContext, ctx: Context): void {
  const home = process.env.AGENTWATCH_HOME || path.join(os.homedir(), ".agentwatch");
  const file = path.join(home, "state", "notify.jsonl");
  let offset = 0;
  try { offset = fs.statSync(file).size; } catch { offset = 0; }
  const read = () => {
    let size = 0;
    try { size = fs.statSync(file).size; } catch { return; }
    if (size < offset) offset = 0; // file was cut back
    if (size === offset) return;
    const fd = fs.openSync(file, "r");
    const buf = Buffer.alloc(Math.min(size - offset, 64 * 1024));
    fs.readSync(fd, buf, 0, buf.length, offset);
    fs.closeSync(fd);
    offset = size;
    for (const line of buf.toString("utf8").split("\n")) {
      if (!line.trim()) continue;
      let n: { decision?: string; tool?: string; agent?: string; reason?: string; hold_id?: string };
      try { n = JSON.parse(line); } catch { continue; }
      const what = n.decision === "hold" ? "is holding" : "blocked";
      const open = "Open in AgentWatch";
      void vscode.window.showWarningMessage(`AgentWatch ${what} ${n.tool ?? "a tool call"} for ${n.agent ?? "an agent"}: ${(n.reason ?? "").slice(0, 200)}`,
        open).then((c) => {
        if (c === open) void vscode.env.openExternal(vscode.Uri.parse(ctx.dashboard(n.decision === "hold" ? "/approvals" : "/dashboard")));
      });
    }
  };
  const w = vscode.workspace.createFileSystemWatcher(new vscode.RelativePattern(vscode.Uri.file(path.dirname(file)), "notify.jsonl"));
  w.onDidChange(read);
  w.onDidCreate(read);
  ext.subscriptions.push(w);
  const poll = setInterval(read, 3000); // watchers outside the workspace are not reliable on every platform
  ext.subscriptions.push({ dispose: () => clearInterval(poll) });
}

export function deactivate(): void {}
