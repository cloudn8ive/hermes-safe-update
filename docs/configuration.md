# Configuration

hermes-safe-update works without any configuration. To change something,
copy [`safe-update.example.yaml`](../safe-update.example.yaml) to

- Windows: `%LOCALAPPDATA%\hermes\safe-update.yaml`
- macOS and Linux: `~/.hermes/safe-update.yaml`

or pass `--config FILE`. Keep only the keys you change.

Rules:

- Indent with spaces. Tabs are an error.
- Quote string values (`"no"`, `"on"` and `"1.10"` are read as a boolean or
  a number by some YAML tools).
- An unknown or misspelled key is an error that names the line, and the run
  exits with code 64. A `--config` file that does not exist is also an error.
  A missing default file just means defaults.
- Maps and sections (`step_seconds`, `hues`, `section_icons`, `colors`,
  `glyphs`) merge with the defaults key by key. Lists (`keep_new`, `union`, `hooks`) replace
  the default list.
- Durations are written as numbers with the unit in the key name (`_s` =
  seconds).

Order of precedence, lowest to highest: built-in defaults, `safe-update.yaml`,
`safe-update.json` (machine state the tool writes), environment
(`HERMES_SAFE_UPDATE_PLAIN`, `NO_COLOR`, `TERM=dumb`), command-line flags.

`safe-update.json` sits next to the YAML file and holds facts about this
machine (the mirror location, "never offer the mirror"). The tool writes it.
Do not copy it between machines.

## paths

Empty means detect. `~`, `$VAR`, `${VAR}` and `%VAR%` are expanded in these
three fields only.

| Key | Default | Meaning |
|---|---|---|
| `hermes_home` | `%LOCALAPPDATA%\hermes` (Windows), `~/.hermes` (macOS, Linux) | Hermes data folder. The `HERMES_HOME` environment variable is ignored on every OS, on purpose: the update always targets the default install, and the tool removes `HERMES_HOME` from everything it starts (as the launcher shims do). For a home elsewhere set this key (and `checkout`); it only tells the tool where to look. |
| `checkout` | `<hermes_home>/hermes-agent` | The source checkout. |
| `user_data` | `%APPDATA%\Hermes`, `~/Library/Application Support/Hermes`, `~/.config/Hermes` | The desktop app's data folder (holds `Local Storage`). |

## tunables

| Key | Default | Meaning |
|---|---|---|
| `min_free_gb` | 5 | Pre-flight stops below this much free disk (decimal GB) on the Hermes drive. |
| `idle_window_s` | 180 | A session active within this many seconds counts as busy. |
| `idle_poll_s` | 60 | How often busy sessions are re-checked while waiting. |
| `idle_wait_max_s` | 1800 | Give up (nothing changed, exit 3) after waiting this long. |
| `confirm_timeout_s` | 30 | The final "Press Y" prompt aborts after this. `--delay N` overrides it. |
| `graceful_close_s` | 30 | How long the desktop app gets to close before it is forced. |
| `update_timeout_s` | 5400 | Hard cap for `hermes update` (90 minutes); then its whole process tree is stopped. |
| `silent_warn_s` | 600 | Note once in the log when the update prints nothing for this long. |
| `verify_wait_s` | 120 | How long to wait for the gateway to report the new code. |
| `marker_refresh_s` | 120 | How often the update-in-progress marker is rewritten (Hermes treats a marker older than 20 minutes as stale). |
| `estimate_window` | 5 | Time estimates use the mean of the last N matching runs. |
| `estimate_overrun_k` | 0.5 | Once a step runs over its estimate, expect k times the elapsed time more. |
| `estimate_class_damp` | 0.15 | How strongly a slow network or machine today scales the later steps. |
| `estimate_class_min`, `estimate_class_max` | 0.8, 2.5 | Clamp for that scale factor. |
| `history_keep` | 40 | Timing records kept (full updates and other runs are counted separately). |
| `step_seconds` | see example file | Seed estimate per step, used until timing history exists. Keys: `local remote procs wait close gateway_stop fetch pull pydeps frontend desktop package finalize restart verify settings relaunch cleanup cua hooks sessions gc_preview cua_status`. `wait: 0` means "waiting on you", never estimated. |

