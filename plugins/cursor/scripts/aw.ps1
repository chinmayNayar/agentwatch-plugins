# AgentWatch plugin shim for PowerShell (Windows without Git Bash).
# Same contract as aw.sh: run the pinned agentwatch binary from the plugin's
# data directory with this script's arguments and stdin. If it is missing, any
# invocation starts one download (under a lock, detached so a timed-out hook
# does not kill it), verifies its SHA-256 against checksums.txt and waits for
# it as long as the caller can. A hook always exits 0; the MCP server exits 1
# with the reason on stderr.
$ErrorActionPreference = 'Continue'
[Console]::InputEncoding  = [System.Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding           = [System.Text.UTF8Encoding]::new($false)

$here    = $PSScriptRoot
$version = (Get-Content -LiteralPath (Join-Path $here 'VERSION') -ErrorAction SilentlyContinue | Select-Object -First 1)
if ($version) { $version = $version.Trim() }
$arch    = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x64' }
$asset   = "agentwatch-$version-windows-$arch.exe"
$data    = if ($env:CLAUDE_PLUGIN_DATA) { $env:CLAUDE_PLUGIN_DATA } else { Join-Path $HOME '.agentwatch\plugin-data' }
$bindir  = Join-Path $data 'bin'
$bin     = Join-Path $bindir $asset
$lock    = Join-Path $bindir '.download.lock'
$errfile = Join-Path $bindir '.download-error'
if ($env:AGENTWATCH_BIN) { $bin = $env:AGENTWATCH_BIN }
$base = 'https://github.com/chinmayNayar/agentwatch-plugins/releases/download'
if ($env:AGENTWATCH_DOWNLOAD_BASE -and ($env:AGENTWATCH_DOWNLOAD_BASE -match '^(https://|http://(127\.0\.0\.1|localhost):)')) {
  $base = $env:AGENTWATCH_DOWNLOAD_BASE
}

function Test-LockAlive {
  if (-not (Test-Path -LiteralPath $lock)) { return $false }
  $age = (Get-Date) - (Get-Item -LiteralPath $lock).CreationTime
  $ownerPid = Get-Content -LiteralPath (Join-Path $lock 'pid') -ErrorAction SilentlyContinue | Select-Object -First 1
  if (-not $ownerPid) { if ($age.TotalSeconds -lt 60) { return $true } }
  elseif ((Get-Process -Id ([int]$ownerPid) -ErrorAction SilentlyContinue) -and $age.TotalMinutes -lt 3) { return $true }
  Remove-Item -Recurse -Force -LiteralPath $lock -ErrorAction SilentlyContinue
  return $false
}

function Invoke-Download {
  New-Item -ItemType Directory -Force -Path $bindir | Out-Null
  try { New-Item -ItemType Directory -Path $lock -ErrorAction Stop | Out-Null }
  catch {
    if (Test-LockAlive) { return }
    try { New-Item -ItemType Directory -Path $lock -ErrorAction Stop | Out-Null } catch { return }
  }
  try {
    Set-Content -LiteralPath (Join-Path $lock 'pid') -Value $PID
    if (Test-Path -LiteralPath (Join-Path $bindir $asset)) { return }
    $line = Get-Content -LiteralPath (Join-Path $here 'checksums.txt') -ErrorAction SilentlyContinue |
      Where-Object { $_ -match (" " + [regex]::Escape($asset) + '$') } | Select-Object -First 1
    if (-not $line) { Set-Content -LiteralPath $errfile -Value "no pinned checksum for $asset"; return }
    $want = ($line -split '\s+')[0].ToLower()
    $tmp = Join-Path $bindir (".$asset." + $PID)
    $url = "$base/v$version/$asset"
    try {
      Invoke-WebRequest -UseBasicParsing -TimeoutSec 150 -OutFile $tmp -Uri $url
    } catch {
      Remove-Item -Force -ErrorAction SilentlyContinue $tmp
      Set-Content -LiteralPath $errfile -Value "download failed: $url : $($_.Exception.Message)"
      return
    }
    $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $tmp).Hash.ToLower()
    if ($got -ne $want) {
      Remove-Item -Force -ErrorAction SilentlyContinue $tmp
      Set-Content -LiteralPath $errfile -Value "checksum mismatch for $asset (got $got, want $want): refused"
      return
    }
    Move-Item -Force -LiteralPath $tmp -Destination (Join-Path $bindir $asset)
    Remove-Item -Force -ErrorAction SilentlyContinue $errfile
  } finally {
    Remove-Item -Recurse -Force -LiteralPath $lock -ErrorAction SilentlyContinue
  }
}

$first = if ($args.Count -gt 0) { $args[0] } else { '' }
$event = if ($args.Count -gt 2) { $args[2] } else { '' }
if ($first -eq '__download') { Invoke-Download; exit 0 }

if (-not (Test-Path -LiteralPath $bin) -and -not $env:AGENTWATCH_BIN) {
  $budget = 8
  if ($first -eq 'fetch' -or $first -eq 'mcp') { $budget = 90 }
  elseif ($event -eq 'SessionStart' -or $event -eq 'sessionStart') { $budget = 50 }
  elseif (@('PreToolUse','preToolUse','beforeShellExecution','beforeMCPExecution','beforeReadFile') -contains $event) { $budget = 3 }
  New-Item -ItemType Directory -Force -Path $bindir | Out-Null
  if (-not (Test-LockAlive)) {
    $ps = (Get-Process -Id $PID).Path
    Start-Process -FilePath $ps -WindowStyle Hidden -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath, '__download') | Out-Null
  }
  $deadline = (Get-Date).AddSeconds($budget)
  $started = Get-Date
  while (-not (Test-Path -LiteralPath $bin) -and (Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 250
    if (((Get-Date) - $started).TotalSeconds -gt 2 -and -not (Test-Path -LiteralPath $lock)) { break }
  }
}
if ($first -eq 'fetch') {
  if (Test-Path -LiteralPath $bin) { exit 0 }
  [Console]::Error.WriteLine("AgentWatch: " + (Get-Content -LiteralPath $errfile -ErrorAction SilentlyContinue | Select-Object -First 1))
  exit 1
}

if ($first -eq 'mcp') {
  if (Test-Path -LiteralPath $bin) {
    # The MCP server streams: hand it our stdin as is.
    & $bin @args
    exit $LASTEXITCODE
  }
  $why = Get-Content -LiteralPath $errfile -ErrorAction SilentlyContinue | Select-Object -First 1
  if (-not $why) { $why = 'the download did not finish in time' }
  [Console]::Error.WriteLine("AgentWatch: the sensor binary is not available yet ($why). Check that github.com is reachable, then reconnect: /mcp -> agentwatch -> Reconnect.")
  exit 1
}

$payload = [Console]::In.ReadToEnd()
if (Test-Path -LiteralPath $bin) {
  $payload | & $bin @args
  exit 0
}
# No binary: answer so the client is not left with invalid output.
$client = if ($args.Count -gt 1) { $args[1] } else { '' }
if ($client -eq 'cursor') {
  if (@('preToolUse','beforeShellExecution','beforeMCPExecution','beforeReadFile','subagentStart','sessionStart','beforeSubmitPrompt') -contains $event) {
    '{"continue":true,"permission":"allow"}'
  } else { '{}' }
}
exit 0
