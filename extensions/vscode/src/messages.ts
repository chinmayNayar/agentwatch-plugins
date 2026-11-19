// Messages between webviews and the extension. Every inbound message is
// validated against this schema before use; anything else is dropped.

export const CLIENTS = ["claude-code", "cursor", "codex", "copilot"] as const;
export type Client = (typeof CLIENTS)[number];

export const OUTCOMES = ["APPROVE_ONCE", "DENY"] as const;
export type Outcome = (typeof OUTCOMES)[number];

export type Inbound =
  | { type: "ready" }
  | { type: "refresh" }
  | { type: "open"; path: string }
  | { type: "signIn" }
  | { type: "operatorSignIn" }
  | { type: "decide"; kind: "run" | "gate"; id: string; outcome: Outcome }
  | { type: "isolate"; passportId: string; agent: string }
  | { type: "restore"; passportId: string; agent: string }
  | { type: "preview"; client: Client }
  | { type: "install"; client: Client }
  | { type: "uninstall"; client: Client }
  | { type: "doctor" }
  | { type: "verifyAudit" }
  | { type: "exportEvidence" }
  | { type: "selectRun"; runId: string };

const ID = /^[A-Za-z0-9-]{1,64}$/;
const AGENT = /^[A-Za-z0-9._@:-]{1,200}$/;
// A dashboard path: relative, no scheme, no "//", no backslash, no "..".
const PATH = /^\/(?!\/)[A-Za-z0-9/_\-?=&.%:@]*$/;

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function str(v: unknown, re: RegExp): v is string {
  return typeof v === "string" && re.test(v);
}

/** Parse an inbound message or return null. Extra keys are ignored. */
export function parseInbound(raw: unknown): Inbound | null {
  if (!isObj(raw) || typeof raw.type !== "string") return null;
  switch (raw.type) {
    case "ready":
    case "refresh":
    case "signIn":
    case "operatorSignIn":
    case "doctor":
    case "verifyAudit":
    case "exportEvidence":
      return { type: raw.type };
    case "open":
      return str(raw.path, PATH) && !raw.path.includes("..") ? { type: "open", path: raw.path } : null;
    case "decide":
      if ((raw.kind === "run" || raw.kind === "gate") && str(raw.id, ID) &&
          typeof raw.outcome === "string" && (OUTCOMES as readonly string[]).includes(raw.outcome)) {
        return { type: "decide", kind: raw.kind, id: raw.id, outcome: raw.outcome as Outcome };
      }
      return null;
    case "isolate":
    case "restore":
      return str(raw.passportId, ID) && str(raw.agent, AGENT)
        ? { type: raw.type, passportId: raw.passportId, agent: raw.agent }
        : null;
    case "preview":
    case "install":
    case "uninstall":
      return typeof raw.client === "string" && (CLIENTS as readonly string[]).includes(raw.client)
        ? { type: raw.type, client: raw.client as Client }
        : null;
    case "selectRun":
      return str(raw.runId, ID) ? { type: "selectRun", runId: raw.runId } : null;
  }
  return null;
}

/** Outbound: data for one view, or an error line. */
export type Outbound =
  | { type: "data"; view: string; payload: unknown }
  | { type: "error"; message: string }
  | { type: "busy"; on: boolean };
