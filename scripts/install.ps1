#requires -Version 5.1

[CmdletBinding()]
param(
    [ValidateSet("install", "update", "uninstall", "status", "doctor")]
    [string]$Action = "install",

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
        "The installer tried the configured command and the standard Windows " +
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
        Resolve-Application -Candidates @("codex.cmd", "codex.exe", "codex") -DisplayName "Codex CLI" | Out-Null
        return
    }

    $hostPath = Resolve-Application -Candidates @("claude.cmd", "claude.exe", "claude") -DisplayName "Claude CLI"
    Assert-ApplicationRuns -Path $hostPath -Arguments @("--version") -FailureMessage "Claude CLI is installed but could not run."
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This installer supports Windows only."
}

if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA) -and
    [string]::IsNullOrWhiteSpace($InstallDirectory)) {
    throw "LOCALAPPDATA is unavailable; specify -InstallDirectory."
}

$sourceRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$packagePath = Join-Path $sourceRoot "context_compactor"
$lockPath = Join-Path $sourceRoot "requirements.lock"
$hookWrapperPath = Join-Path $sourceRoot "scripts\context-compactor-hook.ps1"
if (-not (Test-Path -LiteralPath $packagePath -PathType Container) -or
    -not (Test-Path -LiteralPath $lockPath -PathType Leaf) -or
    -not (Test-Path -LiteralPath $hookWrapperPath -PathType Leaf)) {
    throw "Run this installer from a context-compactor source checkout."
}

try {
    $resolvedProjectRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
} catch {
    throw "Project root does not exist or cannot be accessed: $ProjectRoot"
}
if ([string]::IsNullOrWhiteSpace($InstallDirectory)) {
    $InstallDirectory = Join-Path $env:LOCALAPPDATA "context-compactor"
}
$resolvedInstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)
$allowPythonFallback = -not $PSBoundParameters.ContainsKey("Python")
$pythonPath = Resolve-PythonInterpreter -ConfiguredPython $Python -AllowFallback:$allowPythonFallback

$modelCommand = @()
if ($Action -eq "install" -or $Action -eq "update") {
    $gitPath = Resolve-Application -Candidates @("git.exe", "git") -DisplayName "Git"
    Assert-ApplicationRuns -Path $gitPath -Arguments @("--version") -FailureMessage "Git is installed but could not run."

    $selectedHosts = @($AgentHost)
    if ($AgentHost -eq "all") {
        $selectedHosts = @("codex", "claude")
    }
    foreach ($selectedHost in $selectedHosts) {
        Assert-AgentHostReady -HostName $selectedHost
    }

    if (-not [string]::IsNullOrWhiteSpace($ModelCommandJson)) {
        $trimmedModelCommandJson = $ModelCommandJson.Trim()
        if (-not $trimmedModelCommandJson.StartsWith("[") -or
            -not $trimmedModelCommandJson.EndsWith("]")) {
            throw "-ModelCommandJson must be a JSON array of command arguments."
        }
        try {
            $parsedModelCommand = ConvertFrom-Json -InputObject $ModelCommandJson
            $modelCommand = @($parsedModelCommand | ForEach-Object { $_ })
        } catch {
            throw "-ModelCommandJson must be a JSON array of command arguments."
        }
        if ($modelCommand.Count -eq 0) {
            throw "-ModelCommandJson must contain at least one command argument."
        }
        foreach ($argument in $modelCommand) {
            if ($argument -isnot [string] -or
                [string]::IsNullOrWhiteSpace([string]$argument)) {
                throw "-ModelCommandJson must contain only non-empty strings."
            }
        }
    }
}

$arguments = @(
    "-m",
    "context_compactor",
    $Action,
    "--project-root",
    $resolvedProjectRoot,
    "--install-root",
    $resolvedInstallDirectory,
    "--host",
    $AgentHost
)
if ($Action -eq "install" -or $Action -eq "update") {
    $arguments += @(
        "--source-root",
        $sourceRoot,
        "--python",
        $pythonPath
    )
    if ($modelCommand.Count -gt 0) {
        $arguments += "--model-command"
        $arguments += $modelCommand
    }
}

Push-Location $sourceRoot
try {
    & $pythonPath @arguments
    $exitCode = $LASTEXITCODE
} finally {
    Pop-Location
}
if ($exitCode -ne 0) {
    throw "context-compactor $Action failed with exit code $exitCode."
}
