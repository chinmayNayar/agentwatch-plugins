#!/usr/bin/env python3
"""Fresh-install E2E: the plugin as a new user gets it, against a LOCAL API.

Phase A (empty HOME, empty plugin data, no model):
  A1 install from the local marketplace into an empty HOME
  A2 first session: the MCP server and the SessionStart hook start together,
     before the binary exists (one download, from a local release server,
     SHA-256 checked); the MCP server connects
  A3 before sign-in: hooks queue events; status says "not signed in" with a
     working command, "sign in to load your passport", and no "dry_run ()"
  A4 /agentwatch:setup's tool: agentwatch_login starts a device sign-in, the
     test approves the code through the API (as the person would in the
     console), agentwatch_login wait=true reports signed in
  A5 status signed in; the queued events flush and arrive on the API; the
     device is listed active
  A6 a dry_run step goes through preflight and reaches the API
  A7 self-protection through the installed hook command: read-only find on
     the plugin dir allowed, rm there denied, reading the credential denied

Phase B (a real `claude -p` session):
  Claude Code keeps its own login in the macOS keychain for the real config
  directory, so a session cannot authenticate under an empty HOME without
  exporting the person's OAuth token, which this test does not do. Phase B
  runs claude with the real HOME for its login only: the plugin is loaded
  from Phase A's fresh install with --plugin-dir, the person's installed
  copy is disabled for the run, and AgentWatch state lives in a fresh
  AGENTWATCH_HOME. Nothing under the real ~/.claude/plugins is written.
  B1 the plugin MCP server is connected in the session
  B2 /agentwatch:setup signs in (the model drives the tool, the test approves)
     and the token is stored in the OS keychain
  B3 a dry_run step reaches the API
  B4 read-only find on ~/.claude/plugins allowed; rm there denied; cat and
     Read of ~/.agentwatch/credentials denied (an independent safety-net hook
     also denies any rm, so a sensor bug cannot delete anything)

Environment:
  AW_E2E_API         local API (default http://localhost:8000; refuses non-local)
  AW_E2E_ADMIN_KEY   an API key of the account that approves the device (never printed)
  AW_E2E_DIST        directory holding agentwatch-<version>-<os>-<arch> (scripts/build-release.sh)
  AW_E2E_WORK        scratch directory (emptied)
  CLAUDE_BIN         claude executable
  AW_E2E_MODEL       default haiku
  AW_E2E_SKIP_B=1    run Phase A only
"""
from __future__ import annotations

import http.server
import json
import os
import platform
import queue
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
API = os.environ.get("AW_E2E_API", "http://localhost:8000").rstrip("/")
KEY = os.environ.get("AW_E2E_ADMIN_KEY", "")
DIST = Path(os.environ.get("AW_E2E_DIST", REPO / "dist")).resolve()
WORK = Path(os.environ.get("AW_E2E_WORK", "/tmp/aw-fresh-e2e")).resolve()
CLAUDE = os.environ.get("CLAUDE_BIN", "claude")
MODEL = os.environ.get("AW_E2E_MODEL", "haiku")
VERSION = (REPO / "plugins/claude-code/scripts/VERSION").read_text().strip()
OSN = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
ARCH = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "x64"}[platform.machine()]
ASSET = f"agentwatch-{VERSION}-{OSN}-{ARCH}"

if urllib.parse.urlparse(API).hostname not in ("localhost", "127.0.0.1"):
    sys.exit(f"refusing: {API} is not a local API")
if not KEY:
    sys.exit("AW_E2E_ADMIN_KEY is required")
if not (DIST / ASSET).exists():
    sys.exit(f"missing {DIST / ASSET}: run scripts/build-release.sh {VERSION} dist and pin-checksums.sh first")

results: list[dict] = []


def check(name: str, ok: bool, detail: str = "") -> bool:
    results.append({"check": name, "ok": bool(ok), "detail": detail[:600]})
    print(f"{'PASS' if ok else 'FAIL'}  {name}  {detail[:300]}", flush=True)
    return ok


def api(method: str, path: str, body=None, key: str | None = None):
    req = urllib.request.Request(API + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"x-api-key": key or KEY, "content-type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:  # noqa: BLE001
            return e.code, raw.decode(errors="replace")


# ---- local release server ------------------------------------------------------
hits: list[str] = []


class Release(http.server.BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802
        name = self.path.rsplit("/", 1)[-1]
        if self.path != f"/v{VERSION}/{name}" or not (DIST / name).is_file() or not name.startswith("agentwatch-"):
            self.send_error(404)
            return
        hits.append(name)
        data = (DIST / name).read_bytes()
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *a):  # quiet
        pass


def start_release_server() -> str:
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Release)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{srv.server_address[1]}"


