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

function Resolve-Application {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Candidates,

        [Parameter(Mandatory = $true)]
        [string]$DisplayName
    )

    foreach ($candidate in $Candidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) {
            continue
        }
        $commands = @(
            Get-Command -Name $candidate -CommandType Application -ErrorAction SilentlyContinue
        )
        foreach ($command in $commands) {
            $source = [string]$command.Source
            if (-not [string]::IsNullOrWhiteSpace($source)) {
                return $source
            }
        }
    }

    throw "$DisplayName is unavailable or is not an executable application."
}

function Resolve-PythonInterpreter {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ConfiguredPython,

        [switch]$AllowFallback
    )

    if ($AllowFallback) {
        $candidateNames = @("py", $ConfiguredPython, "python3")
    } else {
        $candidateNames = @($ConfiguredPython)
    }
    $candidateNames = @(
        $candidateNames |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
            Select-Object -Unique
    )
    $probeCode = (
        "import json, sys; " +
        "print(json.dumps({'executable': sys.executable, " +
        "'version': [sys.version_info[0], sys.version_info[1]]}))"
    )

    foreach ($candidateName in $candidateNames) {
        try {
            $commandPath = Resolve-Application -Candidates @($candidateName) -DisplayName "Python"
            $launcherArguments = @()
            $leafName = [IO.Path]::GetFileName($commandPath)
            if ($leafName -ieq "py" -or $leafName -ieq "py.exe") {
                $launcherArguments += "-3"
            }

            $probeOutput = @(
                & $commandPath @launcherArguments -c $probeCode 2>$null
            )
            if ($LASTEXITCODE -ne 0) {
                continue
            }
            $probeLine = (
                $probeOutput |
                    Where-Object {
                        -not [string]::IsNullOrWhiteSpace([string]$_)
                    } |
                    Select-Object -Last 1
            )
            $probe = ConvertFrom-Json -InputObject $probeLine
            $versionParts = @($probe.version)
            if ($versionParts.Count -ne 2) {
                continue
            }
            $version = [Version](
                "{0}.{1}" -f $versionParts[0], $versionParts[1]
            )
            if ($version -lt [Version]"3.9") {
                continue
            }

            $executable = [IO.Path]::GetFullPath(
                [string]$probe.executable
            )
            if (-not [IO.File]::Exists($executable)) {
                continue
            }
            & $executable -c "import ensurepip, venv" 2>$null
            if ($LASTEXITCODE -ne 0) {
                continue
            }
            return $executable
        } catch {
            continue
        }
    }

    throw (
        "Python 3.9 or newer with venv and pip is required. " +
        "The bootstrap tried the configured command and the standard Windows " +
        "launchers. Install Python, enable its App Execution Alias if needed, " +
        "reopen PowerShell, and retry."
    )
}

function Assert-ApplicationRuns {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string[]]$Arguments,

        [Parameter(Mandatory = $true)]
        [string]$FailureMessage
    )

    & $Path @Arguments *> $null
    if ($LASTEXITCODE -ne 0) {
        throw $FailureMessage
    }
}

function Assert-AgentHostReady {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet("codex", "claude")]
        [string]$HostName
    )

    if ($HostName -eq "codex") {
        $hostPath = Resolve-Application -Candidates @("codex.cmd", "codex.exe", "codex") -DisplayName "Codex CLI"
        $loginReady = $false
        try {
            & $hostPath login status *> $null
            $loginReady = $LASTEXITCODE -eq 0
        } catch {
            $loginReady = $false
        }
        if (-not $loginReady) {
            Write-BootstrapStatus -Label 'WARN' -Message (
                "Codex CLI was found, but its login status could not be " +
                "confirmed. Installation will continue; run 'codex login' " +
                "before using background context updates."
            ) -Color Yellow
        }
        return
    }

    $hostPath = Resolve-Application -Candidates @("claude.cmd", "claude.exe", "claude") -DisplayName "Claude CLI"
    Assert-ApplicationRuns -Path $hostPath -Arguments @("--version") -FailureMessage "Claude CLI is installed but could not run."
}

$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This bootstrap installer supports Windows only."
}

Write-BootstrapStatus -Label 'CHECK' -Message ('Validating Python, Git, and {0}...' -f $AgentHost) -Color Cyan

try {
    $resolvedProjectRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
} catch {
    throw "Project root does not exist or cannot be accessed: $ProjectRoot"
}
$expectedInstallDirectory = $null
if (-not [string]::IsNullOrWhiteSpace($InstallDirectory)) {
    $expectedInstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)
} elseif (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
    $expectedInstallDirectory = [IO.Path]::GetFullPath(
        (Join-Path $env:LOCALAPPDATA "context-compactor")
    )
} else {
    throw "LOCALAPPDATA is unavailable; specify -InstallDirectory."
}

try {
    $temporaryRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
} catch {
    throw "The Windows temporary directory is unavailable."
}
if (-not [IO.Directory]::Exists($temporaryRoot)) {
    throw "The Windows temporary directory does not exist: $temporaryRoot"
}

$allowPythonFallback = -not $PSBoundParameters.ContainsKey("Python")
$pythonPath = Resolve-PythonInterpreter -ConfiguredPython $Python -AllowFallback:$allowPythonFallback
$gitPath = Resolve-Application -Candidates @("git.exe", "git") -DisplayName "Git"
Assert-ApplicationRuns -Path $gitPath -Arguments @("--version") -FailureMessage "Git is installed but could not run."
$selectedHosts = @($AgentHost)
if ($AgentHost -eq "all") {
    $selectedHosts = @("codex", "claude")
}
foreach ($selectedHost in $selectedHosts) {
    Assert-AgentHostReady -HostName $selectedHost
}

Write-BootstrapStatus -Label 'OK' -Message 'Prerequisites are ready.' -Color Green

$installationManifestExisted = (
    $null -ne $expectedInstallDirectory -and
    (Test-Path -LiteralPath (
        Join-Path $expectedInstallDirectory "install.json"
    ) -PathType Leaf)
)
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

try {
    $release = Invoke-RestMethod `
        -Uri $releaseApi `
        -Headers $headers `
        -Method Get `
        -TimeoutSec 30
} catch {
    throw (
        "Unable to reach the GitHub release API. Check internet access, DNS, " +
        "proxy settings, TLS inspection or certificates, GitHub rate limits, " +
        "and whether api.github.com is allowed."
    )
}
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

$workspace = Join-Path $temporaryRoot (
    "cc-" + [Guid]::NewGuid().ToString("N")
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
    $extractPath = Join-Path $workspace "s"
    try {
        Invoke-WebRequest `
            -Uri $downloadUri.AbsoluteUri `
            -Headers $headers `
            -UseBasicParsing `
            -OutFile $archivePath `
            -TimeoutSec 120
    } catch {
        throw (
            "Unable to download the GitHub release archive. Check internet " +
            "access, proxy settings, TLS inspection or certificates, and " +
            "whether api.github.com and codeload.github.com are allowed."
        )
    }
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
        Python = $pythonPath
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

    if (-not $installationManifestExisted) {
        $installationResult = 'Installed'
    } elseif ([bool]$installerReport.source_changed) {
        $installationResult = 'Updated'
    } else {
        $installationResult = 'Already up to date'
    }
    $installedLocation = [string]$installerReport.install_root
} finally {
    if (Test-Path -LiteralPath $workspace) {
        try {
            Remove-Item -LiteralPath $workspace -Recurse -Force
        } catch {
            Write-Warning (
                "Temporary files could not be removed: $workspace. " +
                "The installer did not retry with a broader delete."
            )
        }
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
