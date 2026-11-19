import * as os from "os";
import * as vscode from "vscode";
import { Api, ApiError, apiUrl } from "./api";
import * as bin from "./binary";
import { dashboardUrl, isolateSteps, WHY } from "./format";
import { Inbound, Outbound, parseInbound } from "./messages";
import { shell, webviewOptions } from "./webview";

export type ViewName = "live" | "runs" | "findings" | "approvals" | "gate" | "audit";

interface Status { signed_in: boolean; mode: string; fail_mode: string; device_id?: string;
  agents?: Record<string, { agent: string; policy_mode: string; isolated: boolean }> }

/** Shared state the views read. */
export class Context {
  status: Status | null = null;
  selectedRun: string | null = null;
  constructor(public ext: vscode.ExtensionContext, public api: Api) {}

  async loadStatus(): Promise<Status | null> {
    if (!bin.hasBinary(this.ext)) return null;
    this.status = await bin.runJSON<Status>(this.ext, ["status", "--json"]);
    return this.status;
  }

  /** This machine's agent names: <client>@<user>. */
  agents(): string[] {
    const user = (os.userInfo().username || "user").toLowerCase().replace(/[^a-z0-9._-]/g, "");
    const fromStatus = Object.values(this.status?.agents ?? {}).map((a) => a.agent);
    const all = new Set([...fromStatus, ...["claude-code", "cursor", "codex", "copilot"].map((c) => `${c}@${user}`)]);
    return [...all];
  }

  dashboard(path = ""): string {
    const override = vscode.workspace.getConfiguration("agentwatch").get<string>("dashboardUrl") || "";
    return dashboardUrl(apiUrl(), override) + path;
  }
}

type Run = { id: string; agent_name: string; status: string; total_steps: number; started_at: string; tags?: string[] };

export class AgentWatchView implements vscode.WebviewViewProvider {
  private view?: vscode.WebviewView;
  private timer?: NodeJS.Timeout;

