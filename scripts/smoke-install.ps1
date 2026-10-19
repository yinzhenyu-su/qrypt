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
    $userPath = Get-UserPathRaw
    $wanted = Normalize-PathEntry $Directory
    return @($userPath -split ';' | Where-Object { $_ -and ((Normalize-PathEntry $_) -ieq $wanted) }).Count
}

# Get-UserPathRaw reads the user PATH without expanding %VAR% references.
function Get-UserPathRaw {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    if ($null -eq $key) {
        return ''
    }
    try {
        $value = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -eq $value) {
            return ''
        }
        return [string]$value
    } finally {
        $key.Dispose()
    }
}

# Get-UserPathKind returns the registry kind of the user PATH value.
function Get-UserPathKind {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    try {
        return $key.GetValueKind('Path')
    } finally {
        $key.Dispose()
    }
}

# Set-UserPathRaw writes the user PATH with an explicit registry kind, so the
# test restores exactly what it found.
function Set-UserPathRaw($Value, $Kind) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    if ($null -eq $key) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    }
    try {
        if ($null -eq $Value) {
            $key.DeleteValue('Path', $false)
        } else {
            $key.SetValue('Path', $Value, $Kind)
        }
    } finally {
        $key.Dispose()
    }
}

# Save-UserPath captures the raw value and kind of the user PATH.
function Save-UserPath {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    if ($null -eq $key) {
        return [pscustomobject]@{ Value = $null; Kind = $null }
    }
    try {
        $value = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $kind = $null
        if ($null -ne $value) {
            $kind = $key.GetValueKind('Path')
        }
        return [pscustomobject]@{ Value = $value; Kind = $kind }
    } finally {
        $key.Dispose()
    }
}

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    return $port
}