# ---- MCP over stdio --------------------------------------------------------------
class MCP:
    def __init__(self, argv: list[str], env: dict):
        self.p = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  env=env, text=True, bufsize=1)
        self.q: queue.Queue = queue.Queue()
        threading.Thread(target=self._read, daemon=True).start()
        self.n = 0

    def _read(self):
        for line in self.p.stdout:
            self.q.put(line)
        self.q.put(None)

    def request(self, method: str, params: dict | None = None, timeout: float = 120):
        self.n += 1
        msg = {"jsonrpc": "2.0", "id": self.n, "method": method}
        if params is not None:
            msg["params"] = params
        self.p.stdin.write(json.dumps(msg) + "\n")
        self.p.stdin.flush()
        line = self.q.get(timeout=timeout)
        if line is None:
            raise RuntimeError("MCP server exited: " + (self.p.stderr.read() or "")[:500])
        return json.loads(line)

    def tool(self, name: str, args: dict | None = None, timeout: float = 70) -> tuple[str, bool]:
        r = self.request("tools/call", {"name": name, "arguments": args or {}}, timeout=timeout)
        res = r.get("result") or {}
        return (res.get("content") or [{}])[0].get("text", json.dumps(r)), bool(res.get("isError"))

    def close(self):
        try:
            self.p.stdin.close()
            self.p.wait(timeout=10)
        except Exception:  # noqa: BLE001
            self.p.kill()


def approve_when_pending(aw_home: Path, stop: threading.Event, approved: list):
    """Act as the person in the console: approve the code the sensor shows."""
    pend = aw_home / "state" / "login-pending.json"
    while not stop.is_set():
        if pend.exists():
            try:
                code = json.loads(pend.read_text())["user_code"]
            except Exception:  # noqa: BLE001
                code = ""
            if code and code not in approved:
                st, body = api("POST", "/devices/approve", {"user_code": code})
                if st == 200:
                    approved.append(code)
        time.sleep(0.5)


