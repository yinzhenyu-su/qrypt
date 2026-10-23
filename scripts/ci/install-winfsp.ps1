$ErrorActionPreference = 'Stop'

$version = '2.1.25156'
$expectedHash = '073a70e00f77423e34bed98b86e600def93393ba5822204fac57a29324db9f7a'
$fileName = "winfsp-$version.msi"
$cacheDir = Join-Path $env:GITHUB_WORKSPACE '.cache/winfsp'
$msi = Join-Path $cacheDir $fileName

New-Item -ItemType Directory -Path $cacheDir -Force | Out-Null
if (-not (Test-Path -LiteralPath $msi)) {
    $url = "https://github.com/winfsp/winfsp/releases/download/v2.1/$fileName"
    Invoke-WebRequest -Uri $url -OutFile "$msi.download"
    Move-Item -LiteralPath "$msi.download" -Destination $msi -Force
}

$actualHash = (Get-FileHash -LiteralPath $msi -Algorithm SHA256).Hash
if ($actualHash -ine $expectedHash) {
    throw "WinFsp MSI SHA-256 mismatch: expected $expectedHash, got $actualHash"
}

$process = Start-Process msiexec.exe -ArgumentList '/i', "`"$msi`"", '/qn', '/norestart' -Wait -PassThru
# 3010 means a reboot is required but the driver is installed.
if ($process.ExitCode -notin @(0, 3010)) { throw "WinFsp MSI exited with $($process.ExitCode)" }
