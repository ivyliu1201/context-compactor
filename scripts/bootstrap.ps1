#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$ProjectRoot = (Get-Location).Path,

    [ValidateSet("codex", "claude", "all")]
    [string]$AgentHost = "codex",

    [string]$InstallDirectory,

    [string]$Python = "python",

    [string]$ModelCommandJson
)

Set-StrictMode -Version Latest

function Write-BootstrapStatus {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Label,

        [Parameter(Mandatory = $true)]
        [string]$Message,

        [Parameter(Mandatory = $true)]
        [ConsoleColor]$Color
    )

    Write-Host ('[{0}] {1}' -f $Label, $Message) -ForegroundColor $Color
}

$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This bootstrap installer supports Windows only."
}

$resolvedProjectRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
$releaseApi =
    "https://api.github.com/repos/ivyliu1201/context-compactor/releases/latest"
$headers = @{
    Accept = "application/vnd.github+json"
    "User-Agent" = "context-compactor-bootstrap"
    "X-GitHub-Api-Version" = "2022-11-28"
}

[Net.ServicePointManager]::SecurityProtocol =
    [Net.ServicePointManager]::SecurityProtocol -bor
    [Net.SecurityProtocolType]::Tls12

Write-BootstrapStatus `
    -Label '1/4' `
    -Message 'Checking the latest stable release...' `
    -Color Cyan

$release = Invoke-RestMethod `
    -Uri $releaseApi `
    -Headers $headers `
    -Method Get
$requiredProperties = @("tag_name", "zipball_url", "draft", "prerelease")
foreach ($property in $requiredProperties) {
    if ($release.PSObject.Properties.Name -notcontains $property) {
        throw "GitHub latest-release metadata is incomplete."
    }
}
if ([bool]$release.draft -or [bool]$release.prerelease) {
    throw "GitHub latest-release metadata did not select a public stable release."
}

$tagName = [string]$release.tag_name
if ($tagName -notmatch
    '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$') {
    throw "GitHub latest-release tag is not a supported semantic version."
}

$downloadUri = [Uri]([string]$release.zipball_url)
$expectedPath =
    "/repos/ivyliu1201/context-compactor/zipball/$tagName"
if ($downloadUri.Scheme -ne "https" -or
    -not $downloadUri.Host.Equals(
        "api.github.com",
        [StringComparison]::OrdinalIgnoreCase
    ) -or
    -not $downloadUri.AbsolutePath.Equals(
        $expectedPath,
        [StringComparison]::Ordinal
    )) {
    throw "GitHub latest-release download URL is outside the expected repository."
}

$temporaryRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$workspace = Join-Path $temporaryRoot (
    "context-compactor-bootstrap-" + [Guid]::NewGuid().ToString("N")
)
$temporaryPrefix = $temporaryRoot.TrimEnd(
    [IO.Path]::DirectorySeparatorChar,
    [IO.Path]::AltDirectorySeparatorChar
) + [IO.Path]::DirectorySeparatorChar
if (-not $workspace.StartsWith(
    $temporaryPrefix,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "Refusing an unsafe bootstrap temporary path."
}

New-Item -ItemType Directory -Path $workspace | Out-Null
try {
    Write-BootstrapStatus `
        -Label '2/4' `
        -Message ('Downloading context-compactor {0}...' -f $tagName) `
        -Color Cyan
    $archivePath = Join-Path $workspace "release.zip"
    $extractPath = Join-Path $workspace "source"
    Invoke-WebRequest `
        -Uri $downloadUri.AbsoluteUri `
        -Headers $headers `
        -UseBasicParsing `
        -OutFile $archivePath
    Write-BootstrapStatus `
        -Label '3/4' `
        -Message 'Verifying the release package...' `
        -Color Cyan
    Expand-Archive -LiteralPath $archivePath -DestinationPath $extractPath

    $sourceRoots = @(
        Get-ChildItem -LiteralPath $extractPath -Directory | Where-Object {
            Test-Path -LiteralPath (
                Join-Path $_.FullName "scripts\install.ps1"
            ) -PathType Leaf
        }
    )
    if ($sourceRoots.Count -ne 1) {
        throw "Downloaded release archive has an unexpected source layout."
    }

    $sourceRoot = $sourceRoots[0].FullName
    $requiredSourcePaths = @(
        (Join-Path $sourceRoot "context_compactor\__init__.py"),
        (Join-Path $sourceRoot "requirements.lock"),
        (Join-Path $sourceRoot "scripts\context-compactor-hook.ps1")
    )
    foreach ($path in $requiredSourcePaths) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Downloaded release source is incomplete."
        }
    }

    $versionSource = Get-Content -Raw -Encoding UTF8 `
        (Join-Path $sourceRoot "context_compactor\__init__.py")
    $versionMatch = [regex]::Match(
        $versionSource,
        '__version__\s*=\s*"([^"]+)"'
    )
    if (-not $versionMatch.Success -or
        "v$($versionMatch.Groups[1].Value)" -ne $tagName) {
        throw "Downloaded release version does not match its GitHub tag."
    }

    $installerArguments = @{
        Action = "install"
        ProjectRoot = $resolvedProjectRoot
        AgentHost = $AgentHost
        Python = $Python
    }
    if (-not [string]::IsNullOrWhiteSpace($InstallDirectory)) {
        $installerArguments.InstallDirectory = $InstallDirectory
    }
    if (-not [string]::IsNullOrWhiteSpace($ModelCommandJson)) {
        $installerArguments.ModelCommandJson = $ModelCommandJson
    }

    Write-BootstrapStatus `
        -Label '4/4' `
        -Message ('Installing for {0}...' -f $AgentHost) `
        -Color Cyan
    $installerOutput = @(
        & (Join-Path $sourceRoot "scripts\install.ps1") @installerArguments
    )
    $installerReport = ConvertFrom-Json -InputObject (
        $installerOutput -join [Environment]::NewLine
    )
    if ($null -eq $installerReport) {
        throw 'Installer did not return a result.'
    }

    $requiredReportProperties = @(
        'ok',
        'installed',
        'source_created',
        'source_changed',
        'install_root'
    )
    $missingReportProperties = @(
        $requiredReportProperties | Where-Object {
            $installerReport.PSObject.Properties.Name -notcontains $_
        }
    )
    if ($missingReportProperties.Count -gt 0) {
        throw 'Installer returned an incomplete result.'
    }
    if (-not [bool]$installerReport.ok -or
        -not [bool]$installerReport.installed) {
        throw 'Installer did not complete successfully.'
    }

    if ([bool]$installerReport.source_created) {
        $installationResult = 'Installed'
    } elseif ([bool]$installerReport.source_changed) {
        $installationResult = 'Updated'
    } else {
        $installationResult = 'Already up to date'
    }
    $installedLocation = [string]$installerReport.install_root
} finally {
    if (Test-Path -LiteralPath $workspace) {
        Remove-Item -LiteralPath $workspace -Recurse -Force
    }
}

Write-Host
Write-BootstrapStatus `
    -Label 'OK' `
    -Message ('context-compactor {0} is ready.' -f $tagName) `
    -Color Green
Write-BootstrapStatus `
    -Label 'RESULT' `
    -Message $installationResult `
    -Color Green
Write-Host ('Project: {0}' -f $resolvedProjectRoot)
Write-Host ('Install location: {0}' -f $installedLocation)
Write-BootstrapStatus `
    -Label 'NEXT' `
    -Message 'Start your coding agent in this project and approve the Hook if prompted.' `
    -Color Yellow
