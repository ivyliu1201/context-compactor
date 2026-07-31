# Contributing

Contributions should be small, evidence-backed, and scoped to one change.
Before proposing behavior changes, read [SPEC.md](SPEC.md), [README.md](README.md),
and the relevant code and tests.

## Requirements

- Python 3.9+ standard library only.
- Windows installer work requires PowerShell 5.1+.
- No Go toolchain or native build is required for standard flow.

## Verification

- Main test command:
  `python -B -m unittest discover -s tests`
- Python syntax check (AST, equivalent to CI): parse all `*.py` files under
  `context_compactor`, `scripts`, and `tests`.

```sh
python -B -c "import ast, pathlib; paths=[*pathlib.Path('context_compactor').rglob('*.py'), *pathlib.Path('scripts').rglob('*.py'), *pathlib.Path('tests').rglob('*.py')]; [ast.parse(p.read_text(encoding='utf-8')) for p in paths]"
```

- PowerShell syntax check for installer and script files:

```powershell
$failures = @()
Get-ChildItem -LiteralPath scripts -Filter *.ps1 | ForEach-Object {
  $tokens = $null
  $errors = $null
  [System.Management.Automation.Language.Parser]::ParseFile(
    $_.FullName,
    [ref]$tokens,
    [ref]$errors
  ) | Out-Null
  if ($errors.Count -gt 0) {
    $failures += "$($_.Name): $($errors -join '; ')"
  }
}
if ($failures.Count -gt 0) {
  throw ($failures -join [Environment]::NewLine)
}
```

CI runs on Windows with Python 3.9 and Python 3.13. Reference:
[`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Privacy test fixtures

Do not include real secrets, full prompts, transcripts, or logs in fixtures.
Use bounded, synthetic redacted examples only.

## Pull request reports

Report what changed in behavior, relevant risks, and commands actually run.
Avoid unrelated refactors, dependency churn, and formatting-only edits.
