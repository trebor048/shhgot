<#
.SYNOPSIS
    One-shot shhgit installer for Windows.

.DESCRIPTION
    Windows counterpart to install.sh. Checks for Go (installing it with winget
    or Chocolatey when available), fetches the source, builds shhgit.exe, creates
    config.yaml from the example, and smoke-tests the result.

.PARAMETER InstallDir
    Where to clone the repository when you are not already inside a checkout.
    Defaults to $HOME\shhgit.

.PARAMETER NoBuild
    Set up the source and config only; do not compile.

.PARAMETER Docker
    Print the Docker route instead of installing natively.

.EXAMPLE
    .\install.ps1
    .\install.ps1 -InstallDir C:\tools\shhgit
    .\install.ps1 -NoBuild
    .\install.ps1 -Docker
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $HOME 'shhgit'),
    [switch]$NoBuild,
    [switch]$Docker
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoUrl           = 'https://github.com/trebor048/shhgot.git'
$RepoSlug          = 'trebor048/shhgot'
$Binary            = 'shhgit.exe'
$FallbackGoVersion = '1.26.0'   # keep in sync with the `go` directive in go.mod

$script:RepoDir = $null

function Write-Ok   { param([string]$Message) Write-Host "[ok] $Message" -ForegroundColor Green }
function Write-Info { param([string]$Message) Write-Host "[..] $Message" -ForegroundColor Cyan }
function Write-Warn { param([string]$Message) Write-Host "[!] $Message"  -ForegroundColor Yellow }
function Write-Fail { param([string]$Message) Write-Host "[x] $Message"  -ForegroundColor Red; exit 1 }
function Write-Step { param([string]$Message) Write-Host "`n$Message" -ForegroundColor White }

function Get-RequiredGoVersion {
    if (Test-Path 'go.mod') {
        $m = Select-String -Path 'go.mod' -Pattern '^go\s+(\S+)' | Select-Object -First 1
        if ($m) { return $m.Matches[0].Groups[1].Value }
    }
    return $FallbackGoVersion
}

function Test-VersionGe {
    param([string]$Have, [string]$Want)
    try { return ([version]$Have) -ge ([version]$Want) } catch { return $false }
}

function Ensure-Go {
    param([string]$Want)
    Write-Step '1/4  Go toolchain'

    $go = Get-Command go -ErrorAction SilentlyContinue
    if ($go) {
        $have = (& go version) -replace '^go version go', '' -replace '\s.*$', ''
        if (Test-VersionGe -Have $have -Want $Want) {
            Write-Ok "Go $have found (project needs >= $Want)"
            return
        }
        Write-Warn "Go $have is too old - this project needs >= $Want."
    }
    else {
        Write-Info "Go is not installed (project needs >= $Want)."
    }

    if (Get-Command winget -ErrorAction SilentlyContinue) {
        Write-Info 'Installing Go with winget...'
        & winget install --id GoLang.Go -e --accept-source-agreements --accept-package-agreements | Out-Host
    }
    elseif (Get-Command choco -ErrorAction SilentlyContinue) {
        Write-Info 'Installing Go with Chocolatey...'
        & choco install golang -y | Out-Host
    }
    else {
        Write-Fail "Install Go >= $Want from https://go.dev/dl/ and re-run this script."
    }

    # A fresh install updates the machine/user PATH, but not this process. Reload it.
    $machine  = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $env:Path = "$machine;$userPath"

    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Fail "Go is still not on PATH. If the install just finished, open a new PowerShell window and re-run; otherwise install Go >= $Want from https://go.dev/dl/."
    }
    Write-Ok "Go installed: $(& go version)"
}

