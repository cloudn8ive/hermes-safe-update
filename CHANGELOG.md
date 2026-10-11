# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/). Dates are UTC.

## [Unreleased]

## [0.1.2] - 2026-10-11

### Changed

- Release binaries are built with Go 1.26.9 (the `toolchain` line in `go.mod`
  moved from 1.27.1). `govulncheck` reports eight Go standard library
  vulnerabilities (`net/http`, `net/textproto`, `crypto/tls`) against Go
  1.27.1, and the code reaches them; Go 1.26.9 has the fixes. Go 1.27.2 has
  them too, but staticcheck 2026.2.1 cannot read its export data yet, so CI
  would fail on it. To rebuild a release byte for byte, use that toolchain
  (`GOTOOLCHAIN=go1.26.9`).

### Fixed

- The progress display no longer moves backwards when `hermes update` runs
  its parts in a different order than the tool's step list. Newer Hermes
  builds resolve and install Python dependencies after the desktop build
  (older ones do it before the builds); the footer used to jump from
  "4/5 Build" back to "3/5 Update" and the step counter went back with it.
  A step that starts after a later one has already run is now moved up
  behind the furthest step reached and joins its stage, so the banner, the
  stage line, the step counter and the bar only go forward. Both orders work.
  The new line `Resolving Python dependencies…` also starts that step.
- The pre-flight line "python deps: unchanged" could be wrong: Hermes'
  package manager re-resolves and reinstalls the Python environment when
  `pm/lock.json` or the manifest of a plugin that declares Python packages
  (`plugin.yaml`/`plugin.yml`/`plugin.json`, `pyproject.toml` under
  `plugins/`) changes, not only when `uv.lock` or `pyproject.toml` does. One
  real update (a bundled plugin moved out of core and was installed from the
  catalog during the update) locked and synced the whole environment although
  neither file had changed. All three prediction paths (local mirror and git,
  GitHub API file list) now treat those files as "changed, will be
  reinstalled". A changed manifest of a plugin you never enabled gives a
  harmless false "changed" (only the estimate is longer).
- `hermes update` prints `dependency generation cleanup skipped: [WinError 5]
  Access is denied: '…x.pyd'` when a still-running old process holds a file
  of an old dependency set. It is harmless (the update is already committed)
  and the tool's own cleanup step removes the set afterwards, so the screen now
  shows one calm line instead: "Hermes left an old dependency set in place (a
  file was in use); the cleanup step at the end removes it". The log keeps the
  raw line. If that set is still on disk after the cleanup step (it found
  nothing, failed, or did not run because the update was not verified), the
  cleanup step warns and the summary's Cleanup row says so in amber. Other
  reasons for a skipped cleanup are shown as Hermes printed them. This also
  replaces a screen rewrite of this line in the renderer that never matched,
  because the path is a Python repr with doubled backslashes.
- A part of `hermes update` that did not finish (for example a failed web UI
  build, reported as `⚠ Update follow-up 'build' did not finish: …`) was only
  visible in the scrollback: `hermes update` exits 0 after it, and the summary
  said "Hermes is updated and verified." The summary now says "…, but 1 step
  did not finish." and lists each one as an amber "Not finished" row with
  Hermes' retry advice. The result stays OK and the exit code 0, because the
  update itself is verified.
- When a failed build echoes its whole inline-code command (a ~100-line
  Python script, one output line per source line), the screen now shows one
  line, "`⚠ web UI build failed (command details in the log)`". The build
  errors above it stay visible, and the log keeps every raw line.
- Summary and status rows no longer repeat their key in the value
  ("Settings: settings: …", "Start tile: Start tile: …").

## [0.1.1] - 2026-10-03

### Changed

- README demo video now plays inline.

### Fixed

- `--check` was described as read-only and "changes nothing", but it can
  refresh the local mirror and fetch new commits and `v*` tags into the
  checkout's git data (like `git fetch`). That is kept, because it makes the
  check accurate and the later update faster; the README, how-it-works, design
  notes, `--help`, the `.cmd` shims and the summary card now say so exactly
  ("Hermes was not touched"). A new sandbox test pins that `--check` never moves
  HEAD or the branch and never touches the index or the working tree.
- The checkout's git calls now pass `--no-optional-locks`, so `git status` in
  a check no longer rewrites `.git/index` (a stat-cache refresh) when file
  timestamps differ from the cache.

