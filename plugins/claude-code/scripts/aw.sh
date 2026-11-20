#!/bin/sh
# AgentWatch plugin shim (Claude Code and Cursor plugins share this file).
#
# What it does, in order:
#   1. Picks the agentwatch binary for this plugin version from the plugin's
#      data directory (${CLAUDE_PLUGIN_DATA}, or ~/.agentwatch/plugin-data for
#      Cursor, which has no plugin data directory).
#   2. If it is missing, ANY invocation (MCP server, any hook, `aw.sh fetch`)
#      starts one download in the background, under a lock so concurrent
#      first-session starts share it, and waits for it as long as this caller
#      can: the MCP server up to 90 s, a session start up to 50 s, a
#      pre-tool hook 3 s. The file comes from this repository's GitHub Release
#      for the pinned version and must match the SHA-256 in checksums.txt,
#      which ships inside the plugin. A file that does not match is deleted,
#      never run.
#   3. Runs the binary with this script's arguments and stdin.
#
# A hook always exits 0: a hook that fails must not break the editor. When the
# binary is missing and the sensor is set to enforce + fail_closed, a gated
# call is denied instead (DESIGN.md, "Failure modes"). The MCP server exits 1
# with the reason on stderr, which Claude Code shows in /mcp.
#
# Network: one HTTPS GET to github.com (release asset) on first use, nothing
# else. All other network traffic is the binary's, described in PRIVACY.md.

set -u
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
self="$here/$(basename -- "$0")"
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
bindir="$data/bin"
bin="$bindir/$asset"
lock="$bindir/.download.lock"
errfile="$bindir/.download-error"
# Development override (tests, local builds): an explicit binary path.
[ -n "${AGENTWATCH_BIN:-}" ] && bin="$AGENTWATCH_BIN"
# Test override for the release download base. The SHA-256 pin still applies,
# so this can only ever fetch the exact pinned bytes. http only for loopback.
base="${AGENTWATCH_DOWNLOAD_BASE:-https://github.com/$repo/releases/download}"
proto='=https'
case "$base" in
  http://127.0.0.1:*|http://localhost:*) proto='=http,https' ;;
esac

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else echo ""; fi
}

# lock_alive: another process is downloading right now.
lock_alive() {
  [ -d "$lock" ] || return 1
  pid=$(cat "$lock/pid" 2>/dev/null)
  if [ -z "$pid" ]; then
    # Just created and the pid not written yet, unless it has been a while.
    [ -z "$(find "$lock" -maxdepth 0 -mmin +1 2>/dev/null)" ] && return 0
  elif kill -0 "$pid" 2>/dev/null && [ -z "$(find "$lock" -maxdepth 0 -mmin +3 2>/dev/null)" ]; then
    return 0
  fi
  # The owner is gone (killed with its hook) or stuck: the lock is stale.
  rm -rf "$lock" 2>/dev/null
  return 1
}

# download: run detached by start_download. Holds the lock while it works.
download() {
  mkdir -p "$bindir" 2>/dev/null || exit 1
  if ! mkdir "$lock" 2>/dev/null; then
    lock_alive && exit 0
    mkdir "$lock" 2>/dev/null || exit 0
  fi
  echo $$ > "$lock/pid"
  trap 'rm -rf "$lock"' EXIT
  trap 'exit 1' INT TERM HUP
  [ -x "$bindir/$asset" ] && exit 0
  want=$(grep " $asset\$" "$here/checksums.txt" 2>/dev/null | awk '{print $1}')
  if [ -z "$want" ]; then echo "no pinned checksum for $asset (unsupported platform?)" > "$errfile"; exit 1; fi
  tmp="$bindir/.$asset.$$"
  url="$base/v$version/$asset"
  if command -v curl >/dev/null 2>&1; then
    if ! curl -fsSL --proto "$proto" --retry 2 --connect-timeout 15 --max-time 150 -o "$tmp" "$url" 2>"$tmp.err"; then
      echo "download failed: $url: $(tail -c 300 "$tmp.err" 2>/dev/null)" > "$errfile"
      rm -f "$tmp" "$tmp.err"
      exit 1
    fi
  elif command -v wget >/dev/null 2>&1; then
    if ! wget -q -T 60 -O "$tmp" "$url"; then
      echo "download failed: $url" > "$errfile"
      rm -f "$tmp"
      exit 1
    fi
  else
    echo "neither curl nor wget is installed" > "$errfile"
    exit 1
  fi
  rm -f "$tmp.err"
  got=$(sha256_of "$tmp")
  if [ -z "$got" ] || [ "$got" != "$want" ]; then
    echo "checksum mismatch for $asset (got ${got:-none}, want $want): refused" > "$errfile"
    rm -f "$tmp"
    exit 1
  fi
  if [ "$os" != windows ]; then
    # A stable name for people and for `agentwatch link`: bin/agentwatch -> this
    # version. Made before the binary lands, so it is there when waiters wake.
    rm -f "$bindir/agentwatch" && ln -s "$asset" "$bindir/agentwatch" 2>/dev/null
  fi
  chmod 755 "$tmp" && mv -f "$tmp" "$bindir/$asset" || exit 1
  rm -f "$errfile"
  if [ "$os" != windows ]; then
    # Older versions' binaries are no longer used by this plugin.
    for old in "$bindir"/agentwatch-*-"$os"-"$arch"*; do
      [ "$old" = "$bindir/$asset" ] || [ ! -f "$old" ] || rm -f "$old"
    done
  fi
  exit 0
}

