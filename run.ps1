<#
.SYNOPSIS
    Run shhgit on Windows.

.DESCRIPTION
    Windows counterpart to run.sh. Web mode logs to a file (run.log by default);
    the tui, terminal and scanner modes run in the foreground so you can see the
    live feed.

.PARAMETER Mode
    web (default), tui, terminal or scanner.

.PARAMETER WebHost
    Interface for web mode. Default 127.0.0.1 (loopback only).

.PARAMETER WebPort
    Port for web mode. Default 8080.

.PARAMETER Threads
    Scan concurrency. 0 (default) means one thread per logical CPU.

.PARAMETER Log
    Log file for web mode. Default run.log.

.EXAMPLE
    .\run.ps1
    .\run.ps1 -WebPort 9000
    .\run.ps1 -Mode tui
    .\run.ps1 -WebHost 0.0.0.0
#>
[CmdletBinding()]
param(
    [ValidateSet('web', 'tui', 'terminal', 'scanner')]
    [string]$Mode = 'web',
    [string]$WebHost = '127.0.0.1',
    [int]$WebPort = 8080,
    [int]$Threads = 0,
    [string]$Log = 'run.log'
)

$ErrorActionPreference = 'Stop'

$Root = $PSScriptRoot
$exe  = Join-Path $Root 'shhgit.exe'

if (-not (Test-Path $exe)) {
    Write-Host '[x] shhgit.exe not found. Run .\install.ps1 or .\build.ps1 first.' -ForegroundColor Red
    exit 1
}

$shhgitArgs = @("--$Mode", '--config-path', $Root)
if ($Threads -gt 0) { $shhgitArgs += @('--threads', "$Threads") }

if ($Mode -eq 'web') {
    $shhgitArgs += @('--web-host', $WebHost, '--web-port', "$WebPort")
    Write-Host "starting shhgit dashboard on http://${WebHost}:${WebPort} (logs: $Log)"
    & $exe @shhgitArgs *>> (Join-Path $Root $Log)
}
else {
    Write-Host "starting shhgit ($Mode)"
    & $exe @shhgitArgs
}

exit $LASTEXITCODE