- Plain output: the prompt countdown goroutine is now stopped and waited for
  before `Prompt` returns, so a countdown line (`  30s`) can no longer be
  printed after the prompt was answered, in the middle of the next stage. The
  `-race` CI step on Linux caught this as an intermittent extra line in the
  plain session golden; the golden test no longer depends on that timing.

## [0.1.0] - 2026-10-01

First release. Replaces a set of personal Python scripts with one Go binary.

### Added

- `hermes-safe-update` (`run`) and `--check`: wait for idle sessions, close
  the desktop app gracefully, run `hermes update --yes --keep-stash --branch
  main`, verify, migrate desktop settings, reopen Hermes.
- Update check through a local mirror, the GitHub API, or `hermes update
  --check`.
- Handling of Hermes' Windows gateway-pause failure (stop the gateway, retry
  once, restart it).
- Desktop settings migration, a workaround for settings lost on the Hermes
  builds that loaded the renderer from `http://127.0.0.1:47891` instead of
  `file://` (2026-10-01; reverted upstream, #130635 closed): `migrate-settings`,
  `backup-settings`, `list-backups`, `revert-settings`, with backups,
  verification and automatic rollback.
- Settings stage follows upstream: builds without `renderer-server.ts` (the
  renderer loads from `file://` again) are detected from `main.ts` and
  migrate `http://127.0.0.1:<port>` into `file://`. When the origin cannot be
  detected, the stage still takes a settings backup and the warning names it
  and the `migrate-settings --origin <url>` command.
- The mirror writes `logs/update-mirror.log` (`<ISO time> <message>`), as
  documented, besides `safe-update.log`.
- Optional local git mirror: `mirror setup`, `mirror refresh`,
  `mirror status`.
- Dependency-generation cleanup and cua-driver refresh after verified
  updates (Windows).
- Hooks (`pre-close`, `post-update`, `post-relaunch`) and a strict YAML
  config (`safe-update.yaml`) with every key documented.
- Progress footer with time estimates learned from your own runs; plain
  output for pipes and scheduled tasks.
- Step-aside for installs it does not manage (macOS app, MSIX, Docker, Nix,
  AppImage/deb/rpm): exit 5 with advice.
- `scripts/hermes-safe-update.cmd` and `scripts/hermes-update-check.cmd`
  shims. They look next to themselves, then in
  `%LOCALAPPDATA%\Programs\hermes-safe-update`, then on PATH.
- Release builds for windows, darwin and linux on amd64 and arm64 with a
  `SHA256SUMS` file.

### Behaviour worth knowing

- A pre-flight stop (another update running, low disk, update check failed)
  exits 4.
- The busy-session check and confirmation also run when no desktop app is
  open but a gateway, backend or kernel is about to be stopped.
- The idle wait counts real elapsed time, and prompts accept only the
  intended keys.
- Timing history is saved in plain mode too, and all state files are written
  atomically.
- Personal post-update chores (restoring a shortcut, restarting a helper)
  are not built in; put them in a `post-update` hook.
- A `hermes update` timeout stops the whole process tree.
- A process is only force-stopped if it still has the start time it had when it
  was identified, so a reused pid is never killed. This check needs the process
  start time, which Windows and Linux report and macOS does not, so it (and the
  stale-parent check on Unix) does not apply on macOS.
- An update started from inside a Hermes chat (the updater has a gateway,
  backend or kernel of this install as an ancestor, or `HERMES_SESSION_ID` is
  set, or `HERMES_AGENT=true`, which still catches a broken parent chain) stops
  before closing anything and exits 4; `--check` is unaffected.

### Platform status

- Windows 10 amd64: unit-tested; one real unattended update (started by a scheduled task) ran and verified on Windows 10 22H2 amd64, and the Start-menu and Ctrl+K launches were tried. The settings migration was run in a sandbox on a copy of a real desktop settings store and in an automated test on synthetic data, both with the real Electron. On the real update the settings step failed, because that Hermes range removed renderer-server.ts; this was fixed afterwards (file:// detection, backup when detection fails) and covered by unit tests and a read-only dry run, but no real settings migration has been run since the fix.
- Windows 11 amd64: expected to work; not yet tested.
- Windows arm64: builds, not run on arm64 hardware.
- macOS and Linux: **untested, proceed at your own risk.** They are
  cross-compiled and vetted; unit tests so far run on Windows only.

Binaries are not code-signed.

[Unreleased]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/cloudn8ive/hermes-safe-update/releases/tag/v0.1.0
