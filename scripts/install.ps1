<#
.SYNOPSIS
  php-debugger installer bootstrap (Windows).

.DESCRIPTION
  Downloads the latest php-debugger release for Windows and installs the binary.

  Usage:
    powershell -c "irm https://github.com/php-debugger/installer/releases/latest/download/install.ps1 | iex"

  Environment overrides:
    $env:INSTALL_DIR  where to place the binary (default: current directory)
#>

$ErrorActionPreference = "Stop"

$Repo = "php-debugger/installer"
$Bin  = "php-debugger.exe"
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { (Get-Location).Path }

# Only one Windows build is published (x64); Windows-on-ARM runs it via emulation.
$Asset = "php-debugger-windows-amd64.zip"

# The releases/latest/download URL always resolves to the newest release.
$Url = "https://github.com/$Repo/releases/latest/download/$Asset"
Write-Host "Installing $Bin (latest, windows/amd64)"

$tmp = Join-Path $env:TEMP ("php-debugger-" + [System.Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $zip = Join-Path $tmp $Asset
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $zip
    Expand-Archive -Path $zip -DestinationPath $tmp -Force

    $src = Join-Path $tmp $Bin
    if (-not (Test-Path $src)) { throw "archive did not contain $Bin" }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Move-Item -Force $src (Join-Path $InstallDir $Bin)
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host "Installed to $InstallDir\$Bin"
$onPath = ($env:PATH -split ';') -contains $InstallDir
if ($onPath) {
    Write-Host "Run:  php-debugger install"
} else {
    Write-Host "Add $InstallDir to your PATH, then run 'php-debugger install'."
}