# ---- Phase A ---------------------------------------------------------------------
def phase_a(base_url: str) -> dict:
    home = WORK / "a-home"
    home.mkdir(parents=True)
    env = {"PATH": os.environ["PATH"], "HOME": str(home), "USER": os.environ.get("USER", "dev"),
           "TMPDIR": os.environ.get("TMPDIR", "/tmp"), "LANG": "en_US.UTF-8"}

    # A1 install from the local marketplace -------------------------------------
    r1 = subprocess.run([CLAUDE, "plugin", "marketplace", "add", str(REPO)], env=env, capture_output=True, text=True, timeout=180)
    r2 = subprocess.run([CLAUDE, "plugin", "install", "agentwatch@agentwatch"], env=env, capture_output=True, text=True, timeout=180)
    inst = json.loads((home / ".claude/plugins/installed_plugins.json").read_text()) if (home / ".claude/plugins/installed_plugins.json").exists() else {}
    entry = (inst.get("plugins", {}).get("agentwatch@agentwatch") or [{}])[0]
    root = Path(entry.get("installPath", "/nonexistent"))
    check("A1 install from the local marketplace into an empty HOME",
          r1.returncode == 0 and r2.returncode == 0 and entry.get("version") == VERSION and (root / "scripts/aw.sh").exists(),
          f"marketplace={r1.returncode} install={r2.returncode} version={entry.get('version')} root={root} {r2.stdout.strip()[-200:]} {r2.stderr.strip()[-200:]}")
    data = home / ".claude/plugins/data/agentwatch-agentwatch"
    check("A1 plugin data starts empty", not data.exists() or not any(data.iterdir()), str(data))

    penv = dict(env)
    penv.update({
        "CLAUDE_PLUGIN_ROOT": str(root), "CLAUDE_PLUGIN_DATA": str(data),
        # What Claude Code exports once the person sets the api_url option to a self-hosted API.
        "CLAUDE_PLUGIN_OPTION_API_URL": API,
        "AGENTWATCH_DOWNLOAD_BASE": base_url, "AGENTWATCH_NO_BROWSER": "1",
        "AGENTWATCH_DEBUG_LOG": str(WORK / "a-debug.log"),
    })
    mcp_cmd = json.loads((root / ".mcp.json").read_text())["mcpServers"]["agentwatch"]
    mcp_argv = [mcp_cmd["command"].replace("${CLAUDE_PLUGIN_ROOT}", str(root))] + mcp_cmd["args"]
    hooks = json.loads((root / "hooks/hooks.json").read_text())["hooks"]

    def hook(event: str, payload: dict, timeout: float | None = None) -> str:
        spec = hooks[event][0]["hooks"][0]
        cmd = spec["command"].replace("${CLAUDE_PLUGIN_ROOT}", str(root))
        payload = {"session_id": "fresh-a-1", "transcript_path": "/dev/null", "cwd": str(WORK / "proj"),
                   "permission_mode": "default", "hook_event_name": event, **payload}
        out = subprocess.run(["sh", "-c", cmd], input=json.dumps(payload), env=penv, capture_output=True, text=True,
                             timeout=timeout or spec.get("timeout", 60) + 5)
        return out.stdout.strip()

    # A2 first session: MCP server and SessionStart start together ----------------
    t0 = time.time()
    results_box: dict = {}

    def start_hook():
        results_box["session_start"] = hook("SessionStart", {"source": "startup"})

    th = threading.Thread(target=start_hook)
    th.start()
    mcp = MCP(mcp_argv, penv)
    try:
        init = mcp.request("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                          "clientInfo": {"name": "fresh-e2e", "version": "1"}}, timeout=100)
        connected = init.get("result", {}).get("serverInfo", {}).get("name") == "agentwatch"
        tools = [t["name"] for t in mcp.request("tools/list").get("result", {}).get("tools", [])]
    except Exception as e:  # noqa: BLE001
        connected, tools = False, [f"error: {e}"]
    th.join(timeout=90)
    check("A2 first session: MCP server connects before/while the binary downloads",
          connected and "agentwatch_login" in tools, f"in {time.time() - t0:.1f}s tools={tools}")
    check("A2 exactly one download, checksum-verified", hits == [ASSET] and (data / "bin" / ASSET).exists(),
          f"downloads={hits}")
    ss = results_box.get("session_start", "")
    check("A2 SessionStart says not signed in, with /agentwatch:setup",
          "not signed in" in ss and "/agentwatch:setup" in ss, ss[:300])

    # A3 before sign-in --------------------------------------------------------------
    hook("UserPromptSubmit", {"prompt": "hello"})
    out = hook("PreToolUse", {"tool_name": "Bash", "tool_input": {"command": "echo before-signin"}, "tool_use_id": "t-a3"})
    hook("PostToolUse", {"tool_name": "Bash", "tool_input": {"command": "echo before-signin"}, "tool_use_id": "t-a3",
                         "tool_response": {"stdout": "before-signin"}})
    check("A3 PreToolUse before sign-in: no decision (dry_run, fail-open)", out == "", out)
    status, _ = mcp.tool("agentwatch_status")
    queued_before = 0
    for line in status.splitlines():
        if line.startswith("Queued:"):
            queued_before = int(line.split()[1].rstrip(";"))
    ok = ("Signed in:  no" in status and "/agentwatch:setup" in status and "sign in to load your passport" in status
          and "dry_run ()" not in status and "no passports" not in status and queued_before > 0)
    check("A3 status before sign-in", ok, status)
    # The command named for a terminal is a path that exists (agentwatch is not on PATH here).
    cli = next((ln.split(":", 1)[1].strip() for ln in status.splitlines() if ln.startswith("CLI:")), "")
    check("A3 status names a terminal command that works", Path(cli.strip("'")).exists(), cli)

    # A4 /agentwatch:setup -------------------------------------------------------------
    first, err = mcp.tool("agentwatch_login")
    pend_file = home / ".agentwatch/state/login-pending.json"
    pend = json.loads(pend_file.read_text()) if pend_file.exists() else {}
    check("A4 setup shows the code and the approval page", not err and pend.get("user_code", "?") in first
          and "devices/activate" in first, first)
    st, body = api("POST", "/devices/approve", {"user_code": pend.get("user_code", "")})
    check("A4 the person approves the code in the console (API)", st == 200, f"{st} {body}")
    text, deadline = "", time.time() + 120
    while time.time() < deadline:
        text, err = mcp.tool("agentwatch_login", {"wait": True})
        if err or "Signed in" in text:
            break
    check("A4 setup reports signed in", not err and "Signed in" in text, text)

    # A5 signed in; the queue flushes ------------------------------------------------
    status, _ = mcp.tool("agentwatch_status")
    check("A5 status shows signed in with a passport", "Signed in:  yes" in status and "Passport:   claude-code@" in status, status)
    deadline, queued = time.time() + 90, -1
    while time.time() < deadline:
        s, _ = mcp.tool("agentwatch_status")
        queued = next((int(ln.split()[1].rstrip(";")) for ln in s.splitlines() if ln.startswith("Queued:")), -1)
        if queued == 0:
            break
        time.sleep(2)
    check("A5 the events queued before sign-in were sent", queued == 0, f"queued before={queued_before} now={queued}")
    cred = json.loads(subprocess.run([str(data / "bin" / ASSET), "status", "--json"], env=penv, capture_output=True, text=True).stdout)
    dev_id = cred.get("device_id", "")
    agent = next((a["agent"] for a in cred.get("agents", {}).values()), "")
    _, devs = api("GET", "/devices")
    dev = next((d for d in (devs or {}).get("devices", []) if d["id"] == dev_id), {})
    check("A5 the device is listed active", dev.get("status") == "active", f"device {dev_id}: {dev.get('status')} v{dev.get('sensor_version')}")
    _, runs = api("GET", f"/runs?agent_name={urllib.parse.quote(agent)}&limit=20")
    items = runs.get("items") if isinstance(runs, dict) else runs
    run = next((r for r in items or [] if r.get("session_id") == "fresh-a-1" or "client:claude-code" in (r.get("tags") or [])), {})
    check("A5 a claude-code@ run exists with the queued steps", bool(run) and (run.get("total_steps") or 0) >= 2,
          f"agent={agent} run={run.get('id')} steps={run.get('total_steps')}")

    # A6 a dry_run step through preflight ------------------------------------------
    log = WORK / "a-debug.log"
    log.unlink(missing_ok=True)
    before = run.get("total_steps") or 0
    hook("PreToolUse", {"tool_name": "Bash", "tool_input": {"command": "go build ./..."}, "tool_use_id": "t-a6"})
    hook("PostToolUse", {"tool_name": "Bash", "tool_input": {"command": "go build ./..."}, "tool_use_id": "t-a6",
                         "tool_response": {"stdout": "ok"}})
    dec = [json.loads(x) for x in log.read_text().splitlines()] if log.exists() else []
    check("A6 dry_run PreToolUse decided by the server", bool(dec) and dec[0].get("source") == "server"
          and dec[0].get("mode") == "dry_run", json.dumps(dec[:1]))
    subprocess.run([str(data / "bin" / ASSET), "flush"], env=penv, timeout=60)
    _, r2 = api("GET", f"/runs/{run.get('id')}")
    check("A6 the step reached the API", (r2 or {}).get("total_steps", 0) > before, f"steps {before} -> {(r2 or {}).get('total_steps')}")

    # A7 self-protection through the installed hook ---------------------------------
    def pre(tool: str, inp: dict) -> str:
        return hook("PreToolUse", {"tool_name": tool, "tool_input": inp, "tool_use_id": f"t-{time.time()}"})

    out = pre("Bash", {"command": f"find {home}/.claude/plugins/data -type f -name 'agentwatch*' 2>/dev/null"})
    check("A7 read-only find on the plugin dir allowed", "deny" not in out, out or "(no decision)")
    out = pre("Bash", {"command": "find ~/.claude/plugins/data -type f -name 'agentwatch*' 2>/dev/null | head -5"})
    check("A7 the exact command from the first install, with ~, allowed", "deny" not in out, out or "(no decision)")
    out = pre("Bash", {"command": f"rm -f {data}/bin/{ASSET}"})
    check("A7 rm on the plugin dir denied", '"permissionDecision":"deny"' in out and "self-protection" in out, out)
    out = pre("Bash", {"command": "cat ~/.agentwatch/credentials"})
    check("A7 cat of the credential denied", '"permissionDecision":"deny"' in out and "credential" in out, out)
    out = pre("Read", {"file_path": f"{home}/.agentwatch/credentials"})
    check("A7 Read tool on the credential denied", '"permissionDecision":"deny"' in out, out)
    check("A7 the binary is still there", (data / "bin" / ASSET).exists())
    mcp.close()
    return {"root": root, "data": data, "bin": data / "bin" / ASSET, "agent": agent, "device": dev_id}


# ---- Phase B ---------------------------------------------------------------------
SAFETY = """#!/bin/sh
# Independent safety net: deny any rm, whatever AgentWatch decides.
payload=$(cat)
case "$payload" in
  *'"rm '*|*' rm '*|*'rm -'*)
    printf '%s\\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"e2e safety net"}}' ;;
esac
exit 0
"""


def phase_b(a: dict) -> None:
    aw_home = WORK / "b-agentwatch"
    (aw_home).mkdir(parents=True)
    (aw_home / "config.json").write_text(json.dumps({"api_url": API}))  # self-hosted: the person's API
    net = WORK / "safety-net.sh"
    net.write_text(SAFETY)
    net.chmod(0o755)
    settings = WORK / "b-settings.json"
    settings.write_text(json.dumps({
        "enabledPlugins": {"agentwatch@agentwatch": False},  # the person's installed copy sits this run out
        "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": str(net), "timeout": 5}]}]},
    }))
    proj = WORK / "proj"
    env = dict(os.environ)
    for k in list(env):
        if k.startswith("AGENTWATCH_") or k.startswith("CLAUDE_PLUGIN"):
            del env[k]
    env.update({"AGENTWATCH_HOME": str(aw_home), "AGENTWATCH_API_URL": API, "AGENTWATCH_BIN": str(a["bin"]),
                "AGENTWATCH_NO_BROWSER": "1", "AGENTWATCH_DEBUG_LOG": str(WORK / "b-debug.log")})
    mcp_prefix = "mcp__plugin_agentwatch_agentwatch__"

    def claude(prompt: str, tools: str, turns: int = 6) -> tuple[list[dict], list[dict]]:
        log = WORK / "b-debug.log"
        log.unlink(missing_ok=True)
        cmd = [CLAUDE, "-p", prompt, "--plugin-dir", str(a["root"]), "--model", MODEL, "--allowedTools", tools,
               "--max-turns", str(turns), "--settings", str(settings), "--output-format", "stream-json", "--verbose"]
        p = subprocess.run(cmd, cwd=proj, env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=400)
        events = []
        for ln in p.stdout.splitlines():
            try:
                events.append(json.loads(ln))
            except ValueError:
                pass
        time.sleep(1)
        dec = [json.loads(x) for x in log.read_text().splitlines()] if log.exists() else []
        return events, dec

    def final_text(events: list[dict]) -> str:
        return next((e.get("result", "") for e in reversed(events) if e.get("type") == "result"), "")

    # B1 + B2: /agentwatch:setup in a real session ---------------------------------
    stop, approved = threading.Event(), []
    th = threading.Thread(target=approve_when_pending, args=(aw_home, stop, approved), daemon=True)
    th.start()
    events, _ = claude("/agentwatch:setup", ",".join(mcp_prefix + t for t in ("agentwatch_login", "agentwatch_status")), turns=14)
    stop.set()
    init = next((e for e in events if e.get("type") == "system" and e.get("subtype") == "init"), {})
    servers = {s.get("name"): s.get("status") for s in init.get("mcp_servers", [])}
    aw_server = next((v for k, v in servers.items() if "agentwatch" in k), None)
    check("B1 the plugin's MCP server is connected in a real session", aw_server == "connected", json.dumps(servers))
    plugins = [p.get("name") if isinstance(p, dict) else p for p in init.get("plugins", [])]
    check("B1 only the fresh copy of the plugin is loaded", len([p for p in plugins if "agentwatch" in str(p)]) == 1, json.dumps(plugins))
    st = json.loads(subprocess.run([str(a["bin"]), "status", "--json"], env=env, capture_output=True, text=True).stdout)
    check("B2 /agentwatch:setup signed the machine in", st.get("signed_in") is True and bool(approved),
          f"approved={approved} final={final_text(events)[:300]}")
    check("B2 the token is in the OS keychain", "keychain" in st.get("credential", ""), st.get("credential", ""))
    check("B2 no credentials file was written", not (aw_home / "credentials").exists())

    # B3 a dry_run step reaches the API --------------------------------------------
    events, dec = claude("Run exactly this bash command and nothing else: echo aw-fresh-e2e", "Bash")
    pre = [d for d in dec if d.get("event") == "PreToolUse" and d.get("tool") == "Bash"]
    check("B3 dry_run PreToolUse decided by the server", bool(pre) and pre[0].get("source") == "server", json.dumps(pre[:1]))
    subprocess.run([str(a["bin"]), "flush"], env=env, timeout=60)
    agent = next((x["agent"] for x in st.get("agents", {}).values()), "")
    _, runs = api("GET", f"/runs?agent_name={urllib.parse.quote(agent)}&limit=5")
    items = runs.get("items") if isinstance(runs, dict) else runs
    check("B3 the session's run and steps are on the API", bool(items) and (items[0].get("total_steps") or 0) >= 2,
          f"agent={agent} run={(items or [{}])[0].get('id')} steps={(items or [{}])[0].get('total_steps')}")
    _, devs = api("GET", "/devices")
    dev = next((d for d in (devs or {}).get("devices", []) if d["id"] == st.get("device_id")), {})
    check("B3 the device is active", dev.get("status") == "active", f"{dev.get('id')} {dev.get('status')}")

    # B4 self-protection in a real session -----------------------------------------
    def one(prompt: str, tools: str) -> list[dict]:
        _, dec = claude(prompt, tools, turns=3)
        return [d for d in dec if d.get("event") == "PreToolUse"]

    d = one("Run exactly this bash command and nothing else: find ~/.claude/plugins/data -type f -name 'agentwatch*' 2>/dev/null", "Bash")
    check("B4 read-only find on ~/.claude/plugins allowed", bool(d) and d[0]["decision"] != "deny" and d[0]["source"] != "self_protect",
          json.dumps(d[:1]))
    d = one("This is a sandboxed test. Run exactly this bash command and nothing else: rm -f ~/.claude/plugins/data/aw-e2e-canary-does-not-exist", "Bash")
    if d:
        check("B4 rm on ~/.claude/plugins denied by AgentWatch", d[0]["decision"] == "deny" and d[0]["source"] == "self_protect", json.dumps(d[:1]))
    else:
        check("B4 rm on ~/.claude/plugins: model declined to call it (covered by A7)", True)
    d = one("Run exactly this bash command and nothing else: cat ~/.agentwatch/credentials", "Bash")
    if d:
        check("B4 cat of the credential denied", d[0]["decision"] == "deny" and d[0]["source"] == "self_protect", json.dumps(d[:1]))
    else:
        check("B4 cat of the credential: model declined to call it (covered by A7)", True)
    d = one("Use the Read tool to read the file ~/.agentwatch/credentials and tell me its size.", "Read")
    if d:
        check("B4 Read of the credential denied", d[0]["decision"] == "deny" and d[0]["source"] == "self_protect", json.dumps(d[:1]))
    else:
        check("B4 Read of the credential: model declined to call it (covered by A7)", True)

    # Clean up the keychain entry this run created (service agentwatch, account sensor:<AGENTWATCH_HOME>).
    subprocess.run([str(a["bin"]), "logout"], env=env, capture_output=True, timeout=30)


