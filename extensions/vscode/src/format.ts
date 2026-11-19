// Pure presentation logic shared by the views (unit-tested, no vscode import).

export type Chip = "ALLOW" | "SCRUB" | "REROUTE" | "HOLD" | "DENY" | "DRY RUN";

/** The verdict chip for a step, from the sensor's summary in args_redacted. */
export function chipForStep(step: { args_redacted?: unknown; input_data?: unknown }): Chip | null {
  const a = (step.args_redacted ?? step.input_data) as Record<string, unknown> | null | undefined;
  const v = a && typeof a === "object" ? (a.agentwatch as Record<string, unknown> | undefined) : undefined;
  if (!v) return null;
  const d = String(v.decision ?? "");
  if (v.would_have && d === "allow") return "DRY RUN";
  switch (d) {
    case "allow": return "ALLOW";
    case "scrub": return "SCRUB";
    case "hold": return "HOLD";
    case "deny": return "DENY";
    case "reroute": return "REROUTE";
  }
  return null;
}

/** CSS class for a chip (colours live in app.css). */
export function chipClass(c: Chip): string {
  return "chip chip-" + c.toLowerCase().replace(/\s+/g, "-");
}

/** What a finding means and why it matters, for AW-P and AW-D ids. */
export const WHY: Record<string, { title: string; why: string }> = {
  "AW-P01": { title: "Lethal trifecta", why: "The agent can read private data, read untrusted input and send data out. One injected instruction can exfiltrate data." },
  "AW-P02": { title: "Unused privilege", why: "Permissions the agent never uses only widen what a hijacked agent can do." },
  "AW-P03": { title: "Borrowed identity", why: "Acting as a person or a shared account hides which agent did what and gives it that person's access." },
  "AW-P04": { title: "Long-lived keys", why: "Keys that never expire stay useful to whoever copies them." },
  "AW-P05": { title: "Approvals off", why: "Nothing stops a destructive or costly call before it happens." },
  "AW-P06": { title: "Unpinned supply chain", why: "A tool server or model that changes under you can change what the agent does." },
  "AW-P07": { title: "Unapproved component", why: "A tool or model nobody reviewed is part of the agent's attack surface." },
  "AW-P08": { title: "Unsafe model format", why: "Pickle-based model files can run code when loaded." },
  "AW-P09": { title: "No sandbox", why: "Code the agent runs has the same access as the agent's host." },
  "AW-P10": { title: "Unscreened model traffic", why: "Model calls that bypass the gateway are not checked before they run." },
  "AW-P11": { title: "Content logging on", why: "Logged prompts and outputs can hold personal data and secrets; someone must own that decision." },
  "AW-P12": { title: "No kill switch", why: "If the agent misbehaves, there may be no enforced way to stop it." },
  "AW-D01": { title: "Hijack after untrusted read", why: "An outbound or destructive action right after reading untrusted content is the shape of a prompt injection." },
  "AW-D02": { title: "Tool outside allowlist", why: "The agent tried something its passport does not allow." },
  "AW-D03": { title: "New destination", why: "Data going somewhere this agent has never sent data before." },
  "AW-D04": { title: "Tool description changed", why: "A tool server changed what a tool says it does: the classic MCP rug pull." },
  "AW-D05": { title: "Approval-skipping launch", why: "The agent was started with its approval prompts turned off." },
  "AW-D06": { title: "Secret hunting", why: "The agent is looking for credentials it was not asked to use." },
  "AW-D07": { title: "Key misuse", why: "A key is used from somewhere, or at a rate, it never was before." },
  "AW-D08": { title: "Cost or volume spike", why: "Runaway loops burn money and can hide other activity." },
  "AW-D09": { title: "Memory write from untrusted source", why: "Poisoned memory keeps influencing the agent after the attack." },
  "AW-D10": { title: "Rubber-stamp approvals", why: "Approvals given in seconds, or always, are not oversight." },
  "AW-D11": { title: "Ignored stop", why: "The agent kept acting after it was told to stop." },
  "AW-D12": { title: "Unknown package install", why: "A package name an agent made up can be registered by an attacker." },
  "AW-D13": { title: "Autonomy rising", why: "Someone removed approvals or raised the agent's autonomy." },
};

/** The five things isolate does (docs/KILL_SWITCH.md), with their status. */
export interface IsolateStep { label: string; done: boolean | null; detail?: string }

export function isolateSteps(res: {
  isolation?: Record<string, unknown> | null;
  passport?: Record<string, unknown> | null;
  newRunRefused?: boolean | null;
}): IsolateStep[] {
  const iso = res.isolation ?? {};
  const p = res.passport ?? {};
  // decision "isolate" = applied; "allow" + would_have "isolate" = dry_run (recorded only).
  const applied = iso.decision === "isolate";
  const signals = Array.isArray(iso.signals) ? (iso.signals as Record<string, unknown>[]) : [];
  const keys = signals.find((s) => s.action === "revoke_gateway_keys");
  const runs = signals.find((s) => s.action === "kill_running_runs");
  const nKeys = keys ? Number(keys.count ?? (Array.isArray(keys.key_ids) ? keys.key_ids.length : 0)) : 0;
  const nRuns = runs && Array.isArray(runs.run_ids) ? runs.run_ids.length : 0;
  return [
    { label: "Gateway keys revoked", done: applied, detail: `${nKeys} key(s)` },
    { label: "Passport suspended", done: applied && (p.status === "suspended" || p.status === "revoked") },
    { label: "Every tool denied", done: applied && Boolean(p.isolation) },
    { label: "Open runs killed", done: applied, detail: `${nRuns} run(s)` },
    { label: "New runs refused", done: res.newRunRefused ?? null },
  ];
}

/** Console URL from the API URL: api.<host> -> app.<host>, :8000 -> :3000. */
export function dashboardUrl(apiUrl: string, override = ""): string {
  if (override) return override.replace(/\/+$/, "");
  try {
    const u = new URL(apiUrl);
    if (u.hostname.startsWith("api.")) u.hostname = "app." + u.hostname.slice(4);
    else if (u.port === "8000") u.port = "3000";
    u.pathname = "";
    return u.toString().replace(/\/+$/, "");
  } catch {
    return apiUrl;
  }
}

/** API URLs must be https, except loopback http for local development. */
export function validApiUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    if (u.protocol === "https:") return true;
    return u.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(u.hostname);
  } catch {
    return false;
  }
}

/** Never show a whole key: keep a short prefix and the last 4. */
export function maskKey(k: string | null | undefined): string {
  if (!k) return "—";
  if (k.length <= 10) return "••••";
  return k.slice(0, 6) + "••••" + k.slice(-4);
}
