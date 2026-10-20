# qrypt installer for Windows.
#
# Downloads the newest (or a requested) qrypt release for this architecture,
# verifies it against the published sha256 checksum, and installs the binary at
# %USERPROFILE%\.qrypt\bin\qrypt.exe. That directory is added to the user-scope
# PATH, so no administrator rights are needed.
#
# Usage:
#   irm https://raw.githubusercontent.com/yinzhenyu-su/qrypt/main/scripts/install/install.ps1 | iex
#   .\install.ps1 [-Version vX.Y.Z] [-Force]
#
# -PrintManifest is a debugging switch used by the naming drift test; it prints
# the release naming rule without touching the machine.
#
# Environment overrides (mainly for testing):
#   QRYPT_INSTALL_REPO          owner/repo             (default yinzhenyu-su/qrypt)
#   QRYPT_INSTALL_API           GitHub API base        (default https://api.github.com)
#   QRYPT_INSTALL_DOWNLOAD      release download base  (default https://github.com/<repo>/releases/download)
#   QRYPT_INSTALL_HOME          home directory         (default $env:USERPROFILE)
#   QRYPT_INSTALL_LATEST_URL    release "latest" location (default https://github.com/<repo>/releases/latest)
#   QRYPT_INSTALL_LATEST_TAG    skip the lookup and use this tag
#   GITHUB_TOKEN / GH_TOKEN     optional token, for the API fallback
[CmdletBinding()]
param(
    [string]$Version = "",
    [switch]$Force,
    [switch]$PrintManifest,
    [string]$ManifestOS = "",
    [string]$ManifestArch = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Repo = if ($env:QRYPT_INSTALL_REPO) { $env:QRYPT_INSTALL_REPO } else { 'yinzhenyu-su/qrypt' }
$ApiBase = if ($env:QRYPT_INSTALL_API) { $env:QRYPT_INSTALL_API } else { 'https://api.github.com' }
$DownloadBase = if ($env:QRYPT_INSTALL_DOWNLOAD) { $env:QRYPT_INSTALL_DOWNLOAD } else { "https://github.com/$Repo/releases/download" }
$HomeDir = if ($env:QRYPT_INSTALL_HOME) { $env:QRYPT_INSTALL_HOME } else { $env:USERPROFILE }
$LatestUrl = if ($env:QRYPT_INSTALL_LATEST_URL) { $env:QRYPT_INSTALL_LATEST_URL } else { "https://github.com/$Repo/releases/latest" }

# These must match pkg/install (enforced by the naming drift test).
$RelRealDir = '.qrypt/bin'
$BinaryName = 'qrypt.exe'
$MarkerName = '.qrypt-install'

function Write-Info([string]$Message) {
    Write-Host $Message
}

function Write-WarningLine([string]$Message) {
    Write-Warning $Message
}

function Normalize-Version([string]$Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) {
        return ''
    }
    $Value = $Value.Trim()
    if ($Value -match '^v?(\d+\.\d+\.\d+)$') {
        return $Matches[1]
    }
    throw "invalid version: $Value (expected something like v0.5.0)"
}

function Get-OsName {
    if ($env:OS -eq 'Windows_NT') {
        return 'windows'
    }
    if ($PSVersionTable.PSVersion.Major -ge 6) {
        if ($IsWindows) { return 'windows' }
        if ($IsMacOS) { return 'darwin' }
        if ($IsLinux) { return 'linux' }
    }
    throw "unsupported operating system (install manually from the releases page)"
}

function Get-ArchName {
    if ($PSVersionTable.PSVersion.Major -ge 6) {
        $architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
        switch ($architecture) {
            'X64' { return 'amd64' }
            'Arm64' { return 'arm64' }
            'X86' { return '386' }
        }
    }
    $raw = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    switch ($raw.ToUpperInvariant()) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        'X86' { return '386' }
        default { throw "unsupported architecture: $raw (install manually from the releases page)" }
    }
}