## theme

Cosmetic only; nothing in the update depends on it.

| Key | Default | Meaning |
|---|---|---|
| `margin` | 2 | Columns of left margin. |
| `label_width` | 15 | Width of the key column in key/value rows. |
| `status_width` | 50 | Fixed width of the status text right of the progress bar. |
| `max_width` | 400 | Never draw wider than this. |
| `bar_band` | 6 | Gradient colour bands, in cells (fewer bands avoid hairlines in the classic console). |
| `bar_from`, `bar_to` | `[95, 175, 255]`, `[95, 215, 135]` | Gradient start and end, RGB. |
| `date_top_right` | true | Show the date at the top right. |
| `taskbar` | true | Taskbar or terminal progress where supported: the taskbar button (Windows console) or Windows Terminal's tab progress. On macOS and Linux it has no effect unless you set the environment variable `HERMES_SAFE_UPDATE_OSC_PROGRESS=1`; the tool then writes the OSC 9;4 progress sequence to a terminal on stderr, for terminals that understand it (others ignore it). |
| `flash` | true | Flash the taskbar button when input is needed or the run ends and the window is not in front. Windows only: on macOS and Linux the tool cannot tell whether the terminal is in front, so it never flashes (or rings the bell). |
| `title_update`, `title_check` | `"Hermes Safe Update"`, `"Hermes Update Check"` | Window titles. |
| `colors` | see example | SGR parameters, for example `"38;5;80"`. Keys: `accent ok warn err dim faint soft label token neutral track prompt`. `NO_COLOR` removes all colour. |
| `glyphs` | see example | The characters used for status marks, bars and rules. The defaults are single-width characters chosen to exist in the default Windows console font (Consolas); on macOS and Linux your terminal font decides how they look. Keys: `ok err warn info command active pending dot bar rule rule_thin sep gutter arrow title section`. |
| `hues` | see example | Per stage group: `bright` and `dark` colour index (xterm-256) and an `icon`. |
| `section_icons` | see example | Section title prefix to icon. |

## mirror

See "Local mirror" in the README.

| Key | Default | Meaning |
|---|---|---|
| `path` | `""` | Fallback location only. A mirror set up with `mirror setup` is recorded in `safe-update.json`. |
| `offer` | true | Offer to set up a mirror after a run (never with `--unattended`, never in a pipe). |
| `offer_timeout_s` | 45 | The offer prompt gives up after this. |
| `cron_schedule` | `"every 4h"` | Schedule of the Hermes cron job that refreshes the mirror. |
| `job_name` | `"Hermes update mirror refresh"` | Name of that job. |
| `keep_free_gb` | 5 | A mirror location needs repository size x (1 + `growth`) + this much free. |
| `growth` | 0.5 | Growth allowance in that formula. |

## settings

See [settings-migration.md](settings-migration.md).

| Key | Default | Meaning |
|---|---|---|
| `auto_migrate` | true | Back up and migrate desktop settings after every verified update. |
| `backup_retention` | 3 | Keep the newest N settings backups. |
| `backup_dir` | `<hermes_home>/backups` | Where backups go. |
| `origin` | `""` | Target renderer address. Empty = detected from the installed build. `--origin URL` overrides both. |
| `keep_new` | 5 patterns, see example | Live-state keys: the new address's value always stays. |
| `union` | 5 patterns, see example | Session-keyed maps and pair lists: merged, new wins per entry. |

In key lists `*` is the only wildcard (any other glob character is rejected
when the file loads). Keys in neither list are plain preferences.

## desktop

What happens to the desktop app after a run.