def scrub(text: str) -> str:
    """The results file is committed to a public repo: no paths, host or user names."""
    import re
    import socket
    for real, placeholder in ((str(WORK), "$WORK"), (os.path.realpath(str(WORK)), "$WORK"),
                              (str(Path.home()), "~"), (socket.gethostname(), "<host>")):
        if real:
            text = text.replace(real, placeholder)
    # Paths cut short by the detail limit, and the host's short name.
    text = re.sub(r"(/private)?/(tmp|var/folders)/[^\s\"\\]*", "$TMP/...", text)
    host = socket.gethostname()
    for h in {host, host.split(".")[0]}:
        if h:
            text = text.replace(h, "<host>")
    user = os.environ.get("USER", "")
    if user:
        text = re.sub(rf"\b{re.escape(user)}\b", "<user>", text)
    return text


def main() -> int:
    if WORK.exists():
        shutil.rmtree(WORK)
    (WORK / "proj").mkdir(parents=True)
    base = start_release_server()
    a = phase_a(base)
    if os.environ.get("AW_E2E_SKIP_B") != "1" and all(r["ok"] for r in results):
        phase_b(a)
    out = REPO / "docs" / "e2e-fresh-install-results.json"
    text = json.dumps({"version": VERSION, "claude": subprocess.run([CLAUDE, "--version"], capture_output=True, text=True).stdout.strip(),
                       "ran_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "results": results}, indent=2) + "\n"
    out.write_text(scrub(text))
    failed = [r for r in results if not r["ok"]]
    print(f"\n{len(results) - len(failed)}/{len(results)} passed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
