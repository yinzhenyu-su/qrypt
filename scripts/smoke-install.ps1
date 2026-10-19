# End-to-end smoke test for the qrypt Windows installer.
#
# Serves a fake release over loopback HTTP and runs scripts/install.ps1 against
# it in a throwaway home. It asserts the install layout, that the user PATH gains
# exactly one entry, that a bad checksum aborts without installing, and that the
# installed command runs.
#
# Usage:
#   pwsh ./scripts/smoke-install.ps1 [-Installer <path>] [-Binary <qrypt.exe>]
#
# -Binary defaults to the qrypt.exe built at the repository root. The release is
# a zip containing that binary under its in-archive name, so `qrypt version`
# exercises the real executable.
[CmdletBinding()]
param(
    [string]$Installer = '',
    [string]$Binary = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Normalize-PathEntry canonicalizes a PATH entry so a forward slash and a
# backslash name the same directory when counting entries.
function Normalize-PathEntry([string]$Value) {
    if ([string]::IsNullOrEmpty($Value)) {
        return ''
    }
    return ($Value.Trim().TrimEnd('\') -replace '/', '\')
}

# Get-UserPathEntryCount returns how many user-scope PATH entries name Directory.
function Get-UserPathEntryCount([string]$Directory) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $wanted = Normalize-PathEntry $Directory
    return @($userPath -split ';' | Where-Object { $_ -and ((Normalize-PathEntry $_) -ieq $wanted) }).Count
}

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    return $port
}

function Start-FileServer([string]$Prefix, [string]$Root) {
    $runspace = [runspacefactory]::CreateRunspace()
    $runspace.Open()
    $powerShell = [powershell]::Create()
    $powerShell.Runspace = $runspace
    $null = $powerShell.AddScript({
        param($prefix, $root)
        $listener = [System.Net.HttpListener]::new()
        $listener.Prefixes.Add($prefix)
        $listener.Start()
        while ($listener.IsListening) {
            try {
                $context = $listener.GetContext()
            } catch {
                break
            }
            $relative = $context.Request.Url.AbsolutePath.TrimStart('/')
            $path = Join-Path $root ($relative -replace '/', '\')
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                $bytes = [System.IO.File]::ReadAllBytes($path)
                $context.Response.ContentType = 'application/octet-stream'
                $context.Response.ContentLength64 = $bytes.Length
                $context.Response.OutputStream.Write($bytes, 0, $bytes.Length)
            } else {
                $context.Response.StatusCode = 404
            }
            $context.Response.Close()
        }
    }).AddArgument($Prefix).AddArgument($Root)
    return [pscustomobject]@{
        PowerShell = $powerShell
        Handle     = $powerShell.BeginInvoke()
    }
}

function Wait-ForServer([int]$Port) {
    for ($i = 0; $i -lt 50; $i++) {
        $client = [System.Net.Sockets.TcpClient]::new()
        try {
            $client.Connect('127.0.0.1', $Port)
            return
        } catch {
            Start-Sleep -Milliseconds 100
        } finally {
            $client.Dispose()
        }
    }
    throw "server did not start on port $Port"
}

function Stop-FileServer($Server) {
    if ($null -eq $Server) {
        return
    }
    try {
        $Server.PowerShell.Stop()
        $Server.PowerShell.Dispose()
    } catch {
        # The server may already be gone; cleanup must not fail the test.
    }
}

$Root = Split-Path -Parent $PSScriptRoot
if (-not $Installer) {
    $Installer = Join-Path $PSScriptRoot 'install.ps1'
}
if (-not $Binary) {
    $Binary = Join-Path $Root 'qrypt.exe'
}
$Installer = (Resolve-Path -LiteralPath $Installer).Path
$Binary = (Resolve-Path -LiteralPath $Binary).Path

$Architecture = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
    'X64' { 'amd64' }
    'Arm64' { 'arm64' }
    default { throw "smoke-install: unsupported architecture: $_" }
}

$Version = '0.5.3'
$Tag = "v$Version"
$Asset = "qrypt_${Version}_windows_${Architecture}.zip"
$Checksum = "qrypt_${Version}_checksums.txt"
$Inner = "qrypt-windows-$Architecture.exe"
$Repo = 'smoke/qrypt'