| Key | Default | Meaning |
|---|---|---|
| `relaunch` | `"auto"` | `"auto"`: reopen the app only if it was running when the update started. `"always"`: also reopen it after a **verified** update when it was already closed at the start (for example you closed Hermes yourself a moment before). `"never"`: leave it closed. Any other value is a configuration error (exit 64) that names the line. |

`--no-relaunch` overrides all three for one run. `--unattended` runs only
give back what they closed: with `"auto"` or `"always"` an app they closed is
reopened as before, an app that was already closed stays closed (`"always"`
changes nothing there), and `"never"` leaves it closed. `"always"` never
reopens the app after an update that is not verified or that was cancelled
before anything was closed, and does nothing on an install that has no
desktop app.

When the app is not reopened, the summary card has a `Hermes app` row that
says why; when it was reopened there is no such row (the `Time` row says
"until Hermes reopened").

## update

Which Hermes update channel a run follows. Hermes has two source channels:
`stable` (the latest published GitHub release, installed as a detached
checkout) and `main` (every commit). Hermes itself picks one per install; Safe
Update either follows that choice or overrides it for its own runs. A run
never writes Hermes' setting. The one way Safe Update changes it is the
explicit command `hermes-safe-update channel set stable|main`, which asks
Hermes to do it (`hermes update --set-channel`). `hermes-safe-update channel`
shows Hermes' channel, this setting and what the next run will do.

| Key | Default | Meaning |
|---|---|---|
| `channel` | `"follow"` | `"follow"`: use the channel Hermes resolves for this install: its per-install record (Settings, About, or `hermes update --set-channel`), else `stable` for a checkout of the official GitHub repo and `main` for a fork or mirror origin. When that is `main` the run is exactly as before channels existed (`hermes update --yes --keep-stash --branch main`); for `stable` the run passes no channel flag and Hermes' own `--check` says whether a release is waiting. `"stable"`: always update to the latest release (`--channel stable`); a Hermes without release channels (older than v0.21.6) stops in the pre-flight, exit 4, before anything is closed. `"main"`: always the tip of main (`--branch main`), whatever Hermes' channel is; works on every Hermes version. Any other value is a configuration error (exit 64) that names the line. |

`--channel follow|stable|main` on `run` and `check` overrides the key for one
run.

On a release channel, Hermes' check is the only judge of whether a release is
waiting. If you never chose a channel in Hermes, a checkout that is already
newer than the latest release is up to date ("waits for the next release",
exit 0). If stable was chosen explicitly (a record in Hermes, or
`update.channel: stable` / `--channel stable` over a main record or a fork
origin), that checkout would be moved back instead (see below). `Could not
resolve the stable source channel` (GitHub
unreachable or rate limited) is a pre-flight stop, exit 4, with Hermes' reason.
Main's commit mirror and GitHub comparison are not used for a release channel.

