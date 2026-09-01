#!/usr/bin/env pwsh
# Windows real-mount smoke: starts `qrypt mount` with a localfs backend and
# encryption, writes a file through the FUSE-mounted path, then verifies the
# content round-trips decrypt-correctly via `qrypt fs cat` and that the
# backend only stores the encrypted (obfuscated) file name.
#
# Requires WinFsp installed (this script does not install it).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/smoke-windows-mount.ps1
#
# Exit code 0 = pass, 1 = any assertion failed. On failure the mount
# process's stdout/stderr are dumped before cleanup.
[CmdletBinding()]
param(
    [string]$Binary = ""
)

$ErrorActionPreference = "Stop"

if (-not $Binary) {
    $Binary = Join-Path (Split-Path -Parent $PSScriptRoot) "qrypt.exe"
}
if (-not (Test-Path $Binary)) {
    Write-Error "binary not found: $Binary (build first: CGO_ENABLED=0 go build -tags nocgo -o qrypt.exe ./cmd/qrypt)"
    exit 1
}

$work = Join-Path $env:TEMP ("qrypt-smoke-windows-" + [guid]::NewGuid().ToString("N"))
# GitHub-hosted Windows runners point TEMP at the 8.3 short form
# (C:\Users\RUNNER~1\...); WinFsp refuses to mount at paths whose components
# are short names ("mount point in use"), so canonicalize the work tree to
# its long path before deriving any child paths.
$work = [System.IO.Directory]::CreateDirectory($work).FullName
$work = (New-Object -ComObject Scripting.FileSystemObject).GetFolder($work).Path
$remote  = Join-Path $work "remote"
$cache   = Join-Path $work "cache"
$upload  = Join-Path $work "upload"
$state   = Join-Path $work "state"
$mountPt = Join-Path $work "mount"
$config  = Join-Path $work "qrypt.toml"
$stdout  = Join-Path $work "mount.stdout.log"
$stderr  = Join-Path $work "mount.stderr.log"
$fuseFile = Join-Path $mountPt "local\hello.txt"
$content  = "qrypt windows mount smoke " + (Get-Date -Format o)

# The mount point directory must NOT exist: WinFsp preflight rejects an
# existing directory as "mount point in use" and creates it at mount time.
New-Item -ItemType Directory -Force -Path $remote, $cache, $upload, $state | Out-Null

# Forward-slash paths keep the TOML free of backslash escapes; Go and the
# Windows APIs accept them.
$mountPtF = $mountPt.Replace('\', '/')
$remoteF  = $remote.Replace('\', '/')
$cacheF   = $cache.Replace('\', '/')
$uploadF  = $upload.Replace('\', '/')
$stateF   = $state.Replace('\', '/')

$toml = @"
mount_point = "$mountPtF"

[storage]
read_cache_dir = "$cacheF"
upload_dir = "$uploadF"
state_dir = "$stateF"

[time]
ntp_enabled = false

[upload]
upload_delay = "1ms"
delete_delay = "1ms"
upload_workers = 2

[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root_path = "$remoteF"

[mounts.encryption]
password = "windows-smoke"
filename_encryption = "standard"
filename_encoding = "base32"
"@
[System.IO.File]::WriteAllText($config, $toml) # UTF-8 without BOM

function Assert-True {
    param([bool]$Cond, [string]$Msg)
    if (-not $Cond) { throw "assertion failed: $Msg" }
}

$proc = $null
try {
    # Readiness: the FUSE mount exposes the `local` mount name as a child of
    # the mount point; only the nocgo host + WinFsp can create it. WinFsp can
    # transiently report "mount point in use" on a fresh runner (Defender
    # scanning the new directory, leftover mount state), so restart once.
    $ready = $false
    for ($attempt = 0; $attempt -lt 2 -and -not $ready; $attempt++) {
        if ($attempt -gt 0) {
            Write-Host "mount attempt $attempt failed; restarting once"
            if ($proc -and -not $proc.HasExited) {
                Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
                Start-Sleep -Seconds 2
            }
        }
        $proc = Start-Process -FilePath $Binary `
            -ArgumentList @("mount", "--config", $config) `
            -WorkingDirectory $work `
            -RedirectStandardOutput $stdout `
            -RedirectStandardError $stderr `
            -PassThru
        for ($i = 0; $i -lt 60; $i++) {
            if ($proc.HasExited) { break }
            Start-Sleep -Seconds 1
            if (Test-Path (Join-Path $mountPt "local")) { $ready = $true; break }
        }
    }
    Assert-True $ready "mount not ready within 60s (process exited: $($proc.HasExited))"

    # Write through the FUSE path - no qrypt involved on the write side.
    [System.IO.File]::WriteAllText($fuseFile, $content)

    # Decrypted read-back through qrypt.
    $cat = (& $Binary fs cat /local/hello.txt --config $config | Out-String).TrimEnd()
    Assert-True ($cat -eq $content) "fs cat content mismatch (got '$cat')"

    # Backend must hold a single encrypted file name (rclone base32 of the
    # original), never the plaintext hello.txt. Give the async upload
    # (upload_delay=1ms) a moment to land.
    $backendEntries = @()
    for ($i = 0; $i -lt 20 -and $backendEntries.Count -eq 0; $i++) {
        $backendEntries = @(Get-ChildItem -Force $remote)
        if ($backendEntries.Count -eq 0) { Start-Sleep -Milliseconds 500 }
    }
    Assert-True ($backendEntries.Count -eq 1) "backend file count = $($backendEntries.Count), want 1"
    Assert-True ($backendEntries[0].Name -ne "hello.txt") "backend file is not encrypted: $($backendEntries[0].Name)"
    Assert-True ($backendEntries[0].Name -match '^[a-z0-9]{40,}$') "unexpected backend file name: $($backendEntries[0].Name)"

    # Read back through the FUSE path again (served by the mount).
    $readBack = [System.IO.File]::ReadAllText($fuseFile)
    Assert-True ($readBack -eq $content) "FUSE read-back mismatch"

    Write-Host "windows mount smoke passed"
}
catch {
    Write-Host "FAILED: $_"
    if (Test-Path $stdout) { Write-Host "--- mount stdout ---"; Get-Content $stdout }
    if (Test-Path $stderr) { Write-Host "--- mount stderr ---"; Get-Content $stderr }
    exit 1
}
finally {
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 1
    }
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}