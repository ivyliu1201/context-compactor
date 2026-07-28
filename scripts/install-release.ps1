#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$ProjectRoot = (Get-Location).Path,

    [ValidateSet("codex", "claude", "all")]
    [string]$AgentHost = "codex",

    [string]$InstallDirectory,

    [string]$Version = "latest"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repository = "ivyliu1201/context-compactor"
$assetName = "context-compactor-windows-amd64.exe"
$checksumsName = "checksums.txt"
$expectedSelfCheck = '{"protocol":"context-compactor/v1","status":"ok"}'

function Invoke-ContextCompactor {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Executable,

        [Parameter(Mandatory = $true)]
        [string[]]$Arguments
    )

    $output = & $Executable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "context-compactor $($Arguments[0]) failed with exit code $LASTEXITCODE."
    }
    return $output
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This installer supports Windows only."
}

if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA) -and
    [string]::IsNullOrWhiteSpace($InstallDirectory)) {
    throw "LOCALAPPDATA is unavailable; specify -InstallDirectory."
}

if ($Version -ne "latest" -and
    $Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
    throw "Version must be 'latest' or a version tag such as v0.1.0."
}

$projectItem = Get-Item -LiteralPath $ProjectRoot -ErrorAction Stop
if (-not $projectItem.PSIsContainer) {
    throw "ProjectRoot must be a directory."
}
$resolvedProjectRoot = $projectItem.FullName

if ([string]::IsNullOrWhiteSpace($InstallDirectory)) {
    $InstallDirectory = Join-Path $env:LOCALAPPDATA "context-compactor"
}
$resolvedInstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)

$releaseBase = if ($Version -eq "latest") {
    "https://github.com/$repository/releases/latest/download"
} else {
    "https://github.com/$repository/releases/download/$Version"
}
$assetUri = "$releaseBase/$assetName"
$checksumsUri = "$releaseBase/$checksumsName"

$downloadDirectory = Join-Path (
    [IO.Path]::GetTempPath()
) (
    "context-compactor-install-{0}" -f [Guid]::NewGuid().ToString("N")
)
$downloadedAsset = Join-Path $downloadDirectory $assetName
$downloadedChecksums = Join-Path $downloadDirectory $checksumsName
$installedPath = $null
$stagedPath = $null
$copiedNewBinary = $false
$hookInstalled = $false

try {
    New-Item -ItemType Directory -Force -Path $downloadDirectory | Out-Null

    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor
        [Net.SecurityProtocolType]::Tls12

    Write-Host "Downloading context-compactor $Version..."
    Invoke-WebRequest -UseBasicParsing -Uri $assetUri -OutFile $downloadedAsset
    Invoke-WebRequest -UseBasicParsing -Uri $checksumsUri -OutFile $downloadedChecksums

    $expectedHashes = @(
        foreach ($line in Get-Content -LiteralPath $downloadedChecksums) {
            if ($line -match '^(?<hash>[0-9A-Fa-f]{64})\s+\*?(?<name>.+?)\s*$' -and
                $Matches.name -eq $assetName) {
                $Matches.hash.ToLowerInvariant()
            }
        }
    )
    if ($expectedHashes.Count -ne 1) {
        throw "checksums.txt must contain exactly one SHA-256 entry for $assetName."
    }

    $assetHash = (
        Get-FileHash -LiteralPath $downloadedAsset -Algorithm SHA256
    ).Hash.ToLowerInvariant()
    if ($assetHash -ne $expectedHashes[0]) {
        throw "Downloaded executable SHA-256 does not match checksums.txt."
    }

    New-Item -ItemType Directory -Force -Path $resolvedInstallDirectory | Out-Null
    $installedPath = Join-Path $resolvedInstallDirectory (
        "context-compactor-{0}.exe" -f $assetHash.Substring(0, 12)
    )

    if (Test-Path -LiteralPath $installedPath -PathType Leaf) {
        $installedHash = (
            Get-FileHash -LiteralPath $installedPath -Algorithm SHA256
        ).Hash.ToLowerInvariant()
        if ($installedHash -ne $assetHash) {
            throw "The existing installed executable does not match its content-addressed name."
        }
    } else {
        $stagedPath = Join-Path $resolvedInstallDirectory (
            ".context-compactor-{0}.tmp" -f [Guid]::NewGuid().ToString("N")
        )
        Copy-Item -LiteralPath $downloadedAsset -Destination $stagedPath
        $stagedHash = (
            Get-FileHash -LiteralPath $stagedPath -Algorithm SHA256
        ).Hash.ToLowerInvariant()
        if ($stagedHash -ne $assetHash) {
            throw "Staged executable SHA-256 does not match the downloaded executable."
        }
        Move-Item -LiteralPath $stagedPath -Destination $installedPath
        $stagedPath = $null
        $copiedNewBinary = $true
    }

    $selfCheck = Invoke-ContextCompactor -Executable $installedPath -Arguments @("self-check")
    if (($selfCheck -join "`n") -ne $expectedSelfCheck) {
        throw "Installed executable returned an unexpected self-check response."
    }

    Write-Host "Installing $AgentHost hooks into $resolvedProjectRoot..."
    $installReport = Invoke-ContextCompactor -Executable $installedPath -Arguments @(
        "install",
        "--host",
        $AgentHost,
        "--project-root",
        $resolvedProjectRoot,
        "--executable",
        $installedPath
    )
    $hookInstalled = $true

    $statusReport = Invoke-ContextCompactor -Executable $installedPath -Arguments @(
        "status",
        "--host",
        $AgentHost,
        "--project-root",
        $resolvedProjectRoot
    )
    $doctorReport = Invoke-ContextCompactor -Executable $installedPath -Arguments @(
        "doctor",
        "--host",
        $AgentHost,
        "--project-root",
        $resolvedProjectRoot
    )

    $installReport | Write-Output
    $statusReport | Write-Output
    $doctorReport | Write-Output
    Write-Host "Installed executable: $installedPath"
    if ($AgentHost -eq "codex" -or $AgentHost -eq "all") {
        Write-Host "Open /hooks in Codex and trust the project hook before relying on activation."
    }
} catch {
    if ($copiedNewBinary -and -not $hookInstalled -and
        -not [string]::IsNullOrWhiteSpace($installedPath) -and
        (Test-Path -LiteralPath $installedPath -PathType Leaf)) {
        Remove-Item -LiteralPath $installedPath -Force
    }
    throw
} finally {
    if (-not [string]::IsNullOrWhiteSpace($stagedPath) -and
        (Test-Path -LiteralPath $stagedPath -PathType Leaf)) {
        Remove-Item -LiteralPath $stagedPath -Force
    }
    if (Test-Path -LiteralPath $downloadDirectory -PathType Container) {
        Remove-Item -LiteralPath $downloadDirectory -Recurse -Force
    }
}