function Get-ArchiveExtension([string]$OsName) {
    if ($OsName -eq 'windows') {
        return 'zip'
    }
    return 'tar.gz'
}

function Get-AssetName([string]$VersionNumber, [string]$OsName, [string]$ArchName) {
    $extension = Get-ArchiveExtension $OsName
    return "qrypt_${VersionNumber}_${OsName}_${ArchName}.$extension"
}

function Get-ChecksumName([string]$VersionNumber) {
    return "qrypt_${VersionNumber}_checksums.txt"
}

function Get-InnerName([string]$OsName, [string]$ArchName) {
    if ($OsName -eq 'windows') {
        return "qrypt-$OsName-$ArchName.exe"
    }
    return "qrypt-$OsName-$ArchName"
}

function Write-Manifest([string]$VersionNumber, [string]$OsName, [string]$ArchName) {
    Write-Output "asset=$(Get-AssetName $VersionNumber $OsName $ArchName)"
    Write-Output "checksum=$(Get-ChecksumName $VersionNumber)"
    Write-Output "inner=$(Get-InnerName $OsName $ArchName)"
    Write-Output "real_dir=$RelRealDir"
    Write-Output "binary_name=$BinaryName"
    Write-Output "marker_name=$MarkerName"
}

# Get-LatestTagFromRedirect follows the release "latest" location to its tag.
# It is a plain website redirect, not the API, so it costs no API quota.
function Get-LatestTagFromRedirect {
    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($null -eq $curl) {
        return $null
    }
    try {
        $effective = & $curl.Source -fsSL -o NUL -w '%{url_effective}' $LatestUrl 2>$null
    } catch {
        return $null
    }
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace([string]$effective)) {
        return $null
    }
    if ([string]$effective -match '/tag/([^/]+)/?$') {
        return $Matches[1]
    }
    return $null
}

# Get-LatestTagFromApi is the fallback: it needs a token when the anonymous rate
# limit is exhausted, but it works where the releases website does not.
function Get-LatestTagFromApi {
    $headers = @{}
    $token = if ($env:GH_TOKEN) { $env:GH_TOKEN } elseif ($env:GITHUB_TOKEN) { $env:GITHUB_TOKEN } else { '' }
    if ($token) {
        $headers['Authorization'] = "Bearer $token"
    }
    try {
        $release = Invoke-RestMethod -Uri "$ApiBase/repos/$Repo/releases/latest" -Headers $headers -UseBasicParsing
    } catch {
        throw "failed to query the latest release (rate limit? set GITHUB_TOKEN): $_"
    }
    $tagProperty = $release.PSObject.Properties['tag_name']
    if ($null -eq $tagProperty -or [string]::IsNullOrWhiteSpace([string]$tagProperty.Value)) {
        throw 'could not read the latest release tag'
    }
    return [string]$tagProperty.Value
}

function Get-LatestTag {
    if ($env:QRYPT_INSTALL_LATEST_TAG) {
        return $env:QRYPT_INSTALL_LATEST_TAG
    }
    $tag = Get-LatestTagFromRedirect
    if ([string]::IsNullOrWhiteSpace([string]$tag)) {
        $tag = Get-LatestTagFromApi
    }
    if ([string]::IsNullOrWhiteSpace([string]$tag)) {
        throw "could not read the latest release tag (is $LatestUrl reachable? set GITHUB_TOKEN to use the API)"
    }
    return $tag
}

function Invoke-Download([string]$Uri, [string]$Destination) {
    try {
        Invoke-WebRequest -Uri $Uri -OutFile $Destination -UseBasicParsing
    } catch {
        throw "failed to download ${Uri}: $_"
    }
}

function Get-ExpectedChecksum([string]$ChecksumFile, [string]$AssetName) {
    foreach ($line in Get-Content -LiteralPath $ChecksumFile) {
        $fields = $line.Trim() -split '\s+'
        if ($fields.Length -lt 2) {
            continue
        }
        $name = $fields[1].TrimStart('*')
        if ($name -eq $AssetName) {
            return $fields[0].ToLowerInvariant()
        }
    }
    throw "no checksum published for $AssetName"
}

