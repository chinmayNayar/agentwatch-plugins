# AgentWatch plugin shim for PowerShell (Windows without Git Bash).
# Same contract as aw.sh: run the pinned agentwatch binary from the plugin's
# data directory with this script's arguments and stdin; download it on a
# session start and verify its SHA-256 against checksums.txt; always exit 0.
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
$bin     = Join-Path (Join-Path $data 'bin') $asset
if ($env:AGENTWATCH_BIN) { $bin = $env:AGENTWATCH_BIN }

function Get-Binary {
  $line = Get-Content -LiteralPath (Join-Path $here 'checksums.txt') -ErrorAction SilentlyContinue |
    Where-Object { $_ -match (" " + [regex]::Escape($asset) + '$') } | Select-Object -First 1
  if (-not $line) { return $false }
  $want = ($line -split '\s+')[0].ToLower()
  New-Item -ItemType Directory -Force -Path (Join-Path $data 'bin') | Out-Null
  $tmp = Join-Path (Join-Path $data 'bin') (".$asset." + $PID)
  try {
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 60 -OutFile $tmp `
      -Uri "https://github.com/chinmayNayar/agentwatch-plugins/releases/download/v$version/$asset"
  } catch { Remove-Item -Force -ErrorAction SilentlyContinue $tmp; return $false }
  $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $tmp).Hash.ToLower()
  if ($got -ne $want) { Remove-Item -Force -ErrorAction SilentlyContinue $tmp; return $false }
  Move-Item -Force -LiteralPath $tmp -Destination (Join-Path (Join-Path $data 'bin') $asset)
  return $true
}

$first = if ($args.Count -gt 0) { $args[0] } else { '' }
$event = if ($args.Count -gt 2) { $args[2] } else { '' }
if (-not (Test-Path -LiteralPath $bin) -and -not $env:AGENTWATCH_BIN) {
  if ($first -eq 'fetch' -or $event -eq 'SessionStart' -or $event -eq 'sessionStart') { [void](Get-Binary) }
}
if ($first -eq 'fetch') { if (Test-Path -LiteralPath $bin) { exit 0 } else { exit 1 } }

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
