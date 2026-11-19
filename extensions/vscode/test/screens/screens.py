"""Screenshots of every AgentWatch webview, light and dark, with mock data.

Renders media/app.js + media/app.css in Chromium exactly as the webview
does (same body classes VS Code sets), with a stub acquireVsCodeApi, and
writes extensions/vscode/screenshots/<view>-<theme>.png.

    python3 test/screens/screens.py
"""
import json
from pathlib import Path

from playwright.sync_api import sync_playwright

EXT = Path(__file__).resolve().parents[2]
OUT = EXT / "screenshots"
OUT.mkdir(exist_ok=True)

STEP = lambda n, tool, decision, cmd=None, path=None, would=None, src="server": {  # noqa: E731
    "step_number": n, "step_type": "tool_call", "tool_name": tool,
    "args_redacted": {"agentwatch": {"decision": decision, "source": src, **({"would_have": would} if would else {})},
                      **({"command": cmd} if cmd else {}), **({"path": path} if path else {})},
}

MOCK = {
    "live": {"dashboard": "", "run": {"id": "8f31c816-8bf1-4c1b-8080-1c40531c6826", "agent_name": "claude-code@dev", "status": "running",
                                       "total_steps": 9, "started_at": "2026-11-19T09:31:37Z"},
             "steps": [STEP(1, "Read", "allow", path="/Users/dev/project/README.md", src="local"),
                       STEP(2, "Bash", "allow", cmd="go test ./..."),
                       STEP(3, "Edit", "allow", path="/Users/dev/project/parser/parser.go"),
                       STEP(4, "Bash", "scrub", cmd="curl -H 'Authorization: Bearer [REDACTED:auth_header]' https://api.example.com"),
                       STEP(5, "Bash", "allow", cmd="git push --force", would="deny"),
                       STEP(6, "Bash", "hold", cmd="kubectl delete ns staging"),
                       STEP(7, "mcp__github__merge_pr", "reroute"),
                       STEP(8, "Bash", "deny", cmd="rm -rf ~"),
                       STEP(9, "Write", "deny", path="/Users/dev/.claude/settings.json", src="self_protect")]},
    "runs": {"dashboard": "", "runs": [
        {"id": "r1", "agent_name": "claude-code@dev", "status": "running", "total_steps": 9, "started_at": "2026-11-19T09:31:37Z"},
        {"id": "r2", "agent_name": "cursor@dev", "status": "done", "total_steps": 41, "started_at": "2026-11-19T08:02:11Z"},
        {"id": "r3", "agent_name": "codex@dev", "status": "killed", "total_steps": 6, "started_at": "2026-11-18T17:45:00Z"}]},
    "findings": {"dashboard": "", "posture": [{"agent": "claude-code@dev", "items": [
        {"check_id": "AW-P03", "title": "Borrowed identity", "status": "fail", "plain": "The agent acts as you: anything it does looks like you did it."},
        {"check_id": "AW-P05", "title": "Approvals off", "status": "fail", "plain": "No tool needs approval before it runs."}]}],
        "events": [{"event_type": "self_protection_block", "severity": "high", "agent_name": "claude-code@dev",
                    "evidence": {"summary": "agent tried to modify AgentWatch's own files"}},
                   {"event_type": "approval_bypass_launch", "severity": "high", "agent_name": "claude-code@dev",
                    "evidence": {"summary": "claude-code was launched with --dangerously-skip-permissions"}},
                   {"event_type": "sensor_would_block", "severity": "medium", "agent_name": "claude-code@dev",
                    "evidence": {"summary": "dry_run: Bash would have been denied"}}],
        "why": {"AW-P03": {"title": "Borrowed identity", "why": "Acting as a person hides which agent did what and gives it that person's access."},
                "AW-P05": {"title": "Approvals off", "why": "Nothing stops a destructive or costly call before it happens."}}},
    "approvals": {"dashboard": "", "operator": True, "items": [
        {"kind": "run", "id": "a1", "agent_name": "claude-code@dev", "tool_name": "Bash", "created_at": "2026-11-19T09:40:00Z",
         "rule_name": "Bash requires approval on this passport", "tool_input": {"command": "kubectl delete ns staging", "class": "shell"},
         "justification": "Cleaning up the old staging namespace as asked."}]},
    "gate": {"dashboard": "", "operator": True, "mine": ["claude-code@dev", "cursor@dev"], "passports": [
        {"id": "p1", "agent_name": "claude-code@dev", "status": "active", "policy_mode": "enforce", "enforce_mode": "fail_closed",
         "isolated": False, "denied_tools": ["WebFetch"], "approval_required_tools": ["Bash"]},
        {"id": "p2", "agent_name": "cursor@dev", "status": "suspended", "policy_mode": "dry_run", "enforce_mode": "fail_closed",
         "isolated": True, "denied_tools": [], "approval_required_tools": []}]},
    "audit": {"dashboard": "", "operator": False, "verify": {"ok": True, "chains": {
        "compliance": {"entries": 1912, "ok": True}, "governance": {"entries": 214, "ok": True}}}},
    "connect": {"doctor": {"ok": False, "sensor": [
        {"name": "version", "ok": True, "detail": "0.1.0 darwin-arm64"},
        {"name": "config", "ok": True, "detail": "mode dry_run (config file), fail_open, preflight timeout 800 ms"},
        {"name": "credentials", "ok": True, "detail": "device in keychain"},
        {"name": "api", "ok": True, "detail": "https://api.agentwatch.in reachable in 84 ms"},
        {"name": "spool", "ok": True, "detail": "0 queued, 0 dropped"}],
        "clients": [
            {"client": "claude-code", "detected": True, "status": "green", "checks": [{"ok": True, "detail": "/Users/dev/.claude/settings.json"}]},
            {"client": "cursor", "detected": True, "status": "red", "checks": [{"ok": False, "detail": "AgentWatch hooks were removed from /Users/dev/.cursor/hooks.json"}]},
            {"client": "codex", "detected": False, "status": "not_installed", "checks": [{"ok": False, "warn": True, "detail": "not installed (agentwatch install codex)"}]},
            {"client": "copilot", "detected": True, "status": "not_installed", "checks": [{"ok": False, "warn": True, "detail": "not installed (agentwatch install copilot)"}]}]},
        "plans": [{"client": "cursor", "path": "/Users/dev/.cursor/hooks.json", "exists": True, "changed": True,
                   "before": '{\n  "version": 1,\n  "hooks": {}\n}\n',
                   "after": '{\n  "version": 1,\n  "hooks": {\n    "preToolUse": [\n      {\n        "command": "\'/Users/dev/.agentwatch/bin/agentwatch\' hook cursor",\n        "timeout": 5\n      }\n    ]\n  }\n}\n'}]},
}

