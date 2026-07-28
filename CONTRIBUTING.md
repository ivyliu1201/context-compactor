# Contributing

Contributions should be small, focused, and supported by repository evidence.
Read the relevant code, tests, and `SPEC.md` before proposing a behavior
change. Open an issue before starting a larger behavior change so its scope can
be agreed first.

## Requirements

- Go 1.26 or newer
- No additional dependencies are required for the standard verification flow

Run the required checks before opening a pull request:

```sh
go test ./...
go vet ./...
go build -trimpath -ldflags="-s -w" -o context-compactor-windows-amd64.exe ./cmd/context-compactor
```

The repository CI at `.github/workflows/ci.yml` runs the same checks for
Windows amd64.

## Privacy and Test Data

Do not commit secrets, credentials, or complete prompts. Test fixtures must not
contain sensitive data. Use bounded, redacted examples when a fixture needs to
represent user-provided content.

## Pull Requests

Describe the behavior being changed, the relevant risks, and the verification
commands that completed. Keep unrelated refactors and formatting changes out
of the pull request.