# Normalize-PathEntry canonicalizes a PATH entry for comparison: it trims
# surrounding whitespace, drops a trailing separator, and unifies separators so
# a forward slash and a backslash name the same directory.
function Normalize-PathEntry([string]$Value) {
    if ([string]::IsNullOrEmpty($Value)) {
        return ''
    }
    return ($Value.Trim().TrimEnd('\') -replace '/', '\')
}

function Test-PathHasDirectory([string]$PathValue, [string]$Directory) {
    if ([string]::IsNullOrEmpty($PathValue)) {
        return $false
    }
    $wanted = Normalize-PathEntry $Directory
    foreach ($entry in $PathValue -split ';') {
        $candidate = Normalize-PathEntry $entry
        if ($candidate -and ($candidate -ieq $wanted)) {
            return $true
        }
    }
    return $false
}

# Read-Env returns the raw user-scope environment value without expanding %VAR%
# references, or $null when it is unset. Reading raw is what keeps the installer
# from rewriting entries it did not add, and it is how pkg/install reads the same
# value.
function Read-Env([string]$Name) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    if ($null -eq $key) {
        return $null
    }
    try {
        $value = $key.GetValue($Name, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -eq $value) {
            return $null
        }
        return [string]$value
    } finally {
        $key.Dispose()
    }
}

# Write-Env writes a user-scope environment value, preserving the registry kind so
# an existing REG_EXPAND_SZ stays expandable instead of being flattened to a
# plain string. [Environment]::SetEnvironmentVariable does neither, which is why
# the value goes through the registry directly, matching pkg/install.
function Write-Env([string]$Name, [string]$Value) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    if ($null -eq $key) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    }
    try {
        $kind = [Microsoft.Win32.RegistryValueKind]::String
        if ($null -ne $key.GetValue($Name, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)) {
            $kind = $key.GetValueKind($Name)
        }
        $key.SetValue($Name, $Value, $kind)
    } finally {
        $key.Dispose()
    }
    Publish-Env
}

# Initialize-NativeMethods loads the two Win32 calls the installer needs:
# SendMessageTimeout, to announce a changed environment, and MoveFileEx, to ask
# Windows to delete a displaced binary after the next restart. It reports
# whether the type is available; a restricted session may refuse to compile it.
function Initialize-NativeMethods {
    if ('Qrypt.NativeMethods' -as [Type]) {
        return $true
    }
    try {
        Add-Type -Namespace Qrypt -Name NativeMethods -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Auto)]
public static extern IntPtr SendMessageTimeout(
    IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam,
    uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);

[DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
public static extern bool MoveFileEx(string lpExistingFileName, string lpNewFileName, uint dwFlags);
'@
        return $true
    } catch {
        return $false
    }
}