  constructor(private ctx: Context, public readonly name: ViewName) {}

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    view.webview.options = webviewOptions(this.ctx.ext.extensionUri);
    view.webview.html = shell(view.webview, this.ctx.ext.extensionUri, this.name, "AgentWatch " + this.name);
    view.webview.onDidReceiveMessage((raw) => {
      const msg = parseInbound(raw);
      if (msg) void this.handle(msg);
    });
    view.onDidChangeVisibility(() => this.schedule());
    view.onDidDispose(() => this.timer && clearInterval(this.timer));
    this.schedule();
  }

  private schedule(): void {
    if (this.timer) clearInterval(this.timer);
    if (!this.view?.visible) return;
    const secs = vscode.workspace.getConfiguration("agentwatch").get<number>("pollSeconds") ?? 5;
    const every = this.name === "live" ? Math.min(secs, 3) : Math.max(secs, 10);
    this.timer = setInterval(() => void this.refresh(), every * 1000);
  }

  post(m: Outbound): void {
    void this.view?.webview.postMessage(m);
  }

  async refresh(): Promise<void> {
    if (!this.view) return;
    try {
      this.post({ type: "data", view: this.name, payload: await this.load() });
    } catch (e) {
      const err = e as ApiError;
      this.post({ type: "data", view: this.name, payload: { error: err.message, code: err.code ?? "",
        dashboard: this.ctx.dashboard() } });
    }
  }

  // A device token reads only its own runs (GET /devices/runs). Without one,
  // the operator key lists the tenant's runs, narrowed to this machine's agents.
  private async runs(limit: number): Promise<Run[]> {
    const api = this.ctx.api;
    if (await api.token("device")) {
      const d = await api.request<{ items?: Run[] }>("GET", `/devices/runs?limit=${Math.min(limit, 50)}`);
      return d?.items ?? [];
    }
    const d = await api.request<{ items?: Run[] } | Run[]>("GET", `/runs?limit=${limit}`, undefined, "operator");
    const items = Array.isArray(d) ? d : d.items ?? [];
    const mine = new Set(this.ctx.agents());
    return items.filter((r) => mine.has(r.agent_name));
  }

  private async load(): Promise<unknown> {
    const api = this.ctx.api;
    const dash = this.ctx.dashboard();
    switch (this.name) {
      case "live": {
        const rs = await this.runs(5);
        const run = rs.find((r) => r.id === this.ctx.selectedRun) ?? rs[0];
        if (!run) return { run: null, steps: [], dashboard: dash };
        const steps = await api.request<unknown[]>("GET", `/runs/${run.id}/steps`);
        return { run, steps: (steps ?? []).slice(-60), dashboard: dash };
      }
      case "runs":
        return { runs: await this.runs(25), dashboard: dash };
      case "findings": {
        const agents = this.ctx.agents();
        const posture: unknown[] = [];
        for (const a of agents) {
          try {
            const p = await api.request<{ items?: unknown[] }>("GET", `/posture/agents/${encodeURIComponent(a)}`, undefined, "operator");
            if (p?.items?.length) posture.push({ agent: a, items: p.items });
          } catch (e) {
            if ((e as ApiError).code === "operator_required") throw e;
            /* agent never seen */
          }
        }
        const evs = await api.request<Record<string, unknown>[]>("GET", "/security/events?limit=200", undefined, "operator");
        const mine = new Set(agents);
        return { posture, events: (evs ?? []).filter((e) => mine.has(String(e.agent_name))).slice(0, 40), why: WHY,
          dashboard: dash };
      }
      case "approvals": {
        const inbox = await api.request<{ items: unknown[] }>("GET", "/approvals/inbox?status=pending", undefined, "operator");
        return { items: inbox?.items ?? [], operator: Boolean(await api.token("operator")), dashboard: dash };
      }
      case "gate": {
        const ps = await api.request<Record<string, unknown>[] | { items: Record<string, unknown>[] }>("GET", "/gw/admin/passports", undefined, "operator");
        const list = Array.isArray(ps) ? ps : ps.items ?? [];
        // Never ship keys or secrets to the webview: only the fields shown.
        const shown = list.map((p) => ({ id: p.id, agent_name: p.agent_name, status: p.status, policy_mode: p.policy_mode,
          enforce_mode: p.enforce_mode, isolated: Boolean(p.isolation), denied_tools: p.denied_tools,
          approval_required_tools: p.approval_required_tools }));
        return { passports: shown, mine: this.ctx.agents(), operator: Boolean(await api.token("operator")), dashboard: dash };
      }
      case "audit": {
        const v = await api.request<Record<string, unknown>>("GET", "/audit/verify", undefined, "operator");
        return { verify: v, operator: Boolean(await api.token("operator")), dashboard: dash };
      }
    }
  }

  private async handle(msg: Inbound): Promise<void> {
    const api = this.ctx.api;
    switch (msg.type) {
      case "ready":
      case "refresh":
        await this.ctx.loadStatus();
        return this.refresh();
      case "open":
        await vscode.env.openExternal(vscode.Uri.parse(this.ctx.dashboard(msg.path)));
        return;
      case "signIn":
        await vscode.commands.executeCommand("agentwatch.signIn");
        return this.refresh();
      case "operatorSignIn":
        await vscode.commands.executeCommand("agentwatch.operatorSignIn");
        return this.refresh();
      case "selectRun":
        this.ctx.selectedRun = msg.runId;
        await vscode.commands.executeCommand("agentwatch.live.focus");
        return;
      case "decide": {
        const verb = msg.outcome === "DENY" ? "Deny" : "Approve once";
        const ok = await vscode.window.showWarningMessage(
          `${verb} this held call? ${msg.outcome === "DENY" ? "Denying kills the agent's session run." : "It will be allowed exactly once when the agent retries it."}`,
          { modal: true }, verb);
        if (ok !== verb) return;
        try {
          await api.request("POST", `/approvals/${msg.kind}/${msg.id}/decide`,
            { outcome: msg.outcome, decided_by: os.userInfo().username }, "operator");
          vscode.window.showInformationMessage(`AgentWatch: ${verb.toLowerCase()} recorded.`);
        } catch (e) {
          vscode.window.showErrorMessage(`AgentWatch: ${(e as Error).message}`);
        }
        return this.refresh();
      }
      case "isolate": {
        const typed = await vscode.window.showInputBox({ ignoreFocusOut: true, title: `Isolate ${msg.agent}`,
          prompt: "Isolation revokes its gateway keys, suspends its passport, denies every tool, kills its open runs and refuses new ones. Type the agent name to confirm.",
          validateInput: (v) => (v === msg.agent ? undefined : "Type the agent name exactly") });
        if (typed !== msg.agent) return;
        try {
          const res = await api.request<{ isolation: Record<string, unknown>; passport: Record<string, unknown> }>(
            "POST", `/gw/admin/passports/${msg.passportId}/isolate`, { reason: "isolated from the editor", mode: "enforce" }, "operator");
          // Step 5 is checked, not assumed: a new run for the agent must be refused.
          let refused: boolean | null = null;
          try {
            await api.request("POST", "/runs", { agent_name: msg.agent, session_id: "isolate-probe-" + Date.now() });
            refused = false;
          } catch (e) {
            refused = e instanceof ApiError && e.status === 403;
          }
          this.post({ type: "data", view: "isolate", payload: { agent: msg.agent, steps: isolateSteps({ ...res, newRunRefused: refused }) } });
        } catch (e) {
          vscode.window.showErrorMessage(`AgentWatch: ${(e as Error).message}`);
        }
        return;
      }
      case "restore": {
        const ok = await vscode.window.showWarningMessage(`Restore ${msg.agent}? Revoked keys stay revoked.`, { modal: true }, "Restore");
        if (ok !== "Restore") return;
        try {
          await api.request("POST", `/gw/admin/passports/${msg.passportId}/restore`, {}, "operator");
        } catch (e) {
          vscode.window.showErrorMessage(`AgentWatch: ${(e as Error).message}`);
        }
        return this.refresh();
      }
      case "verifyAudit":
        return this.refresh();
      case "exportEvidence": {
        const uri = await vscode.window.showSaveDialog({ title: "Save AgentWatch evidence bundle",
          defaultUri: vscode.Uri.file(os.homedir() + "/agentwatch-evidence.zip"), filters: { "Zip": ["zip"] } });
        if (!uri) return;
        try {
          const tok = await api.token("operator");
          if (!tok) throw new ApiError(401, "operator_required", "This needs an operator sign-in.");
          const res = await fetch(apiUrl() + "/evidence/export", { method: "POST", redirect: "error",
            headers: { "x-api-key": tok, "content-type": "application/json" }, body: "{}" });
          if (!res.ok) throw new ApiError(res.status, String(res.status), `HTTP ${res.status}`);
          await vscode.workspace.fs.writeFile(uri, new Uint8Array(await res.arrayBuffer()));
          vscode.window.showInformationMessage(`AgentWatch: signed evidence bundle saved to ${uri.fsPath}. Verify it offline with the bundle's public key.`);
        } catch (e) {
          vscode.window.showErrorMessage(`AgentWatch: ${(e as Error).message}`);
        }
        return;
      }
      default:
        return;
    }
  }
}
