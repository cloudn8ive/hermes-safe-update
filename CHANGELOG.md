# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/). Dates are UTC.

## [Unreleased]

## [0.3.0] - 2026-10-15

### Changed

- **The default is now `update.channel: follow`.** Safe Update used to pass
  `--branch main` on every run, which silently moved a user who had chosen
  Hermes' `stable` channel (the latest published release) onto `main`, and
  it ignored Settings → About. Now it follows the channel Hermes uses for the
  install: if that is `stable`, a run updates to the latest release (a
  checkout that is already newer waits for the next one only if you never
  chose stable yourself; if you did, the update would move it back and the
  rollback guard asks first); if it is
  `main`, or your Hermes is older than v0.21.6 and has no channels, a run
  is exactly what it was before (`hermes update --yes --keep-stash --branch main`).
  If you want main whatever Hermes says, set `update.channel: main` in
  `safe-update.yaml` (or pass `--channel main` for one run). `stable` pins
  the latest release the same way.
- On Windows the update marker is written in Hermes' format v2 (with this
  process' creation time), so a reused pid can't fake it and it never ages
  out mid-update; holders are checked by creation time and `delegate:` lines
  are honoured. Other systems keep v1.

### Added

- New setting `update.channel` (`follow` | `stable` | `main`, default
  `follow`) and a `--channel` flag for `run` and `check` that beats it for one
  run. `follow` reads Hermes' choice offline and read-only (the install id
  from `hermes update --install-id`, the record in Hermes' `config.yaml`, else
  stable for a checkout of the official GitHub repository and main for any
  other origin). Safe Update never writes Hermes' setting during a run.
- New command `hermes-safe-update channel`: shows the channel Hermes uses for
  this install and where that comes from, what `update.channel` is set to,
  and what the next run will do, with a short explanation of stable and main.
  No network; it never asks the untested-platform question.
- New command `hermes-safe-update channel set stable|main`: switches the
  channel through Hermes' own `hermes update --set-channel` (Safe Update never
  edits Hermes' configuration itself), prints Hermes' confirmation and the new
  state. It refuses any other name, and a Hermes that cannot switch (exit 4,
  with the reason). Before switching to stable it runs a read-only release
  check; if the checkout is newer than the latest release, the next update
  would move Hermes back, so it says so and asks for a `Y` (`--yes` goes
  ahead; `--unattended` or no terminal refuses; a check that cannot run still
  asks). If `safe-update.yaml` forces a channel it says Safe Update keeps using it.
- A new rollback guard: a run that would move the checkout back to an older
  release stops before anything is closed. Attended, it asks for an explicit
  `Y` that names both versions; `--yes` does not skip it, and any other key or
  no answer is exit 4. `--unattended` never rolls back (exit 4, and the
  message names the command to do it by hand).
- The rollback guard asks git, not only Hermes' wording: Hermes v0.21.6 and
  v0.21.7 print "Selected release available" for a release that is older than
  the checkout and then move it back without asking. Before anything is
  closed, Safe Update checks `git merge-base --is-ancestor <release> HEAD`;
  an ancestor is a rollback and goes through the guard (with the number of
  commits). A release that is not an ancestor (or not in the checkout at all)
  is a forward update in a full clone (also a partial one); a shallow clone
  cannot prove that either way, so an attended run asks ("might move Hermes
  BACK") and an unattended run stops, unless GitHub's compare (when a mirror is
  set up) says the release is not behind (ahead, identical or diverged). If the
  installed commit is unknown, Safe Update says so in neutral words instead of
  claiming a move back, and asks or stops the same way.
- `--force` no longer skips the stop when the release check fails or times
  out: without a readable check Safe Update cannot tell a rollback from a
  forward update, so it stops (exit 4) with or without `--force`.
- A slow Hermes start-up no longer reads as "this Hermes has no channels":
  the `update --help` and `--install-id` probes get 45 s (they were 15 s) and
  run side by side. If the help still cannot be read, the checkout's own
  files answer (`hermes_cli/update_channel.py` exists and
  `hermes_cli/subcommands/update.py` names `--channel`); only when neither
  shows channels is the Hermes treated as one from before channels. If
  `--install-id` fails, the channel record whose `path` is this checkout is
  used. The log says whether `help`, `source files` or `none` decided.
