#!/bin/sh
# AgentWatch plugin shim (Claude Code and Cursor plugins share this file).
#
# What it does, in order:
#   1. Picks the agentwatch binary for this plugin version from the plugin's
#      data directory (${CLAUDE_PLUGIN_DATA}, or ~/.agentwatch/plugin-data for
#      Cursor, which has no plugin data directory).
#   2. If it is missing and this is a session start (or `aw.sh fetch`), it
#      downloads it from this repository's GitHub Release for the pinned
#      version and checks it against the SHA-256 in checksums.txt, which ships
#      inside the plugin. A file that does not match is deleted, never run.
#   3. Runs the binary with this script's arguments and stdin.
#
# It always exits 0: a hook that fails must not break the editor. When the
# binary is missing and the sensor is set to enforce + fail_closed, a gated
# call is denied instead (DESIGN.md, "Failure modes").
#
# Network: one HTTPS GET to github.com (release asset) on first use, nothing
# else. All other network traffic is the binary's, described in PRIVACY.md.

set -u
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
version=$(cat "$here/VERSION" 2>/dev/null | tr -d ' \r\n')
repo="chinmayNayar/agentwatch-plugins"

case "$(uname -s 2>/dev/null)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW*|MSYS*|CYGWIN*) os=windows ;;
  *) os=unknown ;;
esac
case "$(uname -m 2>/dev/null)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64) arch=x64 ;;
  *) arch=unknown ;;
esac
ext=""
[ "$os" = windows ] && ext=".exe"
asset="agentwatch-${version}-${os}-${arch}${ext}"

data="${CLAUDE_PLUGIN_DATA:-}"
[ -n "$data" ] || data="${HOME:-.}/.agentwatch/plugin-data"
bin="$data/bin/$asset"
# Development override (tests, local builds): an explicit binary path.
[ -n "${AGENTWATCH_BIN:-}" ] && bin="$AGENTWATCH_BIN"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else echo ""; fi
}

fetch() {
  want=$(grep " $asset\$" "$here/checksums.txt" 2>/dev/null | awk '{print $1}')
  [ -n "$want" ] || return 1
  mkdir -p "$data/bin" || return 1
  tmp="$data/bin/.$asset.$$"
  url="https://github.com/$repo/releases/download/v$version/$asset"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --proto '=https' --max-time 60 -o "$tmp" "$url" || { rm -f "$tmp"; return 1; }
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 60 -O "$tmp" "$url" || { rm -f "$tmp"; return 1; }
  else
    return 1
  fi
  got=$(sha256_of "$tmp")
  if [ -z "$got" ] || [ "$got" != "$want" ]; then
    rm -f "$tmp"
    return 1
  fi
  chmod 755 "$tmp" && mv -f "$tmp" "$data/bin/$asset"
}

# Session start (or an explicit `aw.sh fetch`) may download; nothing else does.
first="${1:-}"
event="${3:-}"
if [ ! -x "$bin" ] && [ -z "${AGENTWATCH_BIN:-}" ]; then
  if [ "$first" = "fetch" ] || [ "$event" = "SessionStart" ] || [ "$event" = "sessionStart" ]; then
    fetch || true
  fi
fi
[ "$first" = "fetch" ] && { [ -x "$bin" ] && exit 0 || exit 1; }

if [ -x "$bin" ]; then
  exec "$bin" "$@"
fi

# No binary. Read the payload (the client is waiting on our stdin), then answer.
cat >/dev/null 2>&1 || true
mode="${CLAUDE_PLUGIN_OPTION_MODE:-${AGENTWATCH_MODE:-}}"
fail=""
cfg="${HOME:-.}/.agentwatch/config.json"
[ -f "$cfg" ] && grep -q '"fail_mode": *"fail_closed"' "$cfg" && fail=fail_closed
[ -z "$mode" ] && [ -f "$cfg" ] && grep -q '"mode": *"enforce"' "$cfg" && mode=enforce
block=no
[ "$first" = "hook" ] && [ "$mode" = "enforce" ] && [ "$fail" = "fail_closed" ] && block=yes
msg="AgentWatch sensor is not installed yet (fail-closed): restart the session to download it, or run agentwatch doctor."
case "${2:-}:$event" in
  claude-code:PreToolUse)
    [ "$block" = yes ] && printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"%s"}}\n' "$msg" ;;
  cursor:preToolUse|cursor:beforeShellExecution|cursor:beforeMCPExecution|cursor:beforeReadFile|cursor:subagentStart)
    if [ "$block" = yes ]; then
      printf '{"continue":true,"permission":"deny","user_message":"%s","agent_message":"%s"}\n' "$msg" "$msg"
    else
      printf '{"continue":true,"permission":"allow"}\n'
    fi ;;
  cursor:sessionStart|cursor:beforeSubmitPrompt)
    printf '{"continue":true,"permission":"allow"}\n' ;;
  cursor:*)
    printf '{}\n' ;;
esac
exit 0