$Work = Join-Path ([System.IO.Path]::GetTempPath()) ('qrypt-smoke-' + [guid]::NewGuid().ToString('N'))
$Serve = Join-Path $Work 'serve'
$ReleaseDir = Join-Path $Serve "dl/$Tag"
New-Item -ItemType Directory -Force -Path $ReleaseDir | Out-Null

Copy-Item -LiteralPath $Binary -Destination (Join-Path $Work $Inner)
Compress-Archive -Path (Join-Path $Work $Inner) -DestinationPath (Join-Path $ReleaseDir $Asset)
$hash = (Get-FileHash -LiteralPath (Join-Path $ReleaseDir $Asset) -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -LiteralPath (Join-Path $ReleaseDir $Checksum) -Value "$hash  $Asset"
Copy-Item -LiteralPath (Join-Path $ReleaseDir $Checksum) -Destination (Join-Path $Work 'good-checksums')

$Port = Get-FreePort
$Server = Start-FileServer -Prefix "http://127.0.0.1:$Port/" -Root $Serve
$Base = "http://127.0.0.1:$Port"
Wait-ForServer -Port $Port

$oldUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$oldProcessPath = [Environment]::GetEnvironmentVariable('Path', 'Process')

function Invoke-Installer([string]$Home, [string[]]$Extra) {
    $env:QRYPT_INSTALL_REPO = $Repo
    $env:QRYPT_INSTALL_API = "$Base/api"
    $env:QRYPT_INSTALL_DOWNLOAD = "$Base/dl"
    $env:QRYPT_INSTALL_HOME = $Home
    & $Installer -Version $Tag @Extra
}

try {
    # 1. Fresh install into a throwaway home.
    $home1 = Join-Path $Work 'home1'
    New-Item -ItemType Directory -Force -Path $home1 | Out-Null
    Invoke-Installer $home1 @()

    $installed = Join-Path $home1 '.qrypt\bin\qrypt.exe'
    $marker = Join-Path $home1 '.qrypt\bin\.qrypt-install'
    if (-not (Test-Path -LiteralPath $installed)) { throw 'FAIL: installed binary missing' }
    if (-not (Test-Path -LiteralPath $marker)) { throw 'FAIL: install marker missing' }
    $entryCount = Get-UserPathEntryCount (Join-Path $home1 '.qrypt\bin')
    if ($entryCount -ne 1) { throw "FAIL: expected one user PATH entry, found $entryCount" }

    $output = & $installed version
    if ($LASTEXITCODE -ne 0) { throw "FAIL: installed qrypt version exited $LASTEXITCODE" }
    if (-not ($output -match 'qrypt')) { throw "FAIL: unexpected version output: $output" }

    # 2. Re-running converges: no duplicate PATH entry, one marker.
    Invoke-Installer $home1 @()
    $entryCount = Get-UserPathEntryCount (Join-Path $home1 '.qrypt\bin')
    if ($entryCount -ne 1) { throw "FAIL: re-run duplicated the user PATH entry ($entryCount)" }

    # 3. A bad checksum aborts without installing.
    Set-Content -LiteralPath (Join-Path $ReleaseDir $Checksum) -Value ("0" * 64 + "  $Asset")
    $home2 = Join-Path $Work 'home2'
    New-Item -ItemType Directory -Force -Path $home2 | Out-Null
    $failed = $false
    try {
        Invoke-Installer $home2 @()
    } catch {
        $failed = $true
        if ($_.Exception.Message -notmatch 'checksum mismatch') {
            throw "FAIL: wrong error for a bad checksum: $($_.Exception.Message)"
        }
    }
    if (-not $failed) { throw 'FAIL: install succeeded despite a bad checksum' }
    if (Test-Path -LiteralPath (Join-Path $home2 '.qrypt\bin\qrypt.exe')) {
        throw 'FAIL: a bad checksum left a binary behind'
    }
    Copy-Item -LiteralPath (Join-Path $Work 'good-checksums') -Destination (Join-Path $ReleaseDir $Checksum)

    Write-Host 'install smoke test passed'
} finally {
    if ($null -eq $oldUserPath) {
        [Environment]::SetEnvironmentVariable('Path', $null, 'User')
    } else {
        [Environment]::SetEnvironmentVariable('Path', $oldUserPath, 'User')
    }
    $env:Path = $oldProcessPath
    Stop-FileServer -Server $Server
    Remove-Item -LiteralPath $Work -Recurse -Force -ErrorAction SilentlyContinue
}