- `hermes-safe-update channel` and `channel set` read the help and the
  install id at the same time (one Hermes start-up of waiting instead of two),
  and a switch no longer asks the help a second time.
- The update marker is re-read right before each refresh write, so a `delegate:`
  (or any other) line Hermes appended earlier is kept; only a line appended in
  the instant between that re-read and the write can still be lost (a very
  small window).
- `update.channel: stable` on a Hermes without release channels stops in the
  pre-flight (exit 4) with the reason, before anything is closed.
- On a release channel, Hermes' own `hermes update --check` is the judge:
  "up to date" and "no newer release ... waits for the next release" end the
  run with exit 0 and name the release; `Could not resolve the stable source
  channel` is a pre-flight stop (exit 4) with Hermes' reason; wording this tool
  does not recognise is a warning and a stop, never silent success. Main's
  mirror and GitHub comparison are skipped there.
- Summary: a new `Channel` row (after `Version`, and in the check card) names
  the update channel and why. On release channels the `Available`/`updates`
  rows name the release (`release v0.22.1 available, 7 commits`), and the
  npm/Python-deps prediction is made against the release commit, not
  `origin/main`. `Version` counts commits only when the new HEAD descends
  from the old one; a rewind says `moved back N commits`. `Newer` on a release
  channel reports a newer published release (`releases/latest`), never main
  movement.
- Mirror: a release tag is seeded into the checkout when the stable channel
  has an update, the mirror refreshes when only a release tag is new, and the
  4-hourly job fetches when a new stable tag appears even if main did not move.
- Docs: README "Update channel (stable or main)", how-it-works, configuration
  and design (D19); the new known limitations: Hermes v0.22 folds
  multi-profile gateways into one on its first update and only the root
  `gateway_state.json` is verified, and an MSIX/Store Hermes desktop running
  next to a source checkout is not closed by Safe Update.

### Fixed

- The fallback update check (no mirror and no GitHub API) ran without
  `--branch main` and so answered for Hermes' channel instead of main (a false
  "Nothing to update" for a user on main). It now asks for main.
- A Python or git folder left behind by an older pin no longer wins over the
  live one: read from Hermes' `tools/facts.json`, with a version-aware
  fallback. The GC step and the gate probe use the interpreter found when
  they run.
- The "is a chat busy?" check uses the freshest of the heartbeat, the newest
  message and the start time, and ignores garbage timestamps, as Hermes does;
  sessions with no heartbeat yet are no longer missed.
- `hermes update` exit code 2 now says another Hermes update is running (or
  why Hermes could not lock the install).
- The progress bar knows the "Building the web UI…" step, and the summary
  lists Hermes' other "owed work" lines (desktop app build owed, gateway
  restart still owed, interrupted after the code was updated, skipped desktop
  rebuild, `<name> failed`) as amber "Not finished" rows; they never change
  the verdict or exit code. On Hermes v0.22 the known "Fleet version check
  returned no rows" warning is such a follow-up with exit 0; the older exit-1
  form is still tolerated.

## [0.2.0] - 2026-10-13

### Added

- The tool's own version is now on screen. The header of every run, the
  update and `--check`, in the console display and in plain text alike, reads
  `Hermes Safe Update · v0.1.2` (`Hermes Update Check · v0.1.2`), with the
  version dimmed after the title; the date stays flush right. The footer and
  the window title keep the bare title. `--version`, `--help` and the `start`
  line of `safe-update.log` carry the same string. README: "Which version am I
  running?".
- A binary that is not a release no longer calls itself just `dev`.
  `scripts/build-all.sh` and `build-all.ps1`, when not given a version (or
  given `dev`), derive one with the new `scripts/dev-version.sh` and
  `dev-version.ps1`: the latest released version from `CHANGELOG.md`, the
  number of commits since it (from the tag `v<version>` if the repository
  has it, else from the commit that added the changelog section), the short
  commit, and `.dirty` for uncommitted changes to tracked files, for example
  `v0.1.2+25.3a8a2c7`; exactly the release (`v0.1.2`) when built at the
  release commit with a clean tree. A plain `go build` that was never given a
  version falls back to the commit Go stamped into the binary
  (`dev+3a8a2c7`, `.dirty`), and to `dev` when there is none.
  A version you pass explicitly (`build-all.sh v0.1.2`) is used exactly as
  given and git is not consulted, so release binaries are still
  byte-identical wherever they are built.