# Publish-Env broadcasts WM_SETTINGCHANGE so processes started afterwards pick up
# the changed environment. It is best-effort: a session without a message pump
# must not fail the install.
function Publish-Env {
    try {
        if (-not (Initialize-NativeMethods)) {
            return
        }
        $result = [UIntPtr]::Zero
        [void][Qrypt.NativeMethods]::SendMessageTimeout(
            [IntPtr]0xffff, 0x1a, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
    } catch {
        # Best-effort: a restricted session or a missing desktop is not a failure.
    }
}

function Add-UserPath([string]$Directory) {
    $current = Read-Env 'Path'
    if (Test-PathHasDirectory $current $Directory) {
        return $false
    }
    $updated = if ([string]::IsNullOrEmpty($current)) {
        $Directory
    } else {
        $current.TrimEnd(';') + ';' + $Directory
    }
    Write-Env 'Path' $updated
    return $true
}

function Show-PathInstructions {
    Write-Info "Add $RealDir to your user PATH:"
    Write-Info "  [Environment]::SetEnvironmentVariable('Path', `$env:Path + ';$RealDir', 'User')"
}

function Configure-Path {
    if (Test-PathHasDirectory (Read-Env 'Path') $RealDir) {
        Write-Info "$RealDir is already on PATH"
    } else {
        try {
            [void](Add-UserPath $RealDir)
            Write-Info "Added $RealDir to the user PATH"
        } catch {
            Write-WarningLine "could not update the user PATH: $_"
            Show-PathInstructions
        }
    }

    # Make the just-installed copy visible to the shadow check below.
    $processPath = [Environment]::GetEnvironmentVariable('Path', 'Process')
    if (-not (Test-PathHasDirectory $processPath $RealDir)) {
        $env:Path = if ([string]::IsNullOrEmpty($processPath)) { $RealDir } else { $processPath.TrimEnd(';') + ';' + $RealDir }
    }
}

function Test-ExistingBinary {
    if (-not (Test-Path -LiteralPath $Binary)) {
        return
    }
    if (Test-Path -LiteralPath $Binary -PathType Container) {
        throw "$Binary is a directory; remove it and re-run the installer"
    }
    if (-not (Test-Path -LiteralPath $Marker) -and -not $Force) {
        throw "$Binary already exists and is not managed by this installer; re-run with -Force to replace it"
    }
}

# Remove-StaleDisplaced removes the ".old" remnants a prior reinstall or upgrade
# left beside the binary, so none of them blocks this run. qrypt uninstall sweeps
# these files too.
function Remove-StaleDisplaced([string]$Path) {
    $directory = Split-Path -Parent $Path
    $base = (Split-Path -Leaf $Path) + '.old'
    Get-ChildItem -LiteralPath $directory -File -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -eq $base -or $_.Name -like "$base.*" } |
        ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
}

# Invoke-DeferredDelete removes a displaced binary now, and when it is the running
# image (and therefore still locked) asks Windows to delete it after the next
# restart.
function Invoke-DeferredDelete([string]$Path) {
    try {
        Remove-Item -LiteralPath $Path -Force -ErrorAction Stop
    } catch {
        if (-not (Initialize-NativeMethods)) {
            return
        }
        [void][Qrypt.NativeMethods]::MoveFileEx($Path, $null, 4)
    }
}

# Set-BinaryInPlace replaces the installed binary with the staged file. A running
# executable cannot be overwritten in place, so the old image is renamed aside
# and the displaced file is deleted now or after the next restart - the same
# dance pkg/selfupdate performs, with the same names qrypt uninstall sweeps.
function Set-BinaryInPlace([string]$Staged, [string]$Target) {
    Remove-StaleDisplaced $Target
    if (-not (Test-Path -LiteralPath $Target)) {
        [System.IO.File]::Move($Staged, $Target)
        return
    }
    $displaced = "$Target.old"
    try {
        [System.IO.File]::Move($Target, $displaced)
    } catch {
        $displaced = "$Target.old.$PID"
        [System.IO.File]::Move($Target, $displaced)
    }
    try {
        [System.IO.File]::Move($Staged, $Target)
    } catch {
        # Put the original back so a failed install leaves a working binary.
        [System.IO.File]::Move($displaced, $Target)
        throw
    }
    Invoke-DeferredDelete $displaced
}

function Install-Binary([string]$Archive, [string]$InnerName, [string]$TemporaryDirectory) {
    $extractDir = Join-Path $TemporaryDirectory 'extract'
    Expand-Archive -LiteralPath $Archive -DestinationPath $extractDir -Force
    $innerFile = Get-ChildItem -LiteralPath $extractDir -Recurse -File |
        Where-Object { $_.Name -eq $InnerName } |
        Select-Object -First 1
    if ($null -eq $innerFile) {
        throw "archive does not contain $InnerName"
    }

    New-Item -ItemType Directory -Force -Path $RealDir | Out-Null
    $staged = Join-Path $RealDir ('.qrypt-new-' + [guid]::NewGuid().ToString('N'))
    Copy-Item -LiteralPath $innerFile.FullName -Destination $staged
    try {
        Set-BinaryInPlace $staged $Binary
    } catch {
        Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
        throw "could not install the binary: $_"
    }
    Set-Content -LiteralPath $Marker -Value 'managed by scripts/install/install.ps1' -NoNewline
}

