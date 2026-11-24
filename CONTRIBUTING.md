# Contributing

Thanks for helping. A few rules keep the sensor trustworthy.

## Build and test

```bash
go test -race ./...                       # unit, golden, chaos and install tests
go vet ./... && staticcheck ./...
claude plugin validate --strict ./plugins/claude-code
claude plugin validate --strict .
cd extensions/vscode && npm ci && npm run typecheck && npm run test:unit
```

Golden files (`testdata/golden/`) record the exact output for every fixture.
- After an intentional behaviour change, regenerate them with `go test ./internal/hook -run TestGolden -update`, and review the diff line by line.
- A golden diff you cannot explain is a bug.

## Rules

- **Every path exits 0 with valid output.** Copilot denies on a non-zero exit; Cursor blocks on invalid JSON.
- **Nothing secret leaves the machine.** New fields go through `internal/redact` before the spool, and PRIVACY.md lists every field sent. A PR that sends a new field updates PRIVACY.md in the same change.
- **No claim without a test.** Do not write that something blocks, detects or prevents unless a test shows it. Do not say something cannot be bypassed: say which path it goes through and link the gaps in LIMITS.md. Do not claim the sensor stops prompt injection, do not claim any compliance standard or third-party attestation, and never give a number that was not measured.
- **New fixtures:** prefer payloads captured from the real client, sanitised (paths, ids, emails, prompts replaced). Mark payloads built from documentation with `"_source": "docs: …"`.
- No secrets in the repo, including examples. CI runs gitleaks.

## Commit sign-off

By contributing you agree your work is licensed under Apache-2.0.