- New setting `desktop.relaunch` in `safe-update.yaml`: what happens to the
  desktop app after a run. `"auto"` (the default, and what the tool always
  did) reopens it only if it was running when the update started. `"always"`
  also reopens it after a verified update when it was already closed at the
  start, which is what you want if you close Hermes yourself a few seconds
  before starting the update. `"never"` leaves it closed. An unknown value is
  a configuration error that names the line. `--no-relaunch` still beats all
  three, and `--unattended` runs never open an app they did not close
  (`"always"` changes nothing there). When the app is not reopened the
  summary card has a new `Hermes app` row that says why (for example "it was
  closed when the update started; set desktop.relaunch: always to reopen
  it"); nothing new is shown when it was reopened. Documented in the README,
  `docs/configuration.md` and `safe-update.example.yaml`.
- New summary row `Newer`: after a verified update the tool takes one quick
  look (GitHub API, then `git ls-remote`; a few seconds at most) at whether
  `main` moved past what was installed, and if so says "7 more commits landed
  on GitHub while this ran; run Safe Update again when convenient" (without a
  number when only a sha comparison was possible). It explains the "update
  available right after updating" effect. It is a courtesy: any error or
  timeout leaves the row out, it never changes the result or the exit code,
  it does not touch the checkout, works without a mirror (or with a stale
  one), and does not run in `--check`.
