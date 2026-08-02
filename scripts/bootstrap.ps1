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
    $archivePath = Join-Path $workspace "release.zip"
    $extractPath = Join-Path $workspace "source"
    Invoke-WebRequest `
        -Uri $downloadUri.AbsoluteUri `
        -Headers $headers `
        -UseBasicParsing `
        -OutFile $archivePath
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

    & (Join-Path $sourceRoot "scripts\install.ps1") @installerArguments
} finally {
    if (Test-Path -LiteralPath $workspace) {
        Remove-Item -LiteralPath $workspace -Recurse -Force
    }
}
