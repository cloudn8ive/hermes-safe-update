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
| Windows 10 | amd64 | unit-tested; one real unattended update (started by a scheduled task) ran and verified on Windows 10 22H2 amd64, and the Start-menu and Ctrl+K launches were tried. The settings migration was run in a sandbox on a copy of a real desktop settings store and in an automated test on synthetic data, both with the real Electron. On the real update the settings step failed, because that Hermes range removed renderer-server.ts; this was fixed afterwards (file:// detection, backup when detection fails) and covered by unit tests and a read-only dry run, but no real settings migration has been run since the fix |
| Windows 11 | amd64 | expected to work; not yet tested |
| Windows 10 / 11 | arm64 | builds; not run on arm64 hardware |
| macOS | arm64, amd64 | **untested, proceed at your own risk** |
| Linux | amd64, arm64 | **untested, proceed at your own risk** |

All targets compile (cross-compiled and vetted). The unit tests have so far
been run on Windows only; CI will run them on macOS and Linux once the
repository is published. Nobody has run an update with it on macOS or Linux.
There it prints a warning and asks you to confirm before it does anything. Pass `--accept-untested` to
skip the question (required together with `--unattended`). Reports from
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
4. It warns and asks you to confirm before it does anything (see
   [Platforms](#platforms)).

To build it yourself you need Go 1.26 or newer:
`scripts/build-all.sh v0.1.0` (git-bash, macOS, Linux) or
`powershell -File scripts\build-all.ps1 -Version v0.1.0`. Output and
`SHA256SUMS` go to `dist/`.

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
```

Or double-click the shims (`hermes-update-check.cmd` is the `--check` run). If
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
   exists: a local mirror first if you set one up, then the GitHub API, then
   `hermes update --check`. It predicts whether Python or Node dependencies
   will be reinstalled, so the time estimate is closer.
3. **Look for running processes**: desktop app, gateway, backends, kernels.
4. **Wait.** If anything is about to be stopped, it reads every profile's
   `state.db` (read-only) for sessions active in the last 3 minutes. While
   any are active it waits. You can press `A` to abort or `F` to go ahead
   anyway. Then it asks for a final `Y` (30 seconds, then it aborts).
5. **Close.** The desktop app gets a normal close request and 30 seconds to
   save its state. Only then is it force-closed, and the log says why it
   probably did not close (an open dialog, for example). Leftover backends
   and kernels are stopped.
6. **Update.** It runs `hermes update --yes --keep-stash --branch main`
   with a hard time limit (90 minutes). If Hermes' own gateway pause fails
   before changing anything, it stops the gateway and retries once.
7. **Verify.** The gateway must report the new commit and be running, and
   Hermes' own desktop-build check must pass. Hermes' known false "fleet
   version" warning does not turn a good update into a failure.
8. **Settings.** Back up and migrate the desktop settings if the address
   changed (see above). Hermes is closed at this point.
9. **Hooks, only after a verified update:** your `post-update` hooks run
   while Hermes is still closed.
10. **Reopen** the desktop app if it was running, then run your
    `post-relaunch` hooks.
11. **Housekeeping, only after a verified update:** remove old dependency
    sets, refresh the cua-driver if the update asked for it. A failure in
    a hook or here is a warning; it never turns a verified update into a
    failed one.
12. Print a summary card and exit.

The stages in more detail: [docs/how-it-works.md](docs/how-it-works.md).

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
  never rewritten. Its environment is your own, except that `HERMES_HOME` is
  removed (for every command the tool starts) and `PYTHONIOENCODING=utf-8`
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

A packaged install (macOS `Hermes.app`, Windows MSIX, AppImage, deb or rpm,
Docker, Nix) is recognised and left alone. The tool prints which updater to
use and exits with code 5. These detections are code plus unit tests on every
platform; what has been run for real is stated in the [Platforms](#platforms)
table.

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
gets `HERMES_SAFE_UPDATE_STAGE` and `HERMES_SAFE_UPDATE_RESULT` in its
environment, and its last output line appears on the summary card. A failing
or slow hook is a warning only. Example and details:
[docs/configuration.md](docs/configuration.md#hooks).

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
Backups live in `<hermes home>\backups\settings-<UTC date-time>\`, and the
newest 3 are kept (`settings.backup_retention`). Merge rules and revert:
[docs/settings-migration.md](docs/settings-migration.md).

## Known limitations

- Time estimates learn from your own past updates. The first few updates, and
  especially updates that change Node packages, can take longer than the
  estimate says. When the tool has fewer than 3 earlier runs like the current
  one, it adds "(rough: ...)" to the estimate.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | done: updated and verified, check finished, or already up to date |
| 1 | the update ran but needs attention, or an unexpected error |
| 2 | the `hermes` launcher was not found |
| 3 | cancelled before anything changed (you, a timeout, idle wait exceeded, the desktop app would not close, untested platform declined) |
| 4 | pre-flight stop: another update running, low disk, update check failed, or started from inside a Hermes chat |
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

`scripts\hermes-safe-update.cmd` runs `hermes-safe-update.exe` (found next to
the shim, else in `%LOCALAPPDATA%\Programs\hermes-safe-update`, else on
PATH), sets the window title, passes all arguments through, prints the exit
code and log location, and waits for a key. It exits with code 2 if it finds
no exe. `scripts\hermes-update-check.cmd` does the same with `--check`. Use
them as targets for a Start menu link or desktop shortcut.

### Shortcuts

Desktop shortcut:

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

## Updating the tool

1. Close the tool if it is running. Do not replace it during an update.
2. Download the new release and check its hash as in [Install](#install).
3. Replace `hermes-safe-update.exe` (and the two shims, if you use them) in
   the install folder.

Your config, the machine state and the settings backups stay as they are.

## Uninstall

Uninstalling does not touch Hermes or its settings.

1. Delete the install folder `%LOCALAPPDATA%\Programs\hermes-safe-update`
   (or wherever you put the exe and shims), and any shortcuts you made. If you
   added it to PATH, remove that entry too.
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

**Where is the log?** `<hermes home>\logs\safe-update.log`. Your home folder
appears as `~` and the local app-data folder as `%LOCALAPPDATA%`. Session
titles and ids never go into the log (the screen shows them; the log only
counts busy sessions), and neither does the mirror folder. Commit hashes,
versions and your hook names do appear. Read an excerpt before you paste it,
but it should hold no names or folders of yours.

**How long does it take?** The progress footer estimates from your last 5
matching runs, and from built-in numbers before there is any history. The
desktop build is the slow part, several minutes. With little history it says
"rough" (see Known limitations).

**Can I schedule it?** It is meant for that
(`hermes-safe-update --unattended --no-elevate`; `--no-elevate` means it
never shows an admin prompt), but it has not yet been tried as a scheduled
task.

## Troubleshooting

- **Exit 5, "not a source checkout".** Your Hermes is a packaged install, or
  the home folder is not where the tool looks. Set `paths.hermes_home`.
- **Exit 2.** The `hermes` launcher is missing from `<hermes home>\bin`.
  Check `paths.hermes_home`.
- **Exit 4.** Read the STOP line in the output or log: marker held by
  another update, less than `tunables.min_free_gb` free, the update check
  failed (offline?), or the updater was started from inside a Hermes chat
  (run it from a normal terminal or the Start menu; updating would stop the
  process that runs it. `--check` works anywhere).
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
- **Garbled symbols in the progress display.** Use Windows Terminal, run with
  `--plain`, or change `theme.glyphs`.

## License

MIT, see [LICENSE](LICENSE). Licence texts for the bundled Go modules are in
[docs/third-party.md](docs/third-party.md). Hermes Agent is a separate project by Nous
Research, also MIT licensed. Not affiliated with or endorsed by Nous
Research. See [NOTICE](NOTICE).

This project is built with AI assistance. Every change is run and checked by
the maintainer; see [CONTRIBUTING.md](CONTRIBUTING.md) for the same rule for
contributions.

[Contributing](CONTRIBUTING.md) | [Security](SECURITY.md) | [Changelog](CHANGELOG.md) | [Design](docs/design.md)
