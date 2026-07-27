#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$ProjectRoot = (Get-Location).Path,

    [ValidateSet("codex", "claude", "all")]
    [string]$AgentHost = "codex",

    [string]$InstallDirectory,

    [string]$DockerImage = "golang:1.26"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

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

$sourceRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$goModulePath = Join-Path $sourceRoot "go.mod"
$commandPath = Join-Path $sourceRoot "cmd\context-compactor"
if (-not (Test-Path -LiteralPath $goModulePath -PathType Leaf) -or
    -not (Test-Path -LiteralPath $commandPath -PathType Container)) {
    throw "Run this installer from a context-compactor source checkout."
}

$resolvedProjectRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
if ([string]::IsNullOrWhiteSpace($InstallDirectory)) {
    $InstallDirectory = Join-Path $env:LOCALAPPDATA "context-compactor"
}
$resolvedInstallDirectory = [IO.Path]::GetFullPath($InstallDirectory)

$docker = Get-Command docker.exe -ErrorAction SilentlyContinue
if ($null -eq $docker) {
    $docker = Get-Command docker -ErrorAction SilentlyContinue
}
if ($null -eq $docker) {
    throw "Docker Desktop is required to build context-compactor."
}

$previousErrorActionPreference = $ErrorActionPreference
try {
    $ErrorActionPreference = "Continue"
    $dockerInfo = & $docker.Source info --format "{{.ServerVersion}}" 2>&1
    $dockerInfoExitCode = $LASTEXITCODE
} finally {
    $ErrorActionPreference = $previousErrorActionPreference
}
if ($dockerInfoExitCode -ne 0) {
    throw "Docker Desktop is installed but unavailable: $dockerInfo"
}

$distDirectory = Join-Path $sourceRoot "dist"
$buildName = "context-compactor-installer-$PID.exe"
$buildPath = Join-Path $distDirectory $buildName
$containerBuildPath = "/workspace/dist/$buildName"
$installedPath = $null
$copiedNewBinary = $false
$hookInstalled = $false

try {
    New-Item -ItemType Directory -Force -Path $distDirectory | Out-Null

    Write-Host "Building context-compactor with $DockerImage..."
    $dockerArguments = @(
        "run",
        "--rm",
        "-v",
        "${sourceRoot}:/workspace",
        "-w",
        "/workspace",
        "-e",
        "GOCACHE=/workspace/.cache/go-build",
        "-e",
        "GOMODCACHE=/workspace/.cache/go-mod",
        "-e",
        "GOOS=windows",
        "-e",
        "GOARCH=amd64",
        "-e",
        "CGO_ENABLED=0",
        $DockerImage,
        "go",
        "build",
        "-trimpath",
        "-ldflags=-s -w",
        "-o",
        $containerBuildPath,
        "./cmd/context-compactor"
    )
    & $docker.Source @dockerArguments
    if ($LASTEXITCODE -ne 0) {
        throw "Docker build failed with exit code $LASTEXITCODE."
    }
    if (-not (Test-Path -LiteralPath $buildPath -PathType Leaf)) {
        throw "Docker build completed without producing the expected executable."
    }

    $buildHash = (Get-FileHash -LiteralPath $buildPath -Algorithm SHA256).Hash.ToLowerInvariant()
    New-Item -ItemType Directory -Force -Path $resolvedInstallDirectory | Out-Null
    $installedPath = Join-Path $resolvedInstallDirectory (
        "context-compactor-{0}.exe" -f $buildHash.Substring(0, 12)
    )

    if (Test-Path -LiteralPath $installedPath -PathType Leaf) {
        $installedHash = (Get-FileHash -LiteralPath $installedPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($installedHash -ne $buildHash) {
            throw "The existing installed executable does not match its content-addressed name."
        }
    } else {
        Copy-Item -LiteralPath $buildPath -Destination $installedPath
        $copiedNewBinary = $true
    }

    $selfCheck = Invoke-ContextCompactor -Executable $installedPath -Arguments @("self-check")
    if (($selfCheck -join "`n") -ne '{"protocol":"context-compactor/v1","status":"ok"}') {
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
    if (Test-Path -LiteralPath $buildPath -PathType Leaf) {
        Remove-Item -LiteralPath $buildPath -Force
    }
}