function Get-Source {
    param([string]$Dir)
    Write-Step '2/4  Source code'

    # Already inside a shhgit checkout? Check the module path, not the folder name.
    if ((Test-Path 'go.mod') -and (Select-String -Path 'go.mod' -Pattern 'shhgot|shhgit' -Quiet)) {
        $script:RepoDir = (Get-Location).Path
        Write-Ok "using existing checkout: $script:RepoDir"
        return
    }

    if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
        Write-Fail 'git is required. Install it (winget install Git.Git) and re-run.'
    }

    if (Test-Path (Join-Path $Dir '.git')) {
        Write-Info "Updating existing checkout at $Dir ..."
        & git -C $Dir pull --ff-only | Out-Host
        if ($LASTEXITCODE -ne 0) { Write-Warn 'could not update; using what is on disk' }
    }
    else {
        Write-Info "Cloning $RepoSlug into $Dir ..."
        $parent = Split-Path -Parent $Dir
        if ($parent -and -not (Test-Path $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
        & git clone --depth 1 $RepoUrl $Dir | Out-Host
        if ($LASTEXITCODE -ne 0) { Write-Fail 'git clone failed' }
    }

    $script:RepoDir = (Resolve-Path $Dir).Path
    Write-Ok "checkout ready: $script:RepoDir"
}

function Build-Binary {
    Write-Step '3/4  Build'
    if ($NoBuild) { Write-Warn 'skipped (-NoBuild)'; return }

    Push-Location $script:RepoDir
    try {
        $env:CGO_ENABLED = '0'
        Write-Info 'Downloading Go modules...'
        & go mod download | Out-Host
        if ($LASTEXITCODE -ne 0) { Write-Fail 'go mod download failed (no network?)' }

        Write-Info 'Compiling...'
        & go build -trimpath -ldflags '-s -w' -o $Binary ./cmd/shhgit | Out-Host
        if ($LASTEXITCODE -ne 0) { Write-Fail 'build failed' }
    }
    finally {
        Pop-Location
    }

    $exe  = Join-Path $script:RepoDir $Binary
    $size = [math]::Round((Get-Item $exe).Length / 1MB, 1)
    Write-Ok "built $exe ($size MB)"
}

function Set-Config {
    Write-Step '4/4  Configuration'
    $cfg     = Join-Path $script:RepoDir 'config.yaml'
    $example = Join-Path $script:RepoDir 'config.yaml.example'

    if (Test-Path $cfg) {
        Write-Ok 'config.yaml already exists - leaving it alone'
    }
    elseif (Test-Path $example) {
        Copy-Item $example $cfg
        # chmod 600 equivalent: drop inherited permissions, keep only this user.
        & icacls $cfg /inheritance:r /grant:r "$($env:USERNAME):(R,W)" | Out-Null
        Write-Ok 'created config.yaml from config.yaml.example (read/write for you only)'
        Write-Warn 'add your GitHub token(s) to github_access_tokens before scanning GitHub'
    }
    else {
        Write-Warn 'no config.yaml.example found - create config.yaml yourself'
    }
}

function Invoke-SmokeTest {
    if ($NoBuild) { return }
    $exe = Join-Path $script:RepoDir $Binary
    if (-not (Test-Path $exe)) { Write-Warn 'binary not found; skipping smoke test'; return }

    $out = (& $exe -h 2>&1 | Out-String)
    if ($out -match '(?i)usage') {
        Write-Ok 'smoke test passed - the binary runs and lists its flags'
    }
    else {
        Write-Warn 'could not verify the binary (it may still work)'
    }
}

function Show-Summary {
    Write-Host "`nSetup complete" -ForegroundColor Green
    Write-Host ''
    Write-Host "  Location:  $script:RepoDir"
    Write-Host "  Binary:    $(Join-Path $script:RepoDir $Binary)"
    Write-Host "  Config:    $(Join-Path $script:RepoDir 'config.yaml')"
    Write-Host ''
    Write-Host 'Next steps' -ForegroundColor White
    Write-Host '  1. Edit config.yaml and add your GitHub token(s) to github_access_tokens'
    Write-Host '     (skip this if you only ever scan local code with --local)'
    Write-Host ''
    Write-Host '  2. Run it:'
    Write-Host "       .\$Binary                 # terminal live feed (default)"
    Write-Host "       .\$Binary --web           # web dashboard on http://127.0.0.1:8080"
    Write-Host "       .\$Binary --local .\code  # scan a local directory, no tokens needed"
    Write-Host ''
    Write-Host "  Docs: https://github.com/$RepoSlug#readme"
    Write-Host ''
}

function Show-DockerNotice {
    Write-Host ''
    Write-Host 'Docker route' -ForegroundColor White
    Write-Host ''
    Write-Host '  shhgit also runs from Docker. From a checkout of the repo:'
    Write-Host ''
    Write-Host '      Copy-Item .env.example .env'
    Write-Host '      Copy-Item config.yaml.example config.yaml'
    Write-Host '      docker compose up -d'
    Write-Host '      # dashboard on http://localhost:8080'
    Write-Host ''
    Write-Host '  config.yaml must exist before `docker compose up`, or Docker creates a directory with that name.'
    Write-Host ''
}

# ── main ───────────────────────────────────────────────────────────────────
Write-Host "`nshhgit installer - $RepoSlug" -ForegroundColor White

if ($Docker) {
    Show-DockerNotice
    exit 0
}

$arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
Write-Ok "platform: windows/$arch"

Ensure-Go -Want (Get-RequiredGoVersion)
Get-Source -Dir $InstallDir
Build-Binary
Set-Config
Invoke-SmokeTest
Show-Summary
