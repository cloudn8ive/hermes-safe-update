# hermes-safe-update

A small command-line program that updates [Hermes Agent](https://github.com/NousResearch/hermes-agent)
on your machine without cutting off work that is still running.

`hermes update` replaces the code under a running app, and the desktop app
has to be closed for that. If an agent is in the middle of a task, that task
is lost. hermes-safe-update waits until your sessions are idle, closes the
desktop app politely, runs `hermes update`, checks that the new version
really came up, carries your desktop settings over to the address the app
uses now if an update moved them (a workaround for settings lost on a few
Hermes builds on 2026-10-01; see
[below](#your-desktop-settings-disappeared-after-updating)), and opens Hermes again.

It is one `.exe` with no installer and nothing to compile. It is not
affiliated with Nous Research (see [NOTICE](NOTICE)).

## Demo

https://github.com/user-attachments/assets/6c1dc345-71a6-4174-a7ad-77aa57387dd9

A 75-second narrated walk through a full update (press play above; the same
video is in the repository as [`docs/demo.mp4`](docs/demo.mp4)). It replays
the program's own recorded screen output from its end-to-end
tests, sped up: nothing was installed or closed, and every name, path and
session id in it is made up.

## Platforms

| OS | Architectures | Status |
|---|---|---|
| Windows 10 | amd64 | unit-tested. Many real interactive updates have run and verified on Windows 10 22H2 amd64 with v0.1.x builds, the latest on 2026-10-13 with the code of this release, and one real unattended update (started by a scheduled task); the Start-menu and Ctrl+K launches were tried. The settings migration was run in a sandbox on a copy of a real desktop settings store and in an automated test on synthetic data, both with the real Electron. The first real update (v0.1.0 development build) failed at the settings step because that Hermes range removed renderer-server.ts; this was fixed (file:// detection, backup when detection fails). Since the fix, one real update has migrated a real settings store from the old `http://127.0.0.1` origin to `file://` (with a backup), and every later real update found nothing left to migrate. No other real migration scenario has been run |
| Windows 11 | amd64 | expected to work; not yet tested |
| Windows 10 / 11 | arm64 | builds; not run on arm64 hardware |
| macOS | arm64, amd64 | **untested, proceed at your own risk** |
| Linux | amd64, arm64 | **untested, proceed at your own risk** |

All targets compile (cross-compiled and vetted). The unit tests run in CI on
Windows, macOS and Linux on every push, and the Linux test binaries were also
run on a real Linux kernel (WSL2) in an audit. Nobody has run a real Hermes
update with it on macOS or Linux; on those systems the code paths are tested
with fakes only.
There it prints a warning and, before a command that closes, updates or
writes something (`run`, `mirror setup`, `channel set`, the settings commands), asks you to
confirm once. Your answer (Y, or `--accept-untested`) is remembered in
`safe-update.json`, so later runs, scheduled ones included, do not ask again;
`--unattended` needs `--accept-untested` the first time. The look-only
commands (`--check`, `channel`, `list-backups`, `migrate-settings --dry-run`) and the
scheduled `mirror refresh` and `mirror status` never ask. Reports from
testers are welcome.

## Your desktop settings disappeared after updating?

(Install the tool first: see [Install](#install). The command below is part of it.)

For about twelve hours on 2026-10-01 (Hermes `main` commits 587e673e2a to
03477e9ae2), the desktop app loaded its page from a tiny local web server at
`http://127.0.0.1:47891` instead of from a local file (`file://`). The desktop
app keeps its interface preferences (last route, scroll positions, which tool
panels are open and so on) in the web engine's local storage, and local
storage is kept separately for each address. On that build the app looked at
the new address, found nothing there, and started as if freshly installed.

Hermes reverted that change the same day
([#130852](https://github.com/NousResearch/hermes-agent/pull/130852); the
report, [#130635](https://github.com/NousResearch/hermes-agent/issues/130635),
was closed). The app loads from `file://` again and reads back the settings
saved before that build. Hermes' note on the closed report says that settings
changed while on that build do not carry over. hermes-safe-update offers a
workaround for that gap: after every verified update it copies the settings
from the address the previous build used to the one the app uses now. If you
never ran such a build, or changed nothing on it, there is nothing to copy and
the step is skipped. If you ran one and your recent settings are gone, close
the Hermes desktop app and run:

```
hermes-safe-update migrate-settings
```

It takes a backup first, works on a copy, checks the result, and only then
swaps it in. To see the plan without changing anything, add `--dry-run`. To
undo it:

```
hermes-safe-update list-backups
hermes-safe-update revert-settings
```

Merge rules and report format: [docs/settings-migration.md](docs/settings-migration.md).
`migrate-settings`, `backup-settings` and `revert-settings` refuse to run
while the desktop app is open (exit code 6). `list-backups` and
`migrate-settings --dry-run` do not.

## Install

1. Download the file for your system from the Releases page:
   `hermes-safe-update-windows-amd64.exe` (most PCs) or
   `hermes-safe-update-windows-arm64.exe`, plus `SHA256SUMS`. `SHA256SUMS`
   also covers the optional shims in step 4.
2. Check the download. In PowerShell:
   ```
   Get-FileHash .\hermes-safe-update-windows-amd64.exe -Algorithm SHA256
   ```
   `Get-FileHash` prints the hash in capitals; `SHA256SUMS` is lowercase, so
   compare ignoring case. Each line of `SHA256SUMS` reads `<hash>  <file name>`:
   compare the line with the name of the file you downloaded.
3. Rename it to `hermes-safe-update.exe` and put it in a folder of its own:
   `%LOCALAPPDATA%\Programs\hermes-safe-update\` (create it if needed). Not
   in `%LOCALAPPDATA%\hermes\bin`: that folder belongs to Hermes, and a Hermes
   reinstall or repair might clear it.
4. Optional: download `hermes-safe-update.cmd` and `hermes-update-check.cmd`
   from the Releases page (their hashes are in `SHA256SUMS`) and put them in
   the same folder (see [Shims](#shims)).

The binaries are **not code-signed**. Windows SmartScreen may show "Windows
protected your PC" the first time. Check the hash above, then choose "More
info" and "Run anyway", or right-click the file, open Properties and tick
"Unblock". Do not turn SmartScreen off for this.

### Optional: put it on PATH

You only need this if you want to type `hermes-safe-update` in any terminal.
The shims and the `.\hermes-safe-update.exe` form in Quick start work without it.

- By hand: Settings, search for "Edit environment variables for your
  account", select `Path`, Edit, New, paste
  `%LOCALAPPDATA%\Programs\hermes-safe-update`, OK.
- Or in PowerShell. This adds the folder to your user `Path` once, keeps
  what is already there and does not run twice over:
  ```
  $d="$env:LOCALAPPDATA\Programs\hermes-safe-update"; $k=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment',$true); $p=@($k.GetValue('Path','','DoNotExpandEnvironmentNames') -split ';' | ? {$_}); if (($p | % {$_.TrimEnd('\')}) -notcontains $d) { $k.SetValue('Path',(($p+$d) -join ';'),'ExpandString') }; $k.Close()
  ```
  Open a new terminal afterwards; open ones keep the old `Path`.

### macOS and Linux

These builds are **untested**. If you try one, please tell us how it went
through the [platform test report](../../issues/new?template=platform_test_report.yml);
success and failure reports both help.

1. Download `hermes-safe-update-<os>-<arch>` and `SHA256SUMS`, then check it:
   `sha256sum -c --ignore-missing SHA256SUMS` (macOS: `shasum -a 256 -c
   --ignore-missing SHA256SUMS`).
2. `chmod +x hermes-safe-update-<os>-<arch>`, then move it to a folder on your
   `PATH`, for example `~/.local/bin/hermes-safe-update`.
3. macOS blocks the first run of a downloaded, unsigned program. Clear the
   quarantine flag with `xattr -d com.apple.quarantine <file>` (or approve it
   in System Settings, Privacy & Security).
4. Before a command that closes or updates something it warns and asks you
   to confirm, once (see [Platforms](#platforms)).
5. Optional: download the shell shims `hermes-safe-update.sh` and
   `hermes-update-check.sh`, plus `hermes-safe-update.desktop` (Linux) or the
   two `.command` files (macOS), and put them in `~/.local/bin` next to the
   binary. They give you a menu entry or a Finder double-click (see
   [Shims](#shims)). `SHA256SUMS` covers them too.

To build it yourself you need Go 1.26 or newer:
`scripts/build-all.sh v0.1.0` (git-bash, macOS, Linux) or
`powershell -File scripts\build-all.ps1 -Version v0.1.0`. Output and
`SHA256SUMS` go to `dist/`. A version you pass is used exactly as given (that
is how releases are built, byte for byte the same everywhere). Without one,
or with `dev`, a local build gets a version worked out from the repository,
see [Which version am I running?](#which-version-am-i-running).

## Quick start

Run it from a normal terminal or a shortcut, never from a terminal inside
the Hermes desktop app. An update closes that app, so the tool refuses to
start one from there (exit code 4). `--check` is safe anywhere.

In PowerShell, from the install folder (no PATH needed):

```
cd $env:LOCALAPPDATA\Programs\hermes-safe-update
.\hermes-safe-update.exe --check       # look only: leaves Hermes as it is (may fetch commits, like git fetch)
.\hermes-safe-update.exe               # update, asking before anything is closed
.\hermes-safe-update.exe --unattended  # for a scheduled task: never asks
.\hermes-safe-update.exe channel       # which Hermes update channel (stable or main) this install uses
```

On macOS and Linux, in a terminal (the binary is on your `PATH`, see
[macOS and Linux](#macos-and-linux)):

```
hermes-safe-update --check             # look only; never asks the untested-platform question
hermes-safe-update                     # update; asks once that you accept the untested platform
hermes-safe-update --unattended --accept-untested   # for cron, launchd or systemd (see FAQ)
hermes-safe-update channel             # which Hermes update channel this install uses; never asks
```

Or double-click the shims (`hermes-update-check.cmd` is the `--check` run;
macOS and Linux have `.command` and `.sh` equivalents, see [Shims](#shims)). If
the folder is on PATH, drop the `.\` and the `.exe`:

```
hermes-safe-update --check
hermes-safe-update
```

`--check` runs the pre-flight checks and tells you whether an update is
available, what is running and which sessions are active. It never closes Hermes or changes
it. HEAD, the branch, the index and the working tree of the checkout stay
exactly as they are. What it does write is like `git fetch`: it may refresh
the local mirror (if you set one up) and fetch the new commits and `v*` tags
into the checkout's git data, which makes the check accurate and the later
update faster. It also writes its own log and timing history.

The first line of every run, `--check` included, names the tool and its
version: `Hermes Safe Update · v0.1.2` (`Hermes Update Check · v0.1.2` for
`--check`), in a real console window and in plain text alike. The same
version is in `hermes-safe-update --version` and on the `start` line of
`safe-update.log`.

A normal run shows a progress footer in a real console window. In a pipe, a
scheduled task, or with `--plain` (also `TERM=dumb`,
`HERMES_SAFE_UPDATE_PLAIN=1`) it prints plain text instead. `NO_COLOR` keeps
the footer and only drops the colours.

An unattended run still waits for idle sessions (up to 30 minutes by
default). If sessions are still busy after that, it ends with exit code 3
and nothing has been changed.

## What a run does

1. **Install check.** If this is not a managed source checkout (see below),
   it says which updater to use and stops (exit 5).
2. **Pre-flight.** Is another update running? Is there enough free disk
   (5 GB by default)? Is the checkout dirty? Then it asks whether an update
   exists. On the main channel: a local mirror first if you set one up, then
   the GitHub API, then `hermes update --check --branch main`. On a release
   channel (stable): Hermes' own `hermes update --check`, which names the
   release. It predicts whether Python or Node dependencies will be
   reinstalled, so the time estimate is closer. Which channel it uses is
   explained in [Update channel](#update-channel-stable-or-main).
3. **Look for running processes**: desktop app, gateway, backends, kernels.
4. **Wait.** If anything is about to be stopped, it reads every profile's
   `state.db` (read-only) for sessions active in the last 3 minutes. While
   any are active it waits. You can press `A` to abort or `F` to go ahead
   anyway. Then it asks for a final `Y` (30 seconds, then it aborts).
5. **Close.** The desktop app gets a normal close request and 30 seconds to
   save its state. Only then is it force-closed, and the log says why it
   probably did not close (an open dialog, for example). Leftover backends
   and kernels are stopped.
6. **Update.** It runs `hermes update --yes --keep-stash` with a hard time
   limit (90 minutes): with `--branch main` on the main channel, with
   `--channel stable` when `update.channel` is `stable`, and with no channel
   flag when it follows a release channel that Hermes chose (Hermes then
   applies its own). If Hermes' own gateway pause fails before changing
   anything, it stops the gateway and retries once.
7. **Verify.** If a gateway was running before the update, it must report
   the new commit and be running (it is started again if it is not). If none
   was running, none is required and none is started. Hermes' own
   desktop-build check must pass. Hermes' known false "fleet
   version" warning does not turn a good update into a failure. (Hermes
   v0.22 reports it as a follow-up with exit code 0; the summary then shows
   it as a "Not finished" row.)
8. **Settings.** Back up and migrate the desktop settings if the address
   changed (see above). Hermes is closed at this point.
9. **Hooks, only after a verified update:** your `post-update` hooks run
   while Hermes is still closed.
10. **Reopen** the desktop app if it was running (setting
    `desktop.relaunch`: `always` also reopens it when you had already closed
    it before starting the update, `never` leaves it closed; see
    [docs/configuration.md](docs/configuration.md)), then run your
    `post-relaunch` hooks.
11. **Housekeeping, only after a verified update:** remove old dependency
    sets, refresh the cua-driver if the update asked for it. A failure in
    a hook or here is a warning; it never turns a verified update into a
    failed one.
12. Print a summary card and exit. The card has a `Channel` row (which
    channel the run used and why). After a verified update it also says,
    in one line each, if `main` moved while the update ran (`Newer`; on a
    release channel: if a newer release was published), and why the desktop
    app was not reopened (`Hermes app`).

The stages in more detail: [docs/how-it-works.md](docs/how-it-works.md).

## Update channel (stable or main)

Hermes v0.21.6 and later update from one of two channels:

- **stable**: only published, tested releases. Fewer updates. If you never
  chose a channel in Hermes, a checkout that is already ahead of the latest
  release waits for the next one. If you picked stable yourself (or Safe
  Update's `stable` setting does it over a main record or a fork), the next
  update moves the checkout back to that release; the rollback guard below
  asks first.
- **main**: every change the day it lands. The newest fixes, more risk.

Hermes keeps the choice per install: Settings → About in the desktop app, or
`hermes update --set-channel`. An install that was never set uses stable
(Hermes v0.21.7 and later) if the checkout comes from the official GitHub
repository and main if it comes from a fork or a mirror. A Hermes older than
v0.21.6 has no channels and always follows main.

**Safe Update follows Hermes' choice by default.** Before v0.3.0 it passed
`--branch main` on every run, which put a stable user on main without telling
them. Now `update.channel` in `safe-update.yaml` decides
([details](docs/configuration.md#update)):

| `update.channel` | What a run does |
|---|---|
| `follow` (default) | Reads Hermes' channel (offline, nothing written). On **main**, or on a Hermes without channels, the run is exactly as before: `--branch main`, the mirror and GitHub comparison. On **stable**, Hermes' own `hermes update --check` decides whether a release is waiting. |
| `stable` | Always the latest release (`--channel stable`). A Hermes without release channels stops in the pre-flight with the reason (exit 4), before anything is closed. |
| `main` | Always `--branch main`, whatever Hermes is set to. Works on every Hermes version. |

`--channel follow|stable|main` on `run` or `check` overrides it for one run.
If you use stable in Hermes, Safe Update now stays on stable. If you want
main whatever Hermes says, set `update.channel: main`.

**Look and switch.** `hermes-safe-update channel` prints the channel Hermes
uses for this install, what `update.channel` is set to and what the next run
will do. It needs no network and never asks the untested-platform question.
`hermes-safe-update channel set stable` (or `main`) switches through Hermes'
own `hermes update --set-channel`; the desktop's Settings → About switch does
the same. Safe Update never edits Hermes' configuration itself, and a run
never changes the channel. A switch updates nothing by itself; the next
update uses the new channel. If `safe-update.yaml` forces a channel, the
command says that Safe Update keeps using it. Exit codes: 0 done (or already
set), 3 you answered no, 4 refused (a Hermes that cannot switch, Hermes
refused, or a question that cannot be asked), 64 for any other name than
`stable` or `main`.

**Rollback guard.** On stable, your checkout can be *newer* than the selected
release (for example after switching from main to stable). Updating would
then move Hermes **back** to that release, and Hermes does
not ask. Safe Update stops before it closes anything: an attended run asks
for an explicit `Y` that names both versions (`--yes` does not skip this
question; any other key, or no answer, exits 4 with nothing changed). An
`--unattended` run never rolls back: exit 4, and the message names the
command to do it by hand. Safe Update does not rely on Hermes' wording for
this (Hermes v0.21.6 and v0.21.7 call an older release "available"): it asks
git whether the release commit is an ancestor of the checkout. If that commit
is not in the checkout, a full clone cannot have it as an ancestor and the
update goes forward; a shallow clone cannot prove that, so the run asks (or,
unattended, stops) with a message saying the move back cannot be ruled out.
`channel set stable` warns the same way, before it
writes anything; it runs a read-only release check first and asks for a `Y`
(`--yes` goes ahead; without a terminal it refuses). If that check cannot run
(offline), it says so and still asks.

## Safety rules

- It never closes Hermes over running work. A session database it cannot
  read counts as busy, never as idle.
- Nothing in Hermes or its settings is changed before you confirm. (The tool
  may refresh its local mirror and fetch commits into the checkout from it,
  and it writes its own log and timing files.) An abort before that leaves
  Hermes running and no marker behind.
- The update-in-progress marker is claimed only after Hermes has closed, and
  released at the end only if this process owns it.
- Closing is graceful first. Force comes after a timeout, and the tool
  refuses to kill its own process, its parents, or any tree it runs in.
- `hermes update` runs without `--gateway`, and the checkout's `origin` is
  never rewritten. A run only reads Hermes' channel; the one command that
  changes it is the explicit `channel set`, and it asks Hermes to do it. Its environment is your own, except that `HERMES_HOME` is
  removed (for every command the tool starts, and it is not used to find the
  Hermes folder either, on any OS: use `paths.hermes_home`) and
  `PYTHONIOENCODING=utf-8`
  is added.
- `state.db` is only ever opened read-only. Desktop settings are changed on
  a copy that is verified before it is swapped in, with a backup and
  automatic rollback.
- A step whose result the tool cannot confirm does not count as success: the
  update is verified against the gateway's reported commit, not against
  wording in Hermes' output. Only the dependency cleanup warns when its
  output is not recognised ("Hermes output changed"). The update check,
  gate probe, verification and progress display do not warn about unexpected
  wording; they fall back to the slower path, show less detail, or report
  the result as unknown.
- No telemetry. The network is used only for the update check and the
  optional mirror. See [SECURITY.md](SECURITY.md).

## Supported installs

Only a **managed source checkout** is updated: `%LOCALAPPDATA%\hermes\hermes-agent`
on Windows, `~/.hermes/hermes-agent` on macOS and Linux (override with
`paths.hermes_home` / `paths.checkout`).

A packaged install (macOS `Hermes.app`, Windows MSIX, Docker, Nix) is
recognised and left alone. The tool prints which updater to use and exits
with code 5. Anything else that has no checkout at that place (an AppImage,
a deb or rpm, a checkout in another folder) is not recognised by name: the
tool says it found no source checkout there and suggests updating Hermes the
way it was installed, or setting `paths.checkout`; it also exits with code 5
and changes nothing. These detections are code plus unit tests on every
platform; what has been run for real is stated in the
[Platforms](#platforms) table.

## Configuration

Everything is optional. Copy [safe-update.example.yaml](safe-update.example.yaml)
to `%LOCALAPPDATA%\hermes\safe-update.yaml` (macOS and Linux:
`~/.hermes/safe-update.yaml`), or pass `--config FILE`, and keep only the
keys you change. A misspelled key is an error that names the line, so a typo
never silently does nothing. Every key is explained in
[docs/configuration.md](docs/configuration.md).

## Hooks

A hook is your own command, run at a fixed point: `pre-close`,
`post-update` or `post-relaunch`. It is an argument list, not shell text. It
gets five variables in its environment (stage, result, the Hermes home, the
checkout and Hermes' Python; exact meaning in
[docs/configuration.md](docs/configuration.md#hooks)), and its last output
line appears on the summary card. A failing or slow hook is a warning only.
A ready-made `post-update` hook that keeps your edited bundled skills up to
date lives in [examples/hooks/bundled-skill-merge](examples/hooks/bundled-skill-merge)
(Windows `.cmd` and macOS/Linux `.sh` wrappers; tested on Windows and, in
WSL2, on Linux; not on a Mac).

## Local mirror (optional)

GitHub's git endpoint can be slow. `hermes-safe-update mirror setup` clones
the Hermes repository into a local bare copy (a download of the full
repository) and creates a Hermes cron job that refreshes it every 4 hours, so
new commits are copied from disk before Hermes is closed. If the mirror is
missing, the tool checks GitHub directly. If it is stale, it is refreshed from
GitHub first and then used; only if that refresh fails does the tool fall back
to GitHub. `mirror status` shows its state; `mirror setup --path DIR` picks
the location. After a run on a real console it may offer to set the mirror
up (never with `--unattended`); answer `N` to stop being asked.

## Settings backups

`backup-settings` makes a backup on demand and `list-backups` lists them.
Backups live in `<hermes home>\backups\settings-<UTC date-time>\` (macOS and
Linux: `<hermes home>/backups/settings-<UTC date-time>/`), and the
newest 3 are kept (`settings.backup_retention`). Merge rules and revert:
[docs/settings-migration.md](docs/settings-migration.md).

## Known limitations

- Time estimates learn from your own past updates. The first few updates, and
  especially updates that change Node packages, can take longer than the
  estimate says. When the tool has fewer than 3 earlier runs like the current
  one, it adds "(rough: ...)" to the estimate.
- Hermes v0.22 folds several per-profile gateways into one on its first
  update. Safe Update verifies only the gateway of the main Hermes home
  (`gateway_state.json` in the root folder), not one per profile.
- An MSIX or Store build of the Hermes desktop app that runs next to a source
  checkout is not closed by Safe Update, so close it yourself before an update.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | done: updated and verified, check finished, or already up to date |
| 1 | the update ran but needs attention, or an unexpected error |
| 2 | the `hermes` launcher was not found |
| 3 | cancelled before anything changed (you, a timeout, idle wait exceeded, the desktop app would not close, untested platform declined) |
| 4 | pre-flight stop: another update running, low disk, update check failed, started from inside a Hermes chat, `update.channel: stable` on a Hermes without release channels, an update that would move Hermes back to an older release (you answered no, or `--unattended`), or `channel set` refused |
| 5 | not a managed source checkout: nothing changed |
| 6 | settings command refused because the desktop app is running |
| 64 | bad command line or invalid configuration |

`mirror setup` has its own meaning for three codes: 1 is a download or
cron-job failure, 2 is no free space or no location for the mirror, 3 is
"declined". `mirror refresh` exits 0 or 1 and prints nothing (see
`logs\update-mirror.log`).

Ctrl+C while the tool is still waiting or asking for confirmation ends the
run with code 3. Ctrl+C while
`hermes update` is running reaches Hermes itself (it shares the
console). The run then ends with code 1, because something may already have
changed.

## Shims

A shim is a small launcher script for a shortcut or menu entry. It finds the
tool, sets the window title, passes all arguments through, prints the exit
code and log location, and waits for a key so the window stays open. It
exits with code 2 if it cannot find the tool. Each OS has its own pair; the
look-only one (`hermes-update-check`) is the same run with `--check`. The
tool itself never creates or changes shortcuts, Start menu entries or menu
entries; you add them as below.

**Windows.** `scripts\hermes-safe-update.cmd` runs `hermes-safe-update.exe`
(found next to the shim, else in `%LOCALAPPDATA%\Programs\hermes-safe-update`,
else on PATH). `scripts\hermes-update-check.cmd` adds `--check`.

**macOS and Linux.** `scripts/hermes-safe-update.sh` runs the
`hermes-safe-update` binary (found next to the shim, else in `~/.local/bin`,
else on PATH); `scripts/hermes-update-check.sh` adds `--check`. Like the
`.cmd` shim it clears `HERMES_HOME`, so it always updates the default
install. Set `HERMES_SAFE_UPDATE_NO_PAUSE=1` to skip the final "Press Enter".
From a terminal you can run the binary directly instead. Tested so far: the
`.sh` and `.command` shims run in automated tests on Linux (dash, and bash in
POSIX mode, which is what macOS uses for `sh`), and the `.desktop` entry
passes `desktop-file-validate`. Neither has been tried in a real Linux
desktop session or on a Mac yet.

### Shortcuts

**Windows.** Desktop shortcut:

1. Right-click the desktop, New, Shortcut.
2. Target: `%LOCALAPPDATA%\Programs\hermes-safe-update\hermes-safe-update.cmd`
   (or `hermes-update-check.cmd` for the look-only check). Next, name it, Finish.

Start menu entry: do the same, but save the shortcut in
`%APPDATA%\Microsoft\Windows\Start Menu\Programs` (paste that into Explorer's
address bar), or move the finished shortcut there.

Or in PowerShell, one shortcut per shim:

```
$t="$env:LOCALAPPDATA\Programs\hermes-safe-update\hermes-safe-update.cmd"; $l=(New-Object -ComObject WScript.Shell).CreateShortcut("$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Safe Hermes update.lnk"); $l.TargetPath=$t; $l.WorkingDirectory=Split-Path $t; $l.Save()
```

**Linux.** `scripts/hermes-safe-update.desktop` is an application menu entry
("Hermes Safe Update", with "Hermes Update Check (look only)" as its
right-click action). It opens a terminal and runs the two `.sh` shims from
`~/.local/bin`; if you keep them elsewhere, edit the two `Exec=` lines.

```
cp hermes-safe-update.desktop ~/.local/share/applications/
```

It is this tool's own entry; Hermes' entry (`com.nousresearch.hermes.desktop`)
belongs to Hermes and is left alone.

**macOS.** There is no Start menu to add to. Double-click
`hermes-safe-update.command` (or `hermes-update-check.command`) in Finder;
it opens Terminal and runs the shim next to it, so keep the `.command` and
`.sh` files together. Make the `.command` files runnable once and clear the
download flag:

```
cd ~/.local/bin
chmod +x hermes-safe-update.command hermes-update-check.command
xattr -d com.apple.quarantine hermes-safe-update.command hermes-update-check.command
```

Drag a `.command` file to the Dock (right part, next to the Trash) for a
one-click launcher.

## Updating the tool

1. Close the tool if it is running. Do not replace it during an update.
2. Download the new release and check its hash as in [Install](#install).
3. Replace `hermes-safe-update.exe` (and the two shims, if you use them) in
   the install folder. On macOS and Linux replace the binary in
   `~/.local/bin` (and the shims, if you use them), run `chmod +x` on it again,
   and on macOS clear the quarantine flag again (see [macOS and
   Linux](#macos-and-linux)).

Your config, the machine state and the settings backups stay as they are.

## Which version am I running?

Look at the first line of any run, run `hermes-safe-update --version`, or
read the `start` line of `safe-update.log` (`version=...`). A release prints
just its version, for example `v0.1.2`. A binary built from a checkout
between releases prints where it comes from:

| It prints | It means |
| --- | --- |
| `v0.1.2` | the release itself |
| `v0.1.2+25.3a8a2c7` | 25 commits after v0.1.2, built at commit `3a8a2c7` |
| `v0.1.2+25.3a8a2c7.dirty` | the same, with uncommitted changes to tracked files |
| `dev+3a8a2c7` | a plain `go build` in a checkout (Go stamps the commit; `.dirty` is added for local changes) |
| `dev` | no version information at all (a source archive built without the scripts) |

The `+25` counts commits since the tag `v0.1.2` if the repository has it,
otherwise since the commit that added the `## [0.1.2]` section to
`CHANGELOG.md`. `scripts/build-all.sh` and `scripts/build-all.ps1` work this
out with `scripts/dev-version.sh` and `scripts/dev-version.ps1`.

## Uninstall

Uninstalling does not touch Hermes or its settings.

1. Delete the install folder `%LOCALAPPDATA%\Programs\hermes-safe-update`
   (or wherever you put the exe and shims), and any shortcuts you made. If you
   added it to PATH, remove that entry too. On macOS and Linux delete the
   binary and the shims from `~/.local/bin`, any Dock item, and on Linux
   `~/.local/share/applications/hermes-safe-update.desktop`.
2. If you ran `mirror setup`, remove its refresh job and folder:
   - `hermes cron list` shows the job, named "Hermes update mirror refresh"
     (or the `mirror.job_name` you set). Remove it with `hermes cron remove <id>`.
   - Run `hermes-safe-update mirror status` first to see the mirror folder,
     then delete it. It holds a full copy of the Hermes repository.
   - Delete `<hermes home>\scripts\hermes_safe_update_mirror.py`, the small
     script the cron job ran.
3. Optional leftovers, all in `<hermes home>` (`%LOCALAPPDATA%\hermes`; on
   macOS and Linux `~/.hermes`). Hermes does not use any of them:
   - `safe-update.yaml`, your config, if you made one
   - `safe-update.json`, the tool's machine state (mirror location and so on)
   - `logs\safe-update.log`, `logs\safe-update-timings.json`,
     `logs\update-mirror.log` (if it exists)
   - `cache\update-mirror-state.json` and `cache\update-mirror.lock`
   - `backups\settings-*`, the desktop settings backups. Keep them if you
     might want to go back to an old state; they are plain copies and safe to
     delete.

## FAQ

**Does it replace `hermes update`?** No, it runs it. It adds the waiting, the
verification and the settings handling around it.

**Where should I run it?** From a normal terminal, a shortcut or a scheduled
task, not from a terminal inside the Hermes desktop app, because it closes
that app.

**Does it need Python?** No. It uses Hermes' own managed Python for two
optional steps (the gateway pause probe and dependency cleanup). If that
Python is missing, those steps are skipped or reported as unknown; see the log.

**Where is the log?** `<hermes home>\logs\safe-update.log` (macOS and Linux:
`<hermes home>/logs/safe-update.log`, by default `~/.hermes/logs/`). Your home folder
appears as `~` and the local app-data folder as `%LOCALAPPDATA%`. Session
titles and ids never go into the log (the screen shows them; the log only
counts busy sessions), and neither does the mirror folder. Commit hashes,
versions and your hook names do appear. Read an excerpt before you paste it,
but it should hold no names or folders of yours.

**How long does it take?** The progress footer estimates from your last 5
matching runs, and from built-in numbers before there is any history. The
desktop build is the slow part, several minutes. With little history it says
"rough" (see Known limitations).

**Can I schedule it?** Yes, it is meant for that: use `--unattended`. One
unattended update started by a Windows scheduled task has run and verified
(see [Platforms](#platforms)); nothing else has been tried as a scheduled job.

- Windows: `hermes-safe-update --unattended --no-elevate` (`--no-elevate`
  means it never shows an admin prompt; it has no effect elsewhere).
- macOS and Linux (cron, a launchd agent or a systemd timer; all untested):
  run `hermes-safe-update --unattended --accept-untested`, with the full path
  to the binary because a scheduler has a short `PATH`. The first run records
  that you accept the untested platform in `safe-update.json`, so later jobs
  may drop `--accept-untested`; without it and without that record an
  unattended run stops with exit 3. On Linux a scheduler has no desktop
  session: the tool then does not reopen the desktop app (it says why) and
  the settings step ends with a warning that Electron needs a graphical
  session; the update itself is not affected. If the scheduler stops the job
  (a timeout, `systemctl stop`, a closed terminal), `SIGTERM`/`SIGHUP` end
  the run like Ctrl+C and the update marker is released. The Hermes cron job
  of `mirror setup` runs `mirror refresh`, which never asks the
  untested-platform question.

## Troubleshooting

- **Exit 5, "not a source checkout".** Your Hermes is a packaged install, or
  the home folder is not where the tool looks. Set `paths.hermes_home`.
- **Exit 2.** The `hermes` launcher was not found. Windows: it is expected in
  `<hermes home>\bin`. macOS and Linux: in `<checkout>/.hermes/bin/hermes`,
  `~/.local/bin/hermes` or `<hermes home>/bin/hermes`. Check
  `paths.hermes_home`.
- **Exit 4.** Read the STOP line in the output or log: marker held by
  another update, less than `tunables.min_free_gb` free, the update check
  failed (offline?), or the updater was started from inside a Hermes chat
  (run it from a normal terminal or a shortcut; updating would stop the
  process that runs it. `--check` works anywhere).
- **Exit 4, "would move Hermes back".** Your checkout is newer than the latest
  stable release. Nothing was changed. Run `hermes-safe-update channel` to see
  the channel, `channel set main` to follow main, or run
  `hermes-safe-update run` yourself and answer `Y` to go back on purpose.
- **Exit 4 (or a question) on a release channel in a shallow clone.** A shallow
  clone cannot prove that a release is newer than your checkout, so an
  attended run asks before updating and an unattended run stops. Fix: make the
  clone full with `git fetch --unshallow` in the Hermes folder, or set
  `update.channel: main`.
- **Exit 3 after waiting.** Sessions stayed busy for 30 minutes. Nothing was
  changed. Try again when idle, or raise `tunables.idle_wait_max_s`.
- **Exit 6.** Close the Hermes desktop app, including any tray icon, and run
  the settings command again.
- **"Hermes output changed" warning.** The dependency cleanup could not read
  its helper's output. The update itself may still be fine. Please open an
  issue with the log lines around the warning.
  Other places that read Hermes output do not warn if the wording changed;
  if a step shows "unknown" and you think it should not, say so in an issue.
- **Settings still missing after `migrate-settings`.** Run it with
  `--dry-run` and read the first line (`from X to Y`). If the target is
  wrong, pass `--origin http://127.0.0.1:PORT`. Go back with
  `revert-settings`.
- **Garbled symbols in the progress display.** On Windows use Windows
  Terminal; on macOS and Linux use a terminal with a UTF-8 locale and a font
  that has the symbols. Or run with `--plain`, or change `theme.glyphs`.

## License

MIT, see [LICENSE](LICENSE). Licence texts for the bundled Go modules are in
[docs/third-party.md](docs/third-party.md). Hermes Agent is a separate project by Nous
Research, also MIT licensed. Not affiliated with or endorsed by Nous
Research. See [NOTICE](NOTICE).

This project is built with AI assistance. Every change is run and checked by
the maintainer; see [CONTRIBUTING.md](CONTRIBUTING.md) for the same rule for
contributions.

[Contributing](CONTRIBUTING.md) | [Security](SECURITY.md) | [Changelog](CHANGELOG.md) | [Design](docs/design.md)
