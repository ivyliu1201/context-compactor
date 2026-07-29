#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$ProjectRoot = $HOME,

    [ValidateSet("codex", "claude", "all")]
    [string]$AgentHost = "codex",

    [string]$InstallDirectory,

    [string]$Version = "latest"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$repository = "ivyliu1201/context-compactor"
$assetName = "context-compactor-windows-amd64.exe"
$checksumsName = "checksums.txt"
$expectedSelfCheck = '{"protocol":"context-compactor/v1","status":"ok"}'
$useDynamicCodexProjectRoot =
    -not $PSBoundParameters.ContainsKey("ProjectRoot") -and
    ($AgentHost -eq "codex" -or $AgentHost -eq "all")

function Write-InstallerStep {
    param([Parameter(Mandatory = $true)][string]$Message)

    Write-Host ("==> {0}" -f $Message) -ForegroundColor Cyan
}

function Write-InstallerSuccess {
    param([Parameter(Mandatory = $true)][string]$Message)

    Write-Host ("[OK] {0}" -f $Message) -ForegroundColor Green
}

function Write-InstallerDetail {
    param([Parameter(Mandatory = $true)][string]$Message)

    Write-Host ("     {0}" -f $Message) -ForegroundColor Gray
}

function Write-InstallerAction {
    param([Parameter(Mandatory = $true)][string]$Message)

    Write-Host ("[ACTION REQUIRED] {0}" -f $Message) -ForegroundColor Yellow
}

function Write-InstallerFailure {
    param([Parameter(Mandatory = $true)][string]$Message)

    Write-Host ("[FAILED] {0}" -f $Message) -ForegroundColor Red
}

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

function ConvertFrom-ContextCompactorReport {
    param(
        [Parameter(Mandatory = $true)]
        [object[]]$Lines,

        [Parameter(Mandatory = $true)]
        [string]$ExpectedCommand
    )

    $encoded = @($Lines) -join "`n"
    if ([string]::IsNullOrWhiteSpace($encoded)) {
        throw "context-compactor $ExpectedCommand returned no report."
    }
    try {
        $document = $encoded | ConvertFrom-Json
    } catch {
        throw "context-compactor $ExpectedCommand returned an invalid report."
    }
    if ($document.command -ne $ExpectedCommand -or
        $null -eq $document.reports -or
        @($document.reports).Count -eq 0) {
        throw "context-compactor $ExpectedCommand returned an unexpected report."
    }
    return $document
}

function Assert-ContextCompactorReport {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Document,

        [switch]$RequireExecutableHealthy
    )

    foreach ($report in @($Document.reports)) {
        if (-not $report.installed -or -not $report.definition_healthy) {
            throw "The $($report.host) Hook definition is unhealthy."
        }
        if ($RequireExecutableHealthy -and -not $report.executable_healthy) {
            throw "The $($report.host) executable is unhealthy."
        }
    }
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

Write-Host ""
Write-Host "context-compactor installer" -ForegroundColor White
Write-Host "---------------------------" -ForegroundColor DarkGray

try {
    New-Item -ItemType Directory -Force -Path $downloadDirectory | Out-Null

    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor
        [Net.SecurityProtocolType]::Tls12

    Write-InstallerStep "1/5 Downloading context-compactor $Version"
    Invoke-WebRequest -UseBasicParsing -Uri $assetUri -OutFile $downloadedAsset
    Invoke-WebRequest -UseBasicParsing -Uri $checksumsUri -OutFile $downloadedChecksums
    Write-InstallerSuccess "Release executable and checksum downloaded."

    Write-InstallerStep "2/5 Verifying SHA-256 checksum"
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
    Write-InstallerSuccess "SHA-256 checksum verified."
    Write-InstallerDetail $assetHash

    Write-InstallerStep "3/5 Installing and checking the executable"
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
    Write-InstallerSuccess "Executable is installed."
    Write-InstallerDetail $installedPath

    $selfCheck = Invoke-ContextCompactor -Executable $installedPath -Arguments @("self-check")
    if (($selfCheck -join "`n") -ne $expectedSelfCheck) {
        throw "Installed executable returned an unexpected self-check response."
    }
    Write-InstallerSuccess "Executable self-check passed."

    Write-InstallerStep "4/5 Configuring $AgentHost Hook definitions"
    $installArguments = @(
        "install",
        "--host",
        $AgentHost,
        "--project-root",
        $resolvedProjectRoot,
        "--executable",
        $installedPath
    )
    if ($useDynamicCodexProjectRoot) {
        $installArguments += "--dynamic-codex-project-root"
        Write-InstallerDetail "Codex scope: global; project root follows each Hook cwd."
    }
    Write-InstallerDetail ("Configuration root: {0}" -f $resolvedProjectRoot)
    $installReport = Invoke-ContextCompactor `
        -Executable $installedPath `
        -Arguments $installArguments
    $hookInstalled = $true
    $installDocument = ConvertFrom-ContextCompactorReport `
        -Lines $installReport `
        -ExpectedCommand "install"
    Assert-ContextCompactorReport `
        -Document $installDocument `
        -RequireExecutableHealthy
    Write-InstallerSuccess "Hook definitions installed."

    Write-InstallerStep "5/5 Running status and doctor checks"
    $statusReport = Invoke-ContextCompactor -Executable $installedPath -Arguments @(
        "status",
        "--host",
        $AgentHost,
        "--project-root",
        $resolvedProjectRoot
    )
    $statusDocument = ConvertFrom-ContextCompactorReport `
        -Lines $statusReport `
        -ExpectedCommand "status"
    Assert-ContextCompactorReport -Document $statusDocument
    $doctorReport = Invoke-ContextCompactor -Executable $installedPath -Arguments @(
        "doctor",
        "--host",
        $AgentHost,
        "--project-root",
        $resolvedProjectRoot
    )
    $doctorDocument = ConvertFrom-ContextCompactorReport `
        -Lines $doctorReport `
        -ExpectedCommand "doctor"
    Assert-ContextCompactorReport `
        -Document $doctorDocument `
        -RequireExecutableHealthy

    foreach ($report in @($doctorDocument.reports)) {
        Write-InstallerSuccess (
            "{0}: Hook definition and executable are healthy." -f
            ([string]$report.host).ToUpperInvariant()
        )
    }

    Write-Host ""
    Write-InstallerSuccess "Installation completed."
    if ($AgentHost -eq "codex" -or $AgentHost -eq "all") {
        Write-InstallerAction (
            "Open /hooks in Codex and review or trust the context-compactor Hook."
        )
    }
} catch {
    Write-InstallerFailure $_.Exception.Message
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
