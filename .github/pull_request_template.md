Fixes #

## What and why

<!-- What does this change, and why? Keep it to the one agreed change. -->

## How I checked it

- [ ] `go test ./... -count=1 -shuffle=on`
- [ ] `gofmt -s -l .` prints nothing, `go vet ./...` is clean
- [ ] OS I ran it on:

## Checklist

- [ ] An issue exists and we agreed on the approach (typo/doc fixes excepted).
- [ ] I ran it myself and it works.
- [ ] AI tools helped with this change: yes / no (delete one). If yes, I
      reviewed every line and can explain it.
- [ ] New tests fail without the change
- [ ] Fixtures are synthetic (no real logs, paths, names, session ids or settings values)
- [ ] No claim of macOS or Linux support that nobody ran
- [ ] Docs and `safe-update.example.yaml` updated if behaviour or config changed
