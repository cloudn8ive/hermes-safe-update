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
| `hermes_home` | `%LOCALAPPDATA%\hermes` (Windows), `~/.hermes` (macOS, Linux) | Hermes data folder. On Windows the `HERMES_HOME` environment variable is ignored on purpose: the update always targets the default install. |
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
| `taskbar` | true | Taskbar or terminal progress where supported. |
| `flash` | true | Flash the taskbar button when input is needed or the run ends. |
| `title_update`, `title_check` | `"Hermes Safe Update"`, `"Hermes Update Check"` | Window titles. |
| `colors` | see example | SGR parameters, for example `"38;5;80"`. Keys: `accent ok warn err dim faint soft label token neutral track prompt`. `NO_COLOR` removes all colour. |
| `glyphs` | see example | Characters the default Windows console font (Consolas) has. Keys: `ok err warn info command active pending dot bar rule rule_thin sep gutter arrow title section`. |
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

## hooks

A hook runs your own command at a fixed point of a run.

| Field | Meaning |
|---|---|
| `name` | Label shown on the summary card. |
| `when` | `pre-close`: at the start of the close stage, after you confirmed, before Hermes is closed. `post-update`: after a verified update, before Hermes reopens. `post-relaunch`: after Hermes reopened (runs whatever the result). |
| `run` | The command as a list: program first, then arguments. No shell is involved unless you name one as the first item. |
| `timeout_s` | Default 300. A hook that takes longer is stopped (with its process tree). |
| `dir` | Working directory. Default: the Hermes home. |

Each hook gets two environment variables: `HERMES_SAFE_UPDATE_STAGE` (the
`when` value) and `HERMES_SAFE_UPDATE_RESULT` (`verified`, `problem` or
`unknown`; `pre-close` hooks always see `unknown`). The last non-empty line
the hook prints becomes its row on the summary card. A non-zero exit or a
timeout is a warning; it never turns a verified update into a failure and
never aborts the run.

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
