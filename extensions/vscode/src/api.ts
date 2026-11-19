import * as vscode from "vscode";
import { validApiUrl } from "./format";

/** Secrets live in VS Code SecretStorage (OS keychain-backed). */
export const DEVICE_TOKEN = "agentwatch.deviceToken";
export const OPERATOR_KEY = "agentwatch.operatorKey";

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
  }
}

export function apiUrl(): string {
  const raw = vscode.workspace.getConfiguration("agentwatch").get<string>("apiUrl") || "https://api.agentwatch.in";
  return raw.replace(/\/+$/, "");
}

/**
 * A small client. `as` picks the credential: the device token (reads,
 * reporting) or the operator key (approve, isolate, export), which a device
 * token is not allowed to do.
 */
export class Api {
  constructor(private secrets: vscode.SecretStorage) {}

  async token(as: "device" | "operator"): Promise<string | undefined> {
    return this.secrets.get(as === "operator" ? OPERATOR_KEY : DEVICE_TOKEN);
  }

  async request<T>(method: string, path: string, body?: unknown, as: "device" | "operator" | "none" = "device",
    timeoutMs = 10000): Promise<T> {
    const base = apiUrl();
    if (!validApiUrl(base)) throw new ApiError(0, "bad_api_url", "agentwatch.apiUrl must be https (http only for localhost)");
    const headers: Record<string, string> = { "content-type": "application/json", accept: "application/json",
      "user-agent": "agentwatch-vscode/" + (vscode.extensions.getExtension("agentwatch.agentwatch")?.packageJSON.version ?? "dev") };
    if (as !== "none") {
      const tok = (await this.token(as)) ?? (as === "operator" ? undefined : await this.token("operator"));
      if (!tok) throw new ApiError(401, as === "operator" ? "operator_required" : "not_signed_in",
        as === "operator" ? "This needs an operator sign-in (AgentWatch: Sign in as operator)." : "Not signed in.");
      headers["x-api-key"] = tok;
    }
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), timeoutMs);
    let res: Response;
    try {
      res = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body),
        signal: ctrl.signal, redirect: "error" });
    } catch (e) {
      throw new ApiError(0, "unreachable", `AgentWatch API unreachable (${(e as Error).name})`);
    } finally {
      clearTimeout(t);
    }
    const text = await res.text();
    let data: unknown = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = null; }
    if (!res.ok) {
      const d = (data as { detail?: unknown } | null)?.detail;
      const code = typeof d === "object" && d ? String((d as Record<string, unknown>).code ?? (d as Record<string, unknown>).error ?? res.status)
        : String(d ?? res.status);
      throw new ApiError(res.status, code, `HTTP ${res.status} (${code})`);
    }
    return data as T;
  }
}
