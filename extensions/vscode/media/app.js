// AgentWatch webview renderer. Rules: build DOM with createElement and
// textContent only (never innerHTML with data), keep the VS Code API object
// private, accept only known message shapes.
(function () {
  "use strict";
  const vscode = acquireVsCodeApi();
  const root = document.getElementById("root");
  const VIEW = document.body.dataset.view || "";
  const VIEWS = ["live", "runs", "findings", "approvals", "gate", "audit", "connect"];
  if (!VIEWS.includes(VIEW)) return;

  function send(msg) { vscode.postMessage(msg); }

  /** h("div.card.row", {onclick}, child...) */
  function h(spec, props, ...kids) {
    const [tag, ...classes] = spec.split(".");
    const el = document.createElement(tag || "div");
    if (classes.length) el.className = classes.join(" ");
    for (const [k, v] of Object.entries(props || {})) {
      if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
      else if (k === "title" || k === "aria-label" || k === "role" || k === "type") el.setAttribute(k, String(v));
      else if (k === "disabled" && v) el.setAttribute("disabled", "");
    }
    for (const kid of kids.flat()) {
      if (kid == null || kid === false) continue;
      el.appendChild(typeof kid === "string" || typeof kid === "number" ? document.createTextNode(String(kid)) : kid);
    }
    return el;
  }
  const text = (v) => (v == null ? "" : String(v));
  const link = (label, path) => h("button.link", { onclick: () => send({ type: "open", path }) }, label);
  const btn = (label, cls, onclick, disabled) => h("button.btn" + (cls ? "." + cls : ""), { onclick, disabled, type: "button" }, label);
  const when = (iso) => { if (!iso) return ""; const d = new Date(iso); return isNaN(d) ? "" : d.toLocaleString(); };

  function chip(step) {
    const a = step && (step.args_redacted || step.input_data);
    const v = a && typeof a === "object" ? a.agentwatch : null;
    let label = null;
    if (v) {
      if (v.would_have && v.decision === "allow") label = "DRY RUN";
      else label = ({ allow: "ALLOW", scrub: "SCRUB", hold: "HOLD", deny: "DENY", reroute: "REROUTE" })[v.decision] || null;
    }
    if (!label) return h("span.chip.chip-none", {}, step.step_type === "tool_call" ? "—" : text(step.step_type));
    return h("span.chip.chip-" + label.toLowerCase().replace(/\s+/g, "-"), { title: v && v.source ? "decided by: " + v.source : "" }, label);
  }

  function errorBox(p) {
    if (p.code === "operator_required" || p.code === "device_scope") {
      return h("div.error.stack", {},
        h("h3", {}, "Operator sign-in needed"),
        h("p", {}, "This view covers your whole AgentWatch account. This editor's device sign-in reads only this machine's own runs."),
        h("div.row", {},
          btn("Sign in as operator", "primary", () => send({ type: "operatorSignIn" })),
          link("Open in dashboard", "/dashboard")));
    }
    const needsSignIn = p.code === "not_signed_in" || p.code === "401";
    return h("div.error.stack", {},
      h("h3", {}, needsSignIn ? "Not signed in" : "AgentWatch is unavailable"),
      h("p", {}, text(p.error)),
      h("div.row", {},
        needsSignIn ? btn("Sign in", "primary", () => send({ type: "signIn" })) : btn("Retry", "", () => send({ type: "refresh" })),
        link("Open in dashboard", "/dashboard")));
  }

  function needOperator(p) {
    return p.operator ? null : h("p.small", {}, "Approving, isolating and exporting need an operator sign-in. ",
      h("button.link", { onclick: () => send({ type: "operatorSignIn" }) }, "Sign in as operator"), " or ", link("open the dashboard", "/approvals"), ".");
  }

  const render = {
    live(p) {
      if (!p.run) return h("div.empty", {}, "No session yet. Start Claude Code, Cursor, Codex or Copilot with AgentWatch connected.");
      const r = p.run;
      return h("div.stack", {},
        h("div.row.between", {}, h("h1.ellipsis.grow", { title: r.id }, text(r.agent_name)),
          h("span.badge", {}, text(r.status))),
        h("p.small", {}, `${r.total_steps} steps · started ${when(r.started_at)} · `, link("Open in dashboard", "/runs/" + r.id)),
        h("ul.steps", {}, (p.steps || []).slice().reverse().map((s) => {
          const a = s.args_redacted || s.input_data || {};
          const what = a.command || a.path || (a.mcp_server ? a.mcp_server + " / " + (a.mcp_tool || "") : "") || "";
          return h("li", {}, h("span.num", {}, "#" + s.step_number), chip(s),
            h("span.grow.ellipsis", { title: text(what) }, h("strong", {}, text(s.tool_name)), " ", h("span.mono.muted", {}, text(what))));
        })));
    },
    runs(p) {
      if (!p.runs || !p.runs.length) return h("div.empty", {}, "No runs from this machine yet.");
      return h("div.stack", {}, p.runs.map((r) => h("div.card", {},
        h("div.row.between", {}, h("h3.ellipsis.grow", {}, text(r.agent_name)), h("span.badge", {}, text(r.status))),
        h("p.small", {}, `${r.total_steps} steps · ${when(r.started_at)}`),
        h("div.row", {}, btn("Live view", "", () => send({ type: "selectRun", runId: r.id })), link("Open in dashboard", "/runs/" + r.id)))));
    },
    findings(p) {
      const out = [];
      for (const a of p.posture || []) {
        const fails = (a.items || []).filter((i) => i.status === "fail");
        out.push(h("div.card", {}, h("div.row.between", {}, h("h3", {}, text(a.agent)),
          h("span.badge." + (fails.length ? "fail" : "pass"), {}, fails.length ? fails.length + " failing" : "no failing checks")),
        (a.items || []).filter((i) => i.status === "fail" || i.status === "warn").map((i) => {
          const w = (p.why || {})[i.check_id] || {};
          return h("div.stack", {}, h("div.row", {}, h("span.mono", {}, text(i.check_id)), h("strong", {}, text(i.title || w.title))),
            h("p.small", {}, text(i.plain || "")), w.why ? h("p.small.why", {}, "Why it matters: " + w.why) : null);
        })));
      }
      if (p.events && p.events.length) {
        out.push(h("h2", {}, "Recent findings"));
        out.push(h("ul.steps", {}, p.events.map((e) => {
          const ev = e.evidence || {};
          return h("li", {}, h("span.chip.chip-" + (e.severity === "high" || e.severity === "critical" ? "deny" : "hold"), {}, text(e.severity)),
            h("span.grow", {}, h("strong", {}, text(e.event_type)), " ", h("span.muted", {}, text(ev.summary || ""))));
        })));
      }
      if (!out.length) return h("div.empty", {}, "No findings for this machine's agents.");
      out.push(link("All findings in the dashboard", "/posture"));
      return h("div.stack", {}, out);
    },
    approvals(p) {
      const items = p.items || [];
      return h("div.stack", {}, needOperator(p),
        items.length ? items.map((i) => h("div.card.stack", {},
          h("div.row.between", {}, h("h3", {}, text(i.agent_name || "agent") + " · " + text(i.tool_name || "")), h("span.chip.chip-hold", {}, "HOLD")),
          h("p.small", {}, "Requested " + when(i.created_at) + (i.rule_name ? " · " + i.rule_name : "")),
          i.tool_input ? h("pre.json", {}, JSON.stringify(i.tool_input, null, 2)) : null,
          i.justification ? h("p.small", {}, "Agent's reason (untrusted): " + text(i.justification)) : null,
          h("div.row", {},
            btn("Approve once", "primary", () => send({ type: "decide", kind: i.kind, id: i.id, outcome: "APPROVE_ONCE" }), !p.operator),
            btn("Deny", "danger", () => send({ type: "decide", kind: i.kind, id: i.id, outcome: "DENY" }), !p.operator),
            link("Open in dashboard", "/approvals"))))
          : h("div.empty", {}, "Nothing waiting for approval."));
    },
    gate(p) {
      const mine = new Set(p.mine || []);
      const list = (p.passports || []).filter((x) => mine.has(x.agent_name)).concat((p.passports || []).filter((x) => !mine.has(x.agent_name)));
      if (!list.length) return h("div.empty", {}, "No passports yet.");
      return h("div.stack", {}, needOperator(p), list.slice(0, 30).map((x) => h("div.card.stack", {},
        h("div.row.between", {}, h("h3.ellipsis.grow", {}, text(x.agent_name)),
          x.isolated ? h("span.chip.chip-deny", {}, "ISOLATED") : h("span.badge", {}, text(x.status))),
        h("p.small", {}, `policy ${text(x.policy_mode)} · ${text(x.enforce_mode)}` +
          ((x.denied_tools || []).length ? " · denied: " + x.denied_tools.join(", ") : "") +
          ((x.approval_required_tools || []).length ? " · approval: " + x.approval_required_tools.join(", ") : "")),
        h("div.row", {},
          x.isolated ? btn("Restore", "", () => send({ type: "restore", passportId: x.id, agent: x.agent_name }), !p.operator)
            : btn("Isolate", "danger", () => send({ type: "isolate", passportId: x.id, agent: x.agent_name }), !p.operator),
          link("Open in dashboard", "/gate")))));
    },
    audit(p) {
      const v = p.verify || {};
      const chains = v.chains || {};
      return h("div.stack", {},
        h("div.card.stack", {}, h("div.row.between", {}, h("h3", {}, "Hash-chained audit log"),
          h("span.badge." + (v.ok ? "pass" : "fail"), {}, v.ok ? "verified" : "NOT verified")),
          Object.entries(chains).map(([name, c]) => h("p.small", {}, `${name}: ${c.entries} entries, ${c.ok ? "intact" : "BROKEN"}`)),
          h("div.row", {}, btn("Verify again", "", () => send({ type: "verifyAudit" })), link("Open in dashboard", "/audit"))),
        h("div.card.stack", {}, h("h3", {}, "Signed evidence bundle"),
          h("p.small", {}, "Ed25519-signed export an auditor can verify offline."),
          needOperator(p),
          btn("Export bundle…", "primary", () => send({ type: "exportEvidence" }), !p.operator)));
    },
    connect(p) {
      if (p.error) return h("div.error", {}, text(p.error));
      const d = p.doctor || { sensor: [], clients: [] };
      const plans = Object.fromEntries((p.plans || []).map((x) => [x.client, x]));
      return h("div.stack", {},
        h("h1", {}, "Connect coding agents"),
        h("p", {}, "AgentWatch adds hooks to each agent's user settings. You see the exact change first; a backup is kept and Uninstall restores it."),
        h("div.card.stack", {}, h("h2", {}, "This machine"),
          h("ul.check", {}, d.sensor.map((c) => h("li", {}, h("span.mark." + (c.ok && !c.warn ? "yes" : c.warn ? "wait" : "no"), {}, c.ok && !c.warn ? "✓" : c.warn ? "!" : "✗"),
            h("span", {}, h("strong", {}, c.name + " "), h("span.muted", {}, text(c.detail)))))),
          h("div.row", {}, btn("Run doctor again", "", () => send({ type: "doctor" })), btn("Sign in", "", () => send({ type: "signIn" })))),
        h("div.grid2", {}, d.clients.map((c) => {
          const plan = plans[c.client] || {};
          const lines = [];
          const before = new Set((plan.before || "").split("\n"));
          const after = new Set((plan.after || "").split("\n"));
          for (const l of (plan.before || "").split("\n")) if (l.trim() && !after.has(l)) lines.push(h("span.diff-del", {}, "- " + l + "\n"));
          for (const l of (plan.after || "").split("\n")) if (l.trim() && !before.has(l)) lines.push(h("span.diff-add", {}, "+ " + l + "\n"));
          return h("div.card.stack", {},
            h("div.row.between", {}, h("h3", {}, h("span.status-dot." + c.status, {}), " " + c.client),
              h("span.badge", {}, c.status === "green" ? "connected" : c.status === "red" ? "problem" : c.detected ? "found, not connected" : "not found")),
            c.checks.map((k) => h("p.small", {}, text(k.detail))),
            plan.path ? h("p.small.mono", {}, (plan.exists ? "changes " : "creates ") + plan.path) : null,
            plan.changed && lines.length ? h("pre.json", {}, lines) : null,
            plan.note ? h("p.small", {}, plan.note) : null,
            h("div.row", {},
              btn(c.status === "green" ? "Reinstall" : "Install", "primary", () => send({ type: "install", client: c.client })),
              c.status !== "not_installed" ? btn("Uninstall", "", () => send({ type: "uninstall", client: c.client })) : null));
        })));
    },
  };

  function renderIsolate(p) {
    const box = h("div.card.stack", { role: "status" }, h("h3", {}, "Isolating " + text(p.agent)),
      h("ul.check", {}, (p.steps || []).map((s) => h("li", {},
        h("span.mark." + (s.done === true ? "yes" : s.done === false ? "no" : "wait"), {}, s.done === true ? "✓" : s.done === false ? "✗" : "…"),
        h("span", {}, text(s.label) + (s.detail ? " (" + s.detail + ")" : ""))))),
      h("div.row", {}, btn("Back", "", () => send({ type: "refresh" })), link("Open in dashboard", "/gate")));
    root.replaceChildren(box);
  }

  window.addEventListener("message", (e) => {
    const m = e.data;
    if (!m || typeof m !== "object" || typeof m.type !== "string") return;
    if (m.type === "data" && typeof m.view === "string" && m.payload && typeof m.payload === "object") {
      if (m.view === "isolate" && VIEW === "gate") return renderIsolate(m.payload);
      if (m.view !== VIEW) return;
      const node = m.payload.error ? errorBox(m.payload) : render[VIEW](m.payload);
      root.replaceChildren(node);
    }
  });
  root.replaceChildren(h("p.muted", {}, "Loading…"));
  send({ type: "ready" });
})();