ISOLATE = {"agent": "cursor@dev", "steps": [
    {"label": "Gateway keys revoked", "done": True, "detail": "1 key(s)"}, {"label": "Passport suspended", "done": True},
    {"label": "Every tool denied", "done": True}, {"label": "Open runs killed", "done": True, "detail": "1 run(s)"},
    {"label": "New runs refused", "done": True}]}

PAGE = """<!doctype html><html><head><meta charset="utf-8">
<link rel="stylesheet" href="{css}"></head>
<body class="{cls}" data-view="{view}"><main id="root"></main>
<script>window.acquireVsCodeApi = () => ({{ postMessage: (m) => (window.__sent = (window.__sent || []).concat([m])) }});</script>
<script src="{js}"></script></body></html>"""


def main() -> None:
    css = (EXT / "media" / "app.css").as_uri()
    js = (EXT / "media" / "app.js").as_uri()
    shots = []
    with sync_playwright() as p:
        b = p.chromium.launch()
        for theme, cls in (("light", "vscode-light"), ("dark", "vscode-dark")):
            for view, data in MOCK.items():
                width = 1100 if view == "connect" else 380
                pg = b.new_page(viewport={"width": width, "height": 900}, device_scale_factor=2)
                errors = []
                pg.on("pageerror", lambda e: errors.append(str(e)))
                html = PAGE.format(css=css, js=js, cls=cls, view=view)
                tmp = EXT / "test" / "screens" / f".{view}.html"
                tmp.write_text(html)
                pg.goto(tmp.as_uri())
                pg.evaluate("d => window.postMessage({type: 'data', view: d.view, payload: d.payload}, '*')",
                            {"view": view, "payload": data})
                pg.wait_for_timeout(250)
                if pg.evaluate("document.querySelector('#root').innerText.includes('Loading')"):
                    errors.append("view did not render")
                out = OUT / f"{view}-{theme}.png"
                pg.screenshot(path=str(out), full_page=True)
                shots.append(out.name)
                if errors:
                    raise SystemExit(f"{view}/{theme}: {errors}")
                if view == "gate":
                    pg.evaluate("d => window.postMessage({type: 'data', view: 'isolate', payload: d}, '*')", ISOLATE)
                    pg.wait_for_timeout(200)
                    pg.screenshot(path=str(OUT / f"gate-isolate-{theme}.png"), full_page=True)
                    shots.append(f"gate-isolate-{theme}.png")
                tmp.unlink()
                pg.close()
        b.close()
    print(json.dumps(shots, indent=1))


if __name__ == "__main__":
    main()
