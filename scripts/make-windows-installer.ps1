#Requires -Version 5.1
<#
.SYNOPSIS
    Build the NetStack Doctor Windows installer.

.DESCRIPTION
    Compiles the standalone GUI binary and wraps it in an Inno Setup installer
    that adds a Start Menu entry. Produces
    dist\NetStack-Doctor-<version>-windows-amd64-setup.exe.

    This is the Windows counterpart to build.sh, and like build.sh it must run
    on the target platform: the GUI uses a native WebView2 webview via cgo,
    which cannot be cross-compiled.

    Requires Go with a C toolchain and Inno Setup 6.3+.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\make-windows-installer.ps1
#>
param(
    # Defaults to the VERSION declared in build.sh so the two stay in step.
    [string]$Version
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root

try {
    if (-not $Version) {
        $m = Select-String -Path "build.sh" -Pattern '^VERSION="([^"]+)"' | Select-Object -First 1
        if (-not $m) { throw "Could not read VERSION from build.sh; pass -Version explicitly." }
        $Version = $m.Matches[0].Groups[1].Value
    }
    Write-Host "Version: $Version"

    New-Item -ItemType Directory -Force -Path "dist" | Out-Null

    Write-Host "Building GUI binary (CGO, amd64)..."
    $env:CGO_ENABLED = "1"
    $env:GOARCH = "amd64"
    go build -ldflags "-s -w -H windowsgui" -o "NetStack Doctor.exe" .
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }

    # ISCC is not on PATH after a default Inno Setup install.
    $iscc = (Get-Command "iscc.exe" -ErrorAction SilentlyContinue).Source
    if (-not $iscc) {
        $candidates = @(
            "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
            "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
        )
        $iscc = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
    }
    if (-not $iscc) {
        throw "Inno Setup 6.3+ not found. Install it (winget install JRSoftware.InnoSetup) and retry."
    }

    Write-Host "Compiling installer with $iscc ..."
    & $iscc "/DAppVersion=$Version" "installer\netstack-doctor.iss"
    if ($LASTEXITCODE -ne 0) { throw "iscc failed" }

    Get-ChildItem "dist\*setup.exe" | ForEach-Object {
        Write-Host ("Built: {0}" -f $_.FullName)
        Write-Host ("SHA256: {0}" -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash)
    }
}
finally {
    Pop-Location
}