- Hooks get three more environment variables next to
  `HERMES_SAFE_UPDATE_STAGE` and `HERMES_SAFE_UPDATE_RESULT`, so a hook no
  longer has to guess paths: `HERMES_SAFE_UPDATE_HOME` (the Hermes home, the
  root and not a profile), `HERMES_SAFE_UPDATE_CHECKOUT` (the source checkout
  being updated) and `HERMES_SAFE_UPDATE_PYTHON` (Hermes' managed Python).
  All are absolute paths, or an empty string when unknown, and always set.
  The Python is looked up when each hook starts, so `post-update` and
  `post-relaunch` hooks get the interpreter the update installed (its folder
  name carries a version and changes on updates). `HERMES_HOME` is still not
  passed on. Variables are only ever added, never renamed or removed.
  Documented in the README, `docs/configuration.md` and `docs/design.md`.
- Example hook `examples/hooks/bundled-skill-merge/`: after a verified update
  it 3-way merges upstream changes into the bundled skills you have edited
  (Hermes itself never updates an edited skill again), with a backup of your
  copy, a keep list for skills that must never be merged, and no change at
  all on a conflict. The manifest, state file, saved base and keep list are
  copied into the run's backup folder before they are edited, and a skill
  whose merge fails makes the hook exit 1 (a warning on the summary card). It finds Hermes through the new hook variables, so it
  needs no helper script; a Windows `.cmd` and a macOS/Linux `.sh` wrapper,
  `safe-update.yaml` snippets and a README are included. It imports private
  Hermes code and says "skipped" (exit 1, nothing changed) if that cannot be
  loaded. Run on Windows and, in WSL2 (Debian, dash and bash), on Linux, both
  on a scratch copy of a Hermes home; not run on a Mac. Not part of the
  release archives.

- Launcher shims for macOS and Linux, the counterparts of the Windows `.cmd`
  shims: `hermes-safe-update.sh` and `hermes-update-check.sh` (find the
  binary next to the shim, in `~/.local/bin` or on PATH; print the exit code
  and log location; wait for Enter), `hermes-safe-update.desktop` (Linux
  application menu entry with a look-only "check" action) and two `.command`
  files (macOS: double-click in Finder). They ship as release assets and
  are covered by `SHA256SUMS`. Tested in automated tests on Linux (dash and
  bash in POSIX mode) and with `desktop-file-validate`; not yet tried in a
  real Linux desktop session or on a Mac.

### Fixed

- The summary's `Version` row counted the pre-flight forecast, not what was
  installed: `hermes update` fetches GitHub itself a few minutes after the
  check, so a run that forecast 85 commits and installed 92 said 85. The row
  now counts the commits between the old and the new HEAD with git, falls
  back to the "Found N new commit(s)" line of `hermes update`, and only then
  to the forecast.
- The summary's `Source` row showed a stale mirror age. The mirror status was
  read before the check, so a run that refreshed the mirror seconds earlier
  still said "refreshed 6 h ago", and the age grew while the update ran. It
  now says "local mirror, refreshed at the start of this run" after a refresh
  in this run, "local mirror, up to date with GitHub at the start of this
  run" when the mirror already matched, and otherwise the age of the last
  refresh as of the start of the run ("refreshed 40 min before this run").
  A refresh that failed still falls back to GitHub, and the row says so.
- macOS/Linux: the scheduled mirror refresh could never run. The Hermes
  cron job calls `mirror refresh` without `--accept-untested`, and the
  untested-platform question was asked first, so every run exited 3 without
  a word and the job looked "active" forever. `mirror refresh` and
  `mirror status` no longer ask; `run`, `mirror setup` and the settings
  commands that change something still do (`check` and the look-only
  settings commands stopped asking too, see Changed).

- Verify no longer demands a messaging gateway that was never there. A
  desktop-only install (no gateway process and no live pid in
  `gateway_state.json` before the update) used to get `hermes gateway start
  --all` and `RESULT: PROBLEM` after a good update; now no gateway is
  required, checked or started. If a gateway did run before, behaviour is as
  before: it must report the new commit, and is started if it is gone.
- macOS/Linux: when the tool does restart a gateway it runs `hermes gateway
  start` instead of `gateway start --all`, because `--all` first kills every
  gateway of every profile (read from `hermes_cli/gateway.py`; not run on a
  real Mac or Linux box).
- macOS/Linux: `SIGTERM` and `SIGHUP` now end a run like Ctrl-C (the update
  marker is released and the usual clean-up runs). Before, a closed terminal,
  a `launchd`/`systemd` stop or a cron timeout killed the tool on the spot
  and left the marker behind. Checked with a signal test on Linux (WSL2);
  not run under a real `launchd` or `systemd`.
- macOS/Linux: when `hermes update` defers the cua-driver (Computer Use)
  refresh, the screen no longer says "handled by the safe updater after
  verification" (it is not: the refresh step only exists with Windows logon
  tasks) and Hermes' own `computer-use install --upgrade` hint is shown
  instead of hidden. Unchanged on Windows.
- macOS: a running `/Applications/Hermes.app` (or `~/Applications`) is now
  recognised as the desktop app, so the wait/close steps see it; `hermes
  update` copies the rebuilt app over it and refuses while it runs. The
  bundle's Electron helpers still never count. Fake-tested; not run on a
  real Mac.
- macOS/Linux: a Hermes backend (`hermes serve`) started through the entry
  script is now recognised by the process check (it was only recognised by
  `hermes.exe` or `hermes_cli` in the command line), so leftover backends are
  stopped before the update. A venv python that resolves to a system
  interpreter outside the install is still not claimed (it fails safe: fewer
  processes are stopped); noted in `docs/design.md`.
- macOS/Linux: the mirror is no longer proposed on a USB stick, other
  removable media or (macOS) a network share. Linux checks the `/sys`
  removable flag and the USB bus of each mounted block device; macOS reads
  the volume's `MNT_LOCAL` / `MNT_REMOVABLE` flags. The offer to set up the
  mirror no longer promises a window of its own where none can open
  (macOS/Linux): it says S shows the command to run in a terminal, and S
  prints it as a note instead of a "could not start" warning. Linux checked
  on a real kernel (WSL2, which has no removable disk); the macOS flags are
  not verified on a Mac.
- Linux: the settings commands now say clearly that Electron needs a
  graphical session when neither `DISPLAY` nor `WAYLAND_DISPLAY` is set (SSH,
  cron, services), and stop before the backup. Before, Electron was started
  and aborted with a generic "settings migrator failed". Not run on a Linux
  desktop.