If the checkout is *newer* than the selected release, updating would move it
**back**. Hermes says so itself from v0.22.0 ("newer than the selected
release"); Hermes v0.21.6 and v0.21.7 call such a release "available", so Safe
Update also asks git whether the release commit is an ancestor of the
checkout. Safe Update stops before closing anything: an attended run asks
for an explicit `Y` that names both versions (`--yes` does not skip this
question; any other key or a timeout stops with exit 4), and `--unattended`
never rolls back (exit 4; the message names the command to do it by hand). If
the release commit is not in a shallow checkout, a move back cannot be ruled
out and the run asks (unattended: stops) the same way.

If `update.channel` is `stable` or `main`, Safe Update keeps using it whatever
Hermes is set to, including after `channel set`; the command says so. To
follow Hermes again, set `follow` or remove the line.

## hooks

A hook runs your own command at a fixed point of a run.

| Field | Meaning |
|---|---|
| `name` | Label shown on the summary card. |
| `when` | `pre-close`: at the start of the close stage, after you confirmed, before Hermes is closed. `post-update`: after a verified update, before Hermes reopens. `post-relaunch`: after Hermes reopened (runs whatever the result). |
| `run` | The command as a list: program first, then arguments. No shell is involved unless you name one as the first item. |
| `timeout_s` | Default 300. A hook that takes longer is stopped (with its process tree). |
| `dir` | Working directory. Default: the Hermes home. |

Each hook gets these environment variables. All five are always set; a value
the tool does not know is the empty string (test with `-z`), never a missing
variable. Paths are absolute or empty.

| Variable | Value |
|---|---|
| `HERMES_SAFE_UPDATE_STAGE` | The hook's `when` value: `pre-close`, `post-update` or `post-relaunch`. |
| `HERMES_SAFE_UPDATE_RESULT` | `verified`, `problem` or `unknown`. `pre-close` hooks always see `unknown`. |
| `HERMES_SAFE_UPDATE_HOME` | The Hermes home: the root folder (`paths.hermes_home` or the default), not a profile folder. Profiles are in `<home>/profiles`. |
| `HERMES_SAFE_UPDATE_CHECKOUT` | The source checkout this run updates (`paths.checkout` or `<home>/hermes-agent`). |
| `HERMES_SAFE_UPDATE_PYTHON` | The Python interpreter that Hermes installs for itself (Windows: `<home>\tools\python-*\python.exe`; macOS and Linux: `<home>/tools/python-*/bin/python3`, else `<home>/tools/python-*/bin/python`; a guess that nobody has checked on those systems). It is a bare interpreter: Hermes' own packages are not on its path by default. Empty when none is found. |

`HERMES_SAFE_UPDATE_PYTHON` is looked up on disk when each hook starts, not
once at the start of the run. `hermes update` installs a new interpreter in a
new, versioned folder, so a `post-update` or `post-relaunch` hook gets the
interpreter that exists after the update, and a `pre-close` hook the one from
before. Do not cache or hard-code this path.

`HERMES_HOME` is not passed on: the tool removes it from every command it
starts (see `paths.hermes_home`). Use `HERMES_SAFE_UPDATE_HOME` instead.

These variables are only ever added, never renamed or removed, so a hook
written today keeps working. A hook must ignore variables it does not know.

The last non-empty line the hook prints becomes its row on the summary card.
A non-zero exit or a timeout is a warning; it never turns a verified update
into a failure and never aborts the run.

Example: re-apply a custom Start menu tile after each update. This is only
an illustration of a `post-update` hook. Point it at your own script.

```yaml
hooks:
  - name: "restore-start-tile"
    when: "post-update"
    run: ["powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "C:\\Users\\you\\scripts\\restore-tile.ps1"]
    timeout_s: 120
```

In YAML double-quoted strings a backslash must be doubled, as above. Single
quotes avoid that: `'C:\Users\you\scripts\restore-tile.ps1'`.

On macOS and Linux the command is an argument list too, so name the shell or
give a script that has the exec bit and a `#!` line, with absolute paths:

```yaml
hooks:
  - name: "after-update"
    when: "post-update"
    run: ["/bin/sh", "/home/you/scripts/after-update.sh"]
    timeout_s: 120
```

`~`, `$VAR` and `${VAR}` are not expanded in `run` or `dir` (nothing but
`paths.hermes_home`, `paths.checkout` and `paths.user_data` is), so write
`/home/you/...`, not `~/...`. A shell would expand them only if the script
does it itself. There is no hooks folder: a hook is whatever the list names.

The script's last printed line (for example `tile restored`) shows up on
the card. Hooks are run by the tool with your permissions; only configure
commands you trust.

## logging

| Key | Default | Meaning |
|---|---|---|
| `level` | `"info"` | `debug`, `info`, `warn` or `error`. `--log-level` overrides. |
| `max_size_mb` | 5 | Rotate the log file at this size. |
| `keep` | 3 | Rotated files kept. |

The log is `<hermes home>/logs/safe-update.log`. Your home folder is written
as `~` and the local app-data folder as `%LOCALAPPDATA%`. Session titles and
ids and the mirror folder are never written to it.