function Start-FileServer([string]$Prefix, [string]$Root) {
    # The listener lives in this scope so Stop-FileServer can close it from here;
    # closing it is what releases the runspace's blocking GetContext() call.
    $listener = [System.Net.HttpListener]::new()
    $listener.Prefixes.Add($Prefix)
    $listener.Start()
    $runspace = [runspacefactory]::CreateRunspace()
    $runspace.Open()
    $powerShell = [powershell]::Create()
    $powerShell.Runspace = $runspace
    $null = $powerShell.AddScript({
        param($listener, $root)
        while ($listener.IsListening) {
            try {
                $context = $listener.GetContext()
            } catch {
                break
            }
            $requestPath = $context.Request.Url.AbsolutePath
            if ($requestPath -eq '/releases/latest') {
                $tagFile = Join-Path $root 'latest-tag.txt'
                if (Test-Path -LiteralPath $tagFile -PathType Leaf) {
                    $tag = (Get-Content -LiteralPath $tagFile -Raw).Trim()
                    $context.Response.StatusCode = 302
                    $context.Response.RedirectLocation = "/releases/tag/$tag"
                } else {
                    $context.Response.StatusCode = 404
                }
                $context.Response.Close()
                continue
            }
            if ($requestPath -like '/releases/tag/*') {
                $bytes = [System.Text.Encoding]::UTF8.GetBytes($requestPath.Split('/')[-1])
                $context.Response.ContentType = 'text/plain'
                $context.Response.ContentLength64 = $bytes.Length
                $context.Response.OutputStream.Write($bytes, 0, $bytes.Length)
                $context.Response.Close()
                continue
            }
            if ($requestPath -like '/api/*' -and (Test-Path -LiteralPath (Join-Path $root 'api-403') -PathType Leaf)) {
                $context.Response.StatusCode = 403
                $context.Response.Close()
                continue
            }
            $relative = $requestPath.TrimStart('/')
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
    }).AddArgument($listener).AddArgument($Root)
    $handle = $powerShell.BeginInvoke()
    return [pscustomobject]@{
        Listener   = $listener
        PowerShell = $powerShell
        Handle     = $handle
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
    # Closing the listener releases the runspace's blocking GetContext(), so the
    # pipeline finishes on its own; PowerShell.Stop() alone would block forever.
    try {
        $Server.Listener.Close()
    } catch {
        # The server may already be gone; cleanup must not fail the test.
    }
    try {
        [void]$Server.Handle.AsyncWaitHandle.WaitOne(2000)
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

# The real binary for the first release, and a copy with a trailing marker for
# the newer release, so the test can tell which one ended up installed.
$OriginalBytes = [System.IO.File]::ReadAllBytes($Binary)
$MarkerBytes = [System.Text.Encoding]::ASCII.GetBytes('QRYPT-SMOKE-UPGRADE')
$UpgradeBytes = $OriginalBytes + $MarkerBytes

# Publish-Release <version> <bytes> publishes those bytes as that release and
# points the release "latest" location at it.
function Publish-Release([string]$ReleaseVersion, [byte[]]$ContentBytes) {
    $releaseTag = "v$ReleaseVersion"
    $asset = "qrypt_${ReleaseVersion}_windows_${Architecture}.zip"
    $checksum = "qrypt_${ReleaseVersion}_checksums.txt"
    $releaseDir = Join-Path $Serve "dl/$releaseTag"
    New-Item -ItemType Directory -Force -Path $releaseDir | Out-Null
    [System.IO.File]::WriteAllBytes((Join-Path $Work $Inner), $ContentBytes)
    Compress-Archive -Path (Join-Path $Work $Inner) -DestinationPath (Join-Path $releaseDir $asset) -Force
    $hash = (Get-FileHash -LiteralPath (Join-Path $releaseDir $asset) -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $releaseDir $checksum) -Value "$hash  $asset"
    Set-Content -LiteralPath (Join-Path $Serve 'latest-tag.txt') -Value $releaseTag
}

Publish-Release $Version $OriginalBytes
Copy-Item -LiteralPath (Join-Path $ReleaseDir $Checksum) -Destination (Join-Path $Work 'good-checksums')

$Port = Get-FreePort
$Server = Start-FileServer -Prefix "http://127.0.0.1:$Port/" -Root $Serve
$Base = "http://127.0.0.1:$Port"
Wait-ForServer -Port $Port

$savedUserPath = Save-UserPath
$oldProcessPath = [Environment]::GetEnvironmentVariable('Path', 'Process')

# Seed an unexpanded reference so the test can prove the installer leaves it
# alone and keeps the value's registry kind.
$seeded = if ([string]::IsNullOrEmpty([string]$savedUserPath.Value)) {
    '%QRYPT_SMOKE%\bin'
} else {
    ([string]$savedUserPath.Value).TrimEnd(';') + ';%QRYPT_SMOKE%\bin'
}
Set-UserPathRaw $seeded ([Microsoft.Win32.RegistryValueKind]::String)

function Invoke-Installer([string]$InstallHome, [string[]]$Extra, [switch]$Latest) {
    $env:QRYPT_INSTALL_REPO = $Repo
    $env:QRYPT_INSTALL_API = "$Base/api"
    $env:QRYPT_INSTALL_LATEST_URL = "$Base/releases/latest"
    $env:QRYPT_INSTALL_DOWNLOAD = "$Base/dl"
    $env:QRYPT_INSTALL_HOME = $InstallHome
    if ($Latest) {
        & $Installer @Extra
    } else {
        & $Installer -Version $Tag @Extra
    }
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

    # The seeded %VAR% entry must survive verbatim, and the value must keep the
    # REG_SZ kind it had: the installer may not rewrite entries or the kind.
    $rawPath = Get-UserPathRaw
    if (-not $rawPath.Contains('%QRYPT_SMOKE%\bin')) {
        throw "FAIL: the seeded %VAR% PATH entry was rewritten: $rawPath"
    }
    if ((Get-UserPathKind) -ne [Microsoft.Win32.RegistryValueKind]::String) {
        throw "FAIL: the PATH value kind changed to $((Get-UserPathKind))"
    }

    # 2. Re-running converges: no duplicate PATH entry, one marker.
    Invoke-Installer $home1 @()
    $entryCount = Get-UserPathEntryCount (Join-Path $home1 '.qrypt\bin')
    if ($entryCount -ne 1) { throw "FAIL: re-run duplicated the user PATH entry ($entryCount)" }

    # 3. Without -Version the newest release is resolved from the redirect.
    $home2 = Join-Path $Work 'home2'
    New-Item -ItemType Directory -Force -Path $home2 | Out-Null
    Invoke-Installer $home2 @() -Latest
    if (-not (Test-Path -LiteralPath (Join-Path $home2 '.qrypt\bin\qrypt.exe'))) {
        throw 'FAIL: the redirect did not resolve the newest release'
    }

    # 4. A bad checksum aborts without installing.
    Set-Content -LiteralPath (Join-Path $ReleaseDir $Checksum) -Value ("0" * 64 + "  $Asset")
    $home3 = Join-Path $Work 'home3'
    New-Item -ItemType Directory -Force -Path $home3 | Out-Null
    $failed = $false
    try {
        Invoke-Installer $home3 @()
    } catch {
        $failed = $true
        if ($_.Exception.Message -notmatch 'checksum mismatch') {
            throw "FAIL: wrong error for a bad checksum: $($_.Exception.Message)"
        }
    }
    if (-not $failed) { throw 'FAIL: install succeeded despite a bad checksum' }
    if (Test-Path -LiteralPath (Join-Path $home3 '.qrypt\bin\qrypt.exe')) {
        throw 'FAIL: a bad checksum left a binary behind'
    }
    Copy-Item -LiteralPath (Join-Path $Work 'good-checksums') -Destination (Join-Path $ReleaseDir $Checksum)

    # 5. Re-running while the installed binary is in use installs a newer release.
    #    The handle shares read and delete but not write, which is how Windows
    #    holds a running image: a rename is allowed, an overwrite is not.
    Publish-Release '0.5.4' $UpgradeBytes
    $lock = [System.IO.File]::Open($installed, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read,
        ([System.IO.FileShare]::Read -bor [System.IO.FileShare]::Delete))
    try {
        Invoke-Installer $home1 @() -Latest
    } finally {
        $lock.Dispose()
    }
    $installedBytes = [System.IO.File]::ReadAllBytes($installed)
    if ($installedBytes.Length -ne $UpgradeBytes.Length) {
        throw "FAIL: the re-run did not install the newer release ($($installedBytes.Length) bytes)"
    }
    $entryCount = Get-UserPathEntryCount (Join-Path $home1 '.qrypt\bin')
    if ($entryCount -ne 1) { throw "FAIL: the upgrade re-run duplicated the user PATH entry ($entryCount)" }
    $remnants = @(Get-ChildItem -LiteralPath (Join-Path $home1 '.qrypt\bin') -File |
        Where-Object { $_.Name -like 'qrypt.exe.old*' })
    if ($remnants.Count -ne 0) {
        throw "FAIL: the re-run left a displaced remnant: $($remnants.Name -join ', ')"
    }

    Write-Host 'install smoke test passed'
} finally {
    Set-UserPathRaw $savedUserPath.Value $savedUserPath.Kind
    $env:Path = $oldProcessPath
    Stop-FileServer -Server $Server
    Remove-Item -LiteralPath $Work -Recurse -Force -ErrorAction SilentlyContinue
}
