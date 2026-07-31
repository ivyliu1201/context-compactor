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

function Resolve-Executable {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Value
    )

    if (Test-Path -LiteralPath $Value -PathType Leaf) {
        return [IO.Path]::GetFullPath($Value)
    }
    $resolved = Get-Command $Value -ErrorAction SilentlyContinue
    if ($null -eq $resolved) {
        throw "Required executable is unavailable."
    }
    return $resolved.Source
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

$resolvedProjectRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
if ([string]::IsNullOrWhiteSpace($InstallDirectory)) {
    $InstallDirectory = Join-Path $env:LOCALAPPDATA "context-compactor"
}
$resolvedInstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)
$pythonPath = Resolve-Executable -Value $Python

$pythonVersion = & $pythonPath -c `
    "import sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}')"
if ($LASTEXITCODE -ne 0) {
    throw "Failed to inspect the configured Python interpreter."
}
try {
    $parsedPythonVersion = [Version]($pythonVersion | Select-Object -First 1)
} catch {
    throw "The configured Python interpreter returned an invalid version."
}
if ($parsedPythonVersion -lt [Version]"3.9") {
    throw "Python 3.9 or newer is required."
}

$modelCommand = @()
if ($Action -eq "install" -or $Action -eq "update") {
    if ($null -eq (Get-Command git -ErrorAction SilentlyContinue)) {
        throw "Git is required for source installation and update."
    }
    if ([string]::IsNullOrWhiteSpace($ModelCommandJson)) {
        throw "-ModelCommandJson is required for install and update."
    }
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
        $pythonPath,
        "--model-command"
    )
    $arguments += $modelCommand
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