- `scripts/build-all.sh` is now committed executable (100755), as the README
  and CONTRIBUTING already assumed when they run it directly; a test checks
  the recorded mode of it and of the shims.
- Wording: macOS/Linux no longer print "gateway pause check: unknown (not
  needed on this platform)" or "pause check unavailable (see log)", which
  read like failures; they say "not applicable on this OS" (a probe that
  really failed keeps the old wording). `--help` marks `--no-elevate` as
  Windows only, and the glyph docs no longer present the Windows console
  font as the only one.

### Changed

- Docs: the README and `docs/design.md` no longer say CI "will run" on macOS
  and Linux once published; the unit tests run there on every push, and the
  Linux test binaries were also run on a real kernel (WSL2). No real Hermes
  update has been run on macOS or Linux. The "Can I schedule it?" answer now
  matches the Platforms table (one unattended update started by a Windows
  scheduled task ran and verified) and has macOS/Linux guidance for cron,
  launchd and systemd (untested). Quick start, log and backup paths and
  troubleshooting have macOS/Linux equivalents. Hook docs gain a POSIX
  example (`["/bin/sh", "/abs/x.sh"]`) and say `~` and `$VAR` are not
  expanded in `run` or `dir` (a test pins it); the validation message names
  both forms. `HERMES_SAFE_UPDATE_OSC_PROGRESS=1` is documented, and
  `theme.taskbar` / `theme.flash` say they do nothing on macOS/Linux
  (taskbar unless that variable is set).
- macOS/Linux: the terminal title is restored after the run. The tool saves
  the title on the terminal's title stack (`CSI 22;0 t`) when it starts the
  progress display and restores it (`CSI 23;0 t`) when it ends, so the tab
  no longer keeps "Hermes Update Check · stopped". Terminals without a title
  stack (Apple's Terminal.app is not known to have one) ignore it and keep
  the last title. Seen in the escape output of the real Linux binary on a
  pty (WSL2); not tried in a real terminal emulator or on a Mac.
- macOS/Linux: `HERMES_HOME` is now ignored when the tool looks for the
  Hermes folder, as it already was on Windows. The tool always removed it
  from the environment of every command it starts, so honouring it for the
  lookup watched one home while `hermes update` ran against another. The
  launcher shims clear it too. To use a home elsewhere set
  `paths.hermes_home` (and `paths.checkout`). Documented in the README and
  `docs/configuration.md`.
- A Hermes install the tool cannot classify (no checkout at the expected
  folder, no packaged-install hint) now says "no Hermes source checkout found
  at <folder>; update it the way it was installed, or set `paths.checkout`".
  It used to print "This Hermes install is a unknown, not a managed source
  checkout: ." with the advice empty. README and `docs/design.md` no longer
  claim that AppImage, deb and rpm installs are detected; they never were
  (exit 5 as before).
- macOS/Linux: the untested-platform question is now really one-time. Your
  answer (Y, or `--accept-untested`) is remembered per OS in
  `safe-update.json` (`accepted_untested`); later runs, scheduled ones
  included, do not ask, and `--unattended` needs `--accept-untested` only
  the first time. It also no longer asks before look-only commands: `check`
  (`--check`), `list-backups` and `migrate-settings --dry-run`. `run`,
  `mirror setup`, `migrate-settings`, `backup-settings` and `revert-settings`
  still ask. Fake-tested; not run on a real Mac or Linux box.
- Linux close request: no change in behaviour, now checked. One `SIGTERM` to
  the Electron main process is `app.quit()` (confirmed in Electron 40.10.2
  source and with a synthetic app on Linux under WSL2); `SIGTERM` to renderer
  or GPU helpers crashed the main process. The tool already signals only the
  main process, once; a test now pins that. Not yet run with Hermes' own
  desktop app on a real Linux desktop (steps in `docs/design.md`).
- The "started from inside a Hermes chat" stop now says "Run this from a
  normal terminal or a shortcut" instead of "… or the Start menu", which
  only fits Windows.
- `--help` names the launcher shims for each OS.

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

[Unreleased]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/cloudn8ive/hermes-safe-update/releases/tag/v0.1.0