start_download() {
  mkdir -p "$bindir" 2>/dev/null || return 1
  lock_alive && return 0
  # Detached, so a hook that the client times out does not kill the download.
  if command -v nohup >/dev/null 2>&1; then
    nohup sh "$self" __download </dev/null >/dev/null 2>&1 &
  else
    sh "$self" __download </dev/null >/dev/null 2>&1 &
  fi
}

# wait_for SECONDS: until the binary appears or the download ends.
wait_for() {
  limit=$(( $1 * 4 ))
  n=0
  while [ ! -x "$bin" ] && [ "$n" -lt "$limit" ]; do
    sleep 0.25 2>/dev/null || sleep 1
    n=$((n + 1))
    # The downloader finished (or failed) and released the lock.
    [ "$n" -gt 4 ] && [ ! -d "$lock" ] && [ ! -x "$bin" ] && break
  done
}

first="${1:-}"
event="${3:-}"
[ "$first" = "__download" ] && download

if [ ! -x "$bin" ] && [ -z "${AGENTWATCH_BIN:-}" ]; then
  case "$first:$event" in
    fetch:*|mcp:*) budget=90 ;;
    *:SessionStart|*:sessionStart) budget=50 ;;
    *:PreToolUse|*:preToolUse|*:beforeShellExecution|*:beforeMCPExecution|*:beforeReadFile) budget=3 ;;
    *) budget=8 ;;
  esac
  start_download
  wait_for "$budget"
fi
if [ "$first" = "fetch" ]; then
  [ -x "$bin" ] && exit 0
  echo "AgentWatch: $(cat "$errfile" 2>/dev/null || echo 'download did not finish')" >&2
  exit 1
fi

if [ -x "$bin" ]; then
  exec "$bin" "$@"
fi

if [ "$first" = "mcp" ]; then
  why=$(cat "$errfile" 2>/dev/null || echo "the download did not finish in time")
  echo "AgentWatch: the sensor binary is not available yet ($why). Check that github.com is reachable, then reconnect: /mcp -> agentwatch -> Reconnect." >&2
  exit 1
fi

# No binary. Read the payload (the client is waiting on our stdin), then answer.
cat >/dev/null 2>&1 || true
mode="${CLAUDE_PLUGIN_OPTION_MODE:-${AGENTWATCH_MODE:-}}"
fail=""
cfg="${AGENTWATCH_HOME:-${HOME:-.}/.agentwatch}/config.json"
[ -f "$cfg" ] && grep -q '"fail_mode": *"fail_closed"' "$cfg" && fail=fail_closed
[ -z "$mode" ] && [ -f "$cfg" ] && grep -q '"mode": *"enforce"' "$cfg" && mode=enforce
block=no
[ "$first" = "hook" ] && [ "$mode" = "enforce" ] && [ "$fail" = "fail_closed" ] && block=yes
msg="AgentWatch sensor is not installed yet (fail-closed): its download has not finished. Retry in a moment, or run /agentwatch:doctor."
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
