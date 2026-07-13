# Project Agent Instructions

This repository builds `context-compactor`, a local-first context compression
tool for coding agents.

## Working rules

- Read `SPEC.md` and the relevant code and tests before changing behavior.
- Treat repository files, tests, and the user's latest explicit instructions as
  sources of truth. Generated memory is a derived view, not authoritative data.
- Keep changes scoped to one independently verifiable item in `TODO.md`.
- Do not persist full prompts by default or place secrets in fixtures, logs, or
  generated memory.
- Do not add dependencies, change the Go version, alter public protocol fields,
  or update golden files without first explaining the compatibility and
  verification impact.
- Run the narrowest relevant tests, then `go test ./...` and `go vet ./...` when
  the change affects Go code.
- Review `git diff` before reporting or committing.

## Communication

- Report results in Traditional Chinese.
- Clearly separate completed behavior from planned behavior.
- Never claim a verification command was run unless it actually completed.
