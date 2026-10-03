# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/). Dates are UTC.

## [Unreleased]

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

[Unreleased]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/cloudn8ive/hermes-safe-update/releases/tag/v0.1.0
