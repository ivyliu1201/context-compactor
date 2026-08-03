#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Manifest,

    [Parameter(Mandatory = $true)]
    [ValidateSet("codex", "claude")]
    [string]$AgentHost
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

try {
    $manifestPath = [IO.Path]::GetFullPath($Manifest)
    if (-not [IO.File]::Exists($manifestPath)) {
        throw "The project manifest is unavailable."
    }
    $document = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    if ($document.schema_version -ne 1) {
        throw "The project manifest version is unsupported."
    }

    if ($null -eq $document.hosts.PSObject.Properties[$AgentHost]) {
        throw "The requested Host is not installed for this project."
    }

    $python = [string]$document.python_interpreter
    if (-not [IO.File]::Exists($python)) {
        throw "The installed Python interpreter is unavailable."
    }

    $projectRoot = [string]$document.project_root
    if (-not [IO.Directory]::Exists($projectRoot)) {
        throw "The configured project root is unavailable."
    }

    $modelCommand = @($document.model_command)
    if ($modelCommand.Count -eq 0) {
        throw "The configured model command is unavailable."
    }

    $runtimeHost = if ($AgentHost -eq "codex") {
        "codex-cli"
    } else {
        "claude-code"
    }
    $arguments = @(
        "-m",
        "context_compactor",
        "hook",
        "--host",
        $runtimeHost,
        "--project-root",
        $projectRoot,
        "--model-command"
    )
    $arguments += $modelCommand

    $manifestEnvironmentName = "CONTEXT_COMPACTOR_PROJECT_MANIFEST"
    $previousManifest = [Environment]::GetEnvironmentVariable(
        $manifestEnvironmentName,
        "Process"
    )
    try {
        [Environment]::SetEnvironmentVariable(
            $manifestEnvironmentName,
            $manifestPath,
            "Process"
        )
        & $python @arguments
        $hookExitCode = $LASTEXITCODE
    } finally {
        [Environment]::SetEnvironmentVariable(
            $manifestEnvironmentName,
            $previousManifest,
            "Process"
        )
    }
    exit $hookExitCode
} catch {
    [Console]::Error.WriteLine("context-compactor: Hook launcher failed.")
    exit 1
}
