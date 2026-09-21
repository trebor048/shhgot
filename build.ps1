<#
.SYNOPSIS
    Build shhgit for Windows - and, with -All, for Linux and macOS too.

.DESCRIPTION
    Native Windows counterpart to `make build` and `make build-all`. shhgit is a
    single static Go binary and the web dashboard is embedded, so there is no
    JavaScript toolchain and no second build step.

    Requires Go 1.26 or newer on PATH (https://go.dev/dl/, or run .\install.ps1
    which installs it for you).

.PARAMETER All
    Cross-compile all six OS/arch targets into dist\ instead of building only
    for this machine.

.PARAMETER Output
    Path for the host binary. Defaults to shhgit.exe next to this script.

.PARAMETER NoTrim
    Keep symbols and debug info (drop -trimpath and -ldflags "-s -w").

.EXAMPLE
    .\build.ps1
    .\build.ps1 -All
    .\build.ps1 -Output C:\tools\shhgit.exe
#>
[CmdletBinding()]
param(
    [switch]$All,
    [string]$Output,
    [switch]$NoTrim
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Root   = $PSScriptRoot
$Binary = 'shhgit'
$Main   = './cmd/shhgit'

function Write-Ok   { param([string]$Message) Write-Host "[ok] $Message" -ForegroundColor Green }
function Write-Info { param([string]$Message) Write-Host "[..] $Message" -ForegroundColor Cyan }
function Write-Fail { param([string]$Message) Write-Host "[x] $Message"  -ForegroundColor Red; exit 1 }

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Fail "Go is not on PATH. Install Go 1.26+ ('winget install GoLang.Go') and reopen this shell."
}

function Invoke-GoBuild {
    param([string]$Out)
    $goArgs = @('build', '-trimpath')
    if (-not $NoTrim) { $goArgs += @('-ldflags', '-s -w') }
    $goArgs += @('-o', $Out, $Main)
    & go @goArgs
    if ($LASTEXITCODE -ne 0) { Write-Fail "build failed: $Out" }
}

Push-Location $Root
try {
    # Static binary: no cgo, so no C toolchain is needed and the same source
    # cross-compiles cleanly to every target below.
    $env:CGO_ENABLED = '0'

    if ($All) {
        $dist = Join-Path $Root 'dist'
        New-Item -ItemType Directory -Force -Path $dist | Out-Null

        $targets = @(
            [pscustomobject]@{ OS = 'linux';   Arch = 'amd64' },
            [pscustomobject]@{ OS = 'linux';   Arch = 'arm64' },
            [pscustomobject]@{ OS = 'darwin';  Arch = 'amd64' },
            [pscustomobject]@{ OS = 'darwin';  Arch = 'arm64' },
            [pscustomobject]@{ OS = 'windows'; Arch = 'amd64' },
            [pscustomobject]@{ OS = 'windows'; Arch = 'arm64' }
        )

        foreach ($t in $targets) {
            $ext  = if ($t.OS -eq 'windows') { '.exe' } else { '' }
            $name = "$Binary-$($t.OS)-$($t.Arch)$ext"
            Write-Info "building $name"
            $env:GOOS   = $t.OS
            $env:GOARCH = $t.Arch
            Invoke-GoBuild (Join-Path $dist $name)
        }

        Remove-Item Env:\GOOS, Env:\GOARCH -ErrorAction SilentlyContinue
        Write-Ok "cross-compiled 6 targets into $dist"
        Get-ChildItem $dist |
            Format-Table Name, @{ Name = 'MB'; Expression = { [math]::Round($_.Length / 1MB, 1) } } -AutoSize
    }
    else {
        $out    = if ($Output) { $Output } else { Join-Path $Root "$Binary.exe" }
        $parent = Split-Path -Parent $out
        if ($parent -and -not (Test-Path $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }

        Write-Info "building $out"
        Invoke-GoBuild $out

        $size = [math]::Round((Get-Item $out).Length / 1MB, 1)
        Write-Ok "built $out ($size MB)"
    }
}
finally {
    Pop-Location
}
