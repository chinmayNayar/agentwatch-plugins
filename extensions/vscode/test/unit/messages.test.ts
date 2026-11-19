import * as assert from "assert";
import { parseInbound } from "../../src/messages";
import { chipForStep, dashboardUrl, isolateSteps, maskKey, validApiUrl, WHY } from "../../src/format";

describe("parseInbound", () => {
  it("accepts every known message", () => {
    for (const m of [
      { type: "ready" }, { type: "refresh" }, { type: "signIn" }, { type: "operatorSignIn" }, { type: "doctor" },
      { type: "verifyAudit" }, { type: "exportEvidence" }, { type: "open", path: "/runs/abc-123" },
      { type: "decide", kind: "run", id: "a1b2-c3", outcome: "APPROVE_ONCE" },
      { type: "decide", kind: "gate", id: "x", outcome: "DENY" },
      { type: "isolate", passportId: "p-1", agent: "claude-code@dev" },
      { type: "restore", passportId: "p-1", agent: "cursor@dev" },
      { type: "install", client: "cursor" }, { type: "uninstall", client: "codex" }, { type: "preview", client: "copilot" },
      { type: "selectRun", runId: "r-1" },
    ]) {
      assert.ok(parseInbound(m), JSON.stringify(m));
    }
  });

  it("rejects anything else", () => {
    for (const m of [
      null, "ready", [], { type: "eval" }, { type: "open", path: "https://evil.example" },
      { type: "open", path: "//evil.example/x" }, { type: "open", path: "/../../etc" },
      { type: "open", path: "/x\\y" }, { type: "open", path: "javascript:alert(1)" },
      { type: "decide", kind: "run", id: "x", outcome: "APPROVE_FOR_TOOL" },
      { type: "decide", kind: "other", id: "x", outcome: "DENY" },
      { type: "decide", kind: "run", id: "'; drop", outcome: "DENY" },
      { type: "isolate", passportId: "p", agent: "a b <script>" },
      { type: "install", client: "vim" }, { type: "selectRun", runId: "" },
    ]) {
      assert.strictEqual(parseInbound(m), null, JSON.stringify(m));
    }
  });

  it("drops extra fields", () => {
    assert.deepStrictEqual(parseInbound({ type: "install", client: "cursor", exe: "/bin/sh" }), { type: "install", client: "cursor" });
  });
});

describe("chips", () => {
  const step = (agentwatch: unknown) => ({ args_redacted: { agentwatch } });
  it("maps decisions to the brand chips", () => {
    assert.strictEqual(chipForStep(step({ decision: "allow" })), "ALLOW");
    assert.strictEqual(chipForStep(step({ decision: "scrub" })), "SCRUB");
    assert.strictEqual(chipForStep(step({ decision: "hold" })), "HOLD");
    assert.strictEqual(chipForStep(step({ decision: "deny" })), "DENY");
    assert.strictEqual(chipForStep(step({ decision: "reroute" })), "REROUTE");
    assert.strictEqual(chipForStep(step({ decision: "allow", would_have: "deny" })), "DRY RUN");
    assert.strictEqual(chipForStep({ args_redacted: null }), null);
  });
});

describe("findings", () => {
  it("explains every AW-P and AW-D id", () => {
    for (let i = 1; i <= 12; i++) assert.ok(WHY[`AW-P${String(i).padStart(2, "0")}`]?.why);
    for (let i = 1; i <= 13; i++) assert.ok(WHY[`AW-D${String(i).padStart(2, "0")}`]?.why);
  });
});

describe("isolate steps", () => {
  it("reads the API's explanation and the probe", () => {
    const steps = isolateSteps({
      isolation: { decision: "isolate", signals: [{ action: "revoke_gateway_keys", count: 2 }, { action: "kill_running_runs", run_ids: ["a"] }] },
      passport: { status: "suspended", isolation: { isolated_at: "x" } },
      newRunRefused: true,
    });
    assert.deepStrictEqual(steps.map((s) => s.done), [true, true, true, true, true]);
    assert.strictEqual(steps[0].detail, "2 key(s)");
    const dry = isolateSteps({ isolation: { decision: "allow", would_have: "isolate" }, passport: { status: "active" }, newRunRefused: false });
    assert.deepStrictEqual(dry.map((s) => s.done), [false, false, false, false, false]);
  });
});

describe("urls and keys", () => {
  it("derives the console URL", () => {
    assert.strictEqual(dashboardUrl("https://api.agentwatch.in"), "https://app.agentwatch.in");
    assert.strictEqual(dashboardUrl("http://localhost:8000"), "http://localhost:3000");
    assert.strictEqual(dashboardUrl("https://x", "https://console.example/"), "https://console.example");
  });
  it("only allows https or loopback http", () => {
    assert.ok(validApiUrl("https://api.agentwatch.in"));
    assert.ok(validApiUrl("http://localhost:8000"));
    assert.ok(!validApiUrl("http://api.agentwatch.in"));
    assert.ok(!validApiUrl("file:///etc/passwd"));
  });
  it("never shows a whole key", () => {
    const k = "aw_" + "0".repeat(32); // assembled: no key-shaped literal in the repo
    assert.ok(!maskKey(k).includes(k.slice(6, -4)));
    assert.strictEqual(maskKey("short"), "••••");
  });
});