function Test-ShadowCopy {
    $found = Get-Command qrypt -ErrorAction SilentlyContinue
    if ($null -eq $found) {
        return
    }
    $source = $null
    foreach ($propertyName in @('Source', 'Path')) {
        $property = $found.PSObject.Properties[$propertyName]
        if ($null -ne $property -and $property.Value) {
            $source = [string]$property.Value
            break
        }
    }
    if (-not $source) {
        return
    }
    $resolvedSource = [System.IO.Path]::GetFullPath([string]$source)
    $resolvedBinary = [System.IO.Path]::GetFullPath($Binary)
    if (-not ($resolvedSource -ieq $resolvedBinary)) {
        Write-WarningLine "another qrypt is on PATH: $resolvedSource"
        Write-WarningLine "this installer placed $Binary; it runs only once $RealDir precedes that copy on PATH"
    }
}

if ($PrintManifest) {
    $manifestOs = if ($ManifestOS) { $ManifestOS } else { Get-OsName }
    $manifestArch = if ($ManifestArch) { $ManifestArch } else { Get-ArchName }
    $manifestVersion = if ($Version) { Normalize-Version $Version } else { '0.0.0' }
    Write-Manifest $manifestVersion $manifestOs $manifestArch
    exit 0
}

if ([string]::IsNullOrWhiteSpace($HomeDir)) {
    throw 'could not determine the user profile directory; set QRYPT_INSTALL_HOME'
}

# Build the native (backslash) form so the PATH entry matches what pkg/install
# and a hand-edited PATH use. The relative constant keeps forward slashes for
# the naming drift manifest, which pkg/install also publishes.
$RealDir = (Join-Path $HomeDir $RelRealDir) -replace '/', '\'
$Binary = Join-Path $RealDir $BinaryName
$Marker = Join-Path $RealDir $MarkerName

$versionNumber = Normalize-Version $Version
$osName = Get-OsName
if ($osName -ne 'windows') {
    throw "unsupported operating system: $osName (install manually from the releases page)"
}
$archName = Get-ArchName
if ($archName -notin @('amd64', 'arm64')) {
    throw "unsupported architecture: $archName (no release asset is published for it)"
}

$temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ('qrypt-install-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null

try {
    if ($versionNumber) {
        $tag = "v$versionNumber"
    } else {
        $tag = [string](Get-LatestTag)
        if (-not $tag.StartsWith('v')) {
            $tag = "v$tag"
        }
    }
    $releaseVersion = $tag.TrimStart('v')

    $asset = Get-AssetName $releaseVersion $osName $archName
    $checksum = Get-ChecksumName $releaseVersion
    $inner = Get-InnerName $osName $archName

    Test-ExistingBinary

    Write-Info "Installing qrypt $tag for $osName/$archName"
    $archivePath = Join-Path $temporaryDirectory $asset
    $checksumPath = Join-Path $temporaryDirectory $checksum
    Invoke-Download "$DownloadBase/$tag/$asset" $archivePath
    Invoke-Download "$DownloadBase/$tag/$checksum" $checksumPath

    $expected = Get-ExpectedChecksum $checksumPath $asset
    $actual = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "checksum mismatch for $asset"
    }

    Install-Binary $archivePath $inner $temporaryDirectory
    Write-Info "Installed qrypt to $Binary"

    Configure-Path
    Test-ShadowCopy

    Write-Info ''
    Write-Info "qrypt $tag is installed."
    Write-Info 'To use it in this session, open a new terminal or run:'
    Write-Info "  `$env:Path = `"$RealDir;`$env:Path`""
    Write-Info 'Then:'
    Write-Info '  qrypt version'
} finally {
    Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force -ErrorAction SilentlyContinue
}
