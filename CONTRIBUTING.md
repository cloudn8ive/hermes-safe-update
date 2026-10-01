# Contributing

Bug reports, testers on macOS and Linux, and fixes are welcome. This is a
spare-time project, so a few rules keep it manageable.

## Issue first

Open an issue and agree on the approach before you send a pull request.
Typo and doc fixes can skip this. A pull request without an agreed issue may
be closed without review; that saves you wasted work.

## Before you open an issue

- Use the issue template. Say your OS, how Hermes is installed (source
  checkout, packaged app, other), the tool version and the exit code.
- Paste only the relevant lines of `logs/safe-update.log`. Remove anything
  personal first: user names in paths, machine names, session titles. The
  log already shortens your home folder to `~`, but check anyway.
- macOS and Linux are untested. A report that says "it worked" or "it broke
  here" on those systems is useful.

## Building and testing

You need Go 1.26 or newer (`go.mod` also pins the tested toolchain).

```
go build ./cmd/hermes-safe-update
go test ./... -count=1 -shuffle=on
gofmt -s -l .                 # must print nothing
go vet ./...
GOOS=linux go vet ./... && GOOS=darwin go vet ./...
scripts/build-all.sh dev      # all six targets plus SHA256SUMS in dist/
```

CI runs `go test` on Windows, macOS and Linux (the race detector on Linux
only, because it needs cgo; releases are built with `CGO_ENABLED=0`). A
separate lint job on Linux runs gofmt, `go mod tidy` (no diff), staticcheck,
govulncheck and `go vet` for all three OSes.

`go test -tags electron ./internal/settings` runs the migrator against the
real Electron bundled with a Hermes checkout, on synthetic data in a temp
folder (`HSU_ELECTRON` points it at an Electron binary).

## Rules for changes

- Read [docs/design.md](docs/design.md) first. Business logic goes through
  the interfaces in `internal/platform`; `runtime.GOOS` is only allowed at
  the edge.
- Fixtures are synthetic. Never commit real logs, session ids or titles,
  settings values, user paths or anything copied from a real Hermes install.
- Tests do not touch a real Hermes install. Use `t.TempDir()` and the fakes.
- Words that Hermes prints and the tool matches live in one file,
  `internal/hermes/contracts.go`, with fixture tests.
- Keep Windows-only code in `*_windows.go` files with a stub for the other
  systems, so every target compiles.
- Do not claim something works on macOS or Linux unless someone ran it there.
- Keep each pull request to the one agreed change, with a test that fails
  without it (or say why it cannot have one).

## AI-assisted contributions

This project is itself built with AI assistance, so AI-assisted
contributions are fine, but you must say so (the issue forms and the pull
request template ask). You must have run or checked the change yourself, or
reproduced the bug yourself. Unchecked AI output is closed.

## Pull requests

Link the issue with `Fixes #123`. Describe what changed and how you checked
it. Say which OS you ran it on.
By contributing you agree that your contribution is licensed under the MIT
License of this project.

## Conduct

The account-wide [Code of Conduct](https://github.com/cloudn8ive/.github/blob/main/CODE_OF_CONDUCT.md)
applies. Short version: be civil, be specific, stay on topic.
