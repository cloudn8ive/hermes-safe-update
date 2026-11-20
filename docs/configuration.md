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
| `confirm_timeout_s` | 600 | The final "Press Y" prompt aborts after this (10 minutes; the prompt and the footer count in minutes from two minutes on, in seconds below that). `--delay N` overrides it. If the answer comes more than 60 seconds after the prompt appeared, busy sessions are checked once more before Hermes closes; if a chat is busy again, Safe Update goes back to waiting for idle (A to abort, F to close anyway; the idle limit starts again) and then asks for `Y` again. That check is not repeated after F, `--yes` or `--unattended`. Hermes' checkout lock is looked at again in every mode, right before Hermes closes, whenever the last look is more than 60 seconds old (a long prompt wait, a long wait for idle sessions, F, `--yes`, `--unattended`); if an update has started meanwhile (the lock is held), the run stops with exit 4, nothing closed. |
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
| `older_release` | `"wait"` | What a run on the stable channel does when the latest release is not newer than your build. `"wait"`: Safe Update never moves Hermes back. A release that is an ancestor of your build, diverged from it, or that git and GitHub cannot prove to contain it (a shallow clone, the release commit not in the checkout and GitHub unable to compare it, an unknown installed commit) ends the run like an up-to-date one: exit 0, "Waiting for stable: ...", nothing changed, nothing asked, no repair offer, also with `--unattended` and `check`. A release that contains your build is updated to as usual. While it waits, `repair`, `--force` and finishing an unfinished update are refused (exit 4, nothing changed) because each would run `hermes update` for the stable channel, which moves Hermes back with no question; the message names `--channel main` for one run and says that this also updates Hermes to the newest main commits. `"ask"`: the older behaviour: an attended run asks for an explicit `Y` that names both versions, `--unattended` and `check` stop (exit 4). `--allow-rollback` on `run`, `repair` and `check` is `"ask"` for one run. Applies to the stable channel only (`update.channel: stable`, `--channel stable`, or `follow` resolving to stable); main never waits. Any other value is a configuration error (exit 64). |

`--channel follow|stable|main` on `run` and `check` overrides the key for one
run. `--allow-rollback` on `run`, `repair` and `check` sets `older_release: ask`
for one run.

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
checkout. With `older_release: wait` (the default) Safe Update does not stop and
does not ask: it waits for stable, as the `older_release` row above says. With
`older_release: ask` Safe Update stops before closing anything: an attended run asks
for an explicit `Y` that names both versions (`--yes` does not skip this
question; any other key or a timeout stops with exit 4), and `--unattended`
never rolls back (exit 4; the message names the command to do it by hand). If
the release commit is not in a shallow checkout, a move back cannot be ruled
out and the run asks (unattended: stops) the same way.

If `update.channel` is `stable` or `main`, Safe Update keeps using it whatever
Hermes is set to, including after `channel set`; the command says so. To
follow Hermes again, set `follow` or remove the line.

## preflight

Checks that run before Hermes is closed. A stop ends the run with exit 4 and
nothing changed. All of them only read; none installs or repairs anything.
`check` runs the same probes, so it shows the same stops and exits 4 on a held
lock or an unreadable `tools\` entry (the administrator terminal only warns
there). The lock, elevation and access tests are Windows only; on macOS and
Linux they do nothing (the log says "checkout lock: not probed" and "elevation
check unavailable"; the access walk finds nothing).

| Key | Default | Meaning |
|---|---|---|
| `acl_check` | `"stop"` | Safe Update opens each entry of Hermes' `tools\` folder (and one level below) with your own account, plus a few other folders the update writes to. An entry you cannot open was left behind by an elevated install or update (owner Administrators, no usable permission for your account); the next update would fail on it with `[WinError 5]`. `"stop"`: end the run when an entry under `tools\` cannot be opened for reading, and print the two commands that give it back for each of the first 5 entries (`takeown /f "<entry>" /r /d y`, then `icacls "<entry>" /reset /t /c /q`; run them in an administrator terminal; on a non-English Windows replace `y` with that language's yes letter, `j` on German). The stop message also names the way past it: `preflight.acl_check: "warn"` in `safe-update.yaml` (the update is then expected to fail on those entries). An entry that is refused is opened a second time after about 400 ms (once for all of them), and only a refusal that repeats counts, because Windows also refuses a file for a moment while it is being deleted or scanned. If the walk hits its limit (400 opens or 2 seconds), it says "ACL check stopped early after N opens; deeper entries were not looked at". `"warn"`: print the same and go on. `"off"`: skip. Entries outside `tools\` that cannot be opened, and entries you can read but not change, are always only a warning. The test is a real open, never a guess from owner or permission entries, and it is shallow: damage deep inside `node_modules` is not seen. |
| `arm64_toolchain` | `"warn"` | Only when Hermes' managed Python is the `win32-arm64` one (the folder name under `tools\`; Safe Update does not look at its own build), an update is coming, and Python dependencies are expected to be rebuilt (or Safe Update cannot tell). Hermes then needs the Visual Studio ARM64 C++ tools and `clang.exe`, and starts the Visual Studio Build Tools installer (several GB, an administrator prompt) when either is missing, after Hermes was closed. Safe Update repeats Hermes' own lookups (`clang.exe` on `PATH`, in `Program Files\LLVM\bin` and in the Visual Studio LLVM folders, and `vswhere`). `"warn"`: say so and go on. `"stop"`: end the run before anything is closed. `"off"`: skip. If the lookup itself fails, nothing is reported. Safe Update installs nothing. |

Two more stops need no setting:

- **Running as administrator.** From an elevated terminal (or a task set to
  run with highest privileges) `run` stops with exit 4 before closing anything:
  an elevated update leaves files in `tools\` that your normal account cannot
  update later. `--allow-elevated` goes on anyway. `check` only warns. A
  standard user, or an administrator with UAC off, is not "elevated" here.
- **Checkout lock held.** `hermes update` holds a lock on the checkout
  (`hermes-update.lock` in the git directory, or `.hermes-update.lock` in the
  install folder when there is no `.git`) for its whole process tree.
  When a process still holds it (another update, or a leftover child of a
  killed one), Safe Update stops with exit 4 before closing anything and says
  who holds it, instead of closing Hermes and letting `hermes update` refuse.
  It only tries the lock and gives it back at once; it never creates the
  file and never stops a process. If the lock file exists but cannot be
  opened, that also stops (`hermes update` would refuse too). If the probe
  cannot be asked at all, the run goes on.

## extras

The parts of a Hermes install that do not depend on the Hermes version:
catalog plugins, hub-installed skills and the tools Hermes installs for itself
(git, node, ...). After the Hermes verdict of every run (up to date, waiting
for stable, `check`, and after a verified update) Safe Update looks at them,
through Hermes' own check commands and in parallel (Safe Update writes
nothing itself; Hermes may refresh its own caches while it answers), shows one
row each on the card and writes the detail to the log. It never touches Hermes' own checkout. Toolchain, Python
and Node pins live in the checkout and are never updated here.

| Key | Default | Meaning |
|---|---|---|
| `plugins` | `"ask"` | Catalog and git plugins with an update (`hermes plugins check-updates --json`). Plugins you copied in by hand have no source and are never touched. `"ask"`: list them and, on an attended run (a console, not `--unattended` or `--yes`, not inside a Hermes chat, not `check`, and this run's Hermes verified or a clean "up to date"), ask for each plugin (y/N, default no; the memory provider is named, e.g. `Update holographic (your memory provider) 46b6f90 -> ff558dd? [y/N]`, and goes last). Only Y updates; any other key, the timeout (`tunables.confirm_timeout_s`) and Ctrl+C skip that plugin. A plugin is updated through `hermes plugins update <name>` (never `--force`; Hermes' security scan still applies and, with no terminal, refuses a caution result or a plugin that adds dependencies or capabilities: the row then says to run it in a terminal). Hermes' own plugin update is all-or-nothing (it stages and builds the new version and publishes only on success) and never evicts a plugin, so Safe Update writes **no file** around it: it checks the exit code, that the plugin is still enabled and that `memory.provider` is unchanged, and a problem is a WARN row naming the plugin and the command to run. The update step keeps its own time budget (15 min per plugin) and is not cut by Ctrl+C; its timeout stops the whole `hermes` process tree. **The plugin guard:** the eviction of the 22 Oct happens inside `hermes update` (and its follow-ups and repairs), where Hermes' dependency rebuild switches a plugin off and clears `memory.provider`. Safe Update reads `plugins.enabled`, `plugins.disabled` and `memory.provider` in the pre-flight (before anything is closed) and again after any run that ran `hermes update`. A plugin that was already disabled before the run, or a provider you changed before it, is not an eviction. If Hermes switched a plugin off or cleared or changed the provider, the card says so (`Plugins  Hermes switched off holographic during the update (memory provider cleared)`), Hermes' own reason line goes to the log, and an attended run on a verified Hermes asks once (Y only) to put it back with Hermes' own commands: `hermes plugins enable <name>` (it re-admits the plugin through the dependency manager and fails atomically if the environment still cannot build it; that stays a WARN with the reason), then `hermes config set memory.provider <old>` only if the enable worked and the provider was cleared from that plugin. `--unattended`, `--yes`, `check` and `"report"` only report and print the commands. Safe Update never edits `config.yaml`. `"report"`: list only. `"off"`: do not look (the guard is off too). |
| `skills` | `"ask"` | Hub-installed skills with an update (`hermes skills check`), applied with `hermes skills update` after a Y (skills you edited are kept by Hermes; `--force` is never passed). Same modes as `plugins`. |
| `tools` | `"report"` | Faults `hermes pm doctor` finds in the bundled tools, shown as one row (the advice is on screen and in the log, naming `hermes pm install <name>` only for the fault classes that command fixes: not installed, outdated, failed verification). A digest mismatch (`realized bytes do not match recorded digest`) usually means stray files in the tool's folder; Hermes records a digest, not a file list, so Safe Update cannot name the files. In `check` only, `hermes pm update --check` is run as well and the newer tool versions are listed (it needs the network and takes about a minute, and may hit GitHub errors). Listing only: `hermes pm update` rewrites core files and is never run. `"report"` or `"off"`. |
| `stale_days` | `14` | On a release channel (stable): a row `Stale stable` when the latest stable release is older than this many days, or when commits whose subject contains `CVE-<digit>`, `GHSA-` or `security` (case-insensitive, whole word start) are on main since the release. The release date and the subjects are read with git only: the local mirror's `main` when there is one, else the checkout's `origin/main` as of its last fetch; nothing is fetched. The row names the one-time way out, `hermes-safe-update --channel main` (updates Hermes to the newest main commits; afterwards the hold waits until stable passes it). Nothing happens by itself. `0` turns the row off. |

The extras stage never changes the exit code; a plugin that Hermes switched
off, a refused update or an unreadable answer is a WARN and a row.

**Pinned plugins (v0.8.1).** A plugin installed with `--ref` is never offered.
Safe Update reads `<hermes home>/plugins/.install-metadata.json` (read-only;
same shape in Hermes v0.22.7 and v0.23.0): `catalog.pin` empty with `pinned:
true` is Hermes' `held_with_ref` (a catalog install off the reviewed pin; a
catalog install at the reviewed pin also says `pinned: true`, with the pin set,
and is updated normally), and `pinned: true` on a git install is refused by
Hermes itself. Both show `<name> pinned to <ref> (not updated; ...)`. A record
that is missing, unreadable, or of an unknown shape is "unknown" and the plugin
is skipped with a note (fail toward not touching). This includes a git plugin
with no entry in the install record: Hermes would treat it as not pinned, Safe
Update is more cautious and does not touch it. A record without a `pinned`
field counts as not pinned, as in Hermes (`pinned is True`). The record is read
again right before `hermes plugins update <name>`; a plugin found pinned then
is shown like the pinned row above (not a failure).

**Catalog notice (v0.8.1).** The end-of-update notice of Hermes v0.23.0 ("N
plugin update(s) from the new catalog in this profile: a, b", then an "apply:"
line) is read, logged and hidden from the screen, and shown once in the Plugins
row. Questions only ever come from the plugin probe; a name only the notice has
gets the command, never a question. `extras.plugins: off` or a run without the
extras stage shows the notice in a Plugins row of its own.

Every
command runs with a time limit (probes 90 to 150 s, one plugin update 15
min) and every `hermes` child is started so that a timeout stops its whole
process tree. Where it runs: for `check` and for runs that end early (up to
date, waiting for stable) at the end, before the card, on the run's own
context (not inside the five-minute clean-up window); for an update after the
relaunch and the cleanup, so the time Hermes is closed is not stretched by
probes or a question. The plugin guard's question comes first, right after the
cleanup. Plugin settings of other Hermes profiles are not looked at (only the
home Safe Update found).

## statedb

Hermes' pre-update snapshot (`hermes update`) leaves out any file over 1 GiB
(`_PRE_UPDATE_SNAPSHOT_MAX_FILE_SIZE`) and prints `! Snapshot: skipping
state.db (1.2 GB exceeds 1.0 GB limit)` and `! Skipping snapshot prune: DB
file(s) skipped for size: state.db`, once per profile. An install whose session
history (`state.db`, the home's and each profile's) is bigger is therefore
updated with no copy of it. Safe Update does three things about it:

1. **Probe, every run (`check` included).** The size of `state.db` in the home
   and in `profiles/*`. Over 1 GiB gives the row `state.db  work 4.6 GB,
   default 1.3 GB over Hermes' 1 GiB snapshot limit; ...`. After `hermes update`
   its own skip lines are read as a second signal: a skipped file with no copy
   from Safe Update is a row.
2. **Compaction, attended runs only, where Hermes has work to do.** Before
   asking, Safe Update reads each database over the limit **read-only**
   (`mode=ro`, `query_only`, 3 s busy timeout) and mirrors Hermes'
   `fts_optimize_available()` (v0.23.1, `hermes_state_search.py:592-604`; v0.23.3,
   `persistence/sqlite/admin/_maintenance.py:388-402`):
   an older FTS layout (`fts_layout_drift`), `state_meta.fts_rebuild_high_water`
   (an interrupted optimize), a table named `fts_v22_trash_*`, or `messages`
   rows with an empty `messages_fts_docsize`. None of those: the index is
   already compact, compaction would free nothing, so there is no question and
   no gateway stop (the row says "search index already compact"; the own copy
   below is still made). If the probe cannot tell (locked or unreadable file,
   a schema it does not know, `fts_stale`, Hermes 0.23.0's CJK markers, or a
   Hermes other than release 0.23.0 to 0.23.3 (`+canary.*` builds of those included), or a derived `+N.g<sha>` build) the result is "unknown" and the question is
   asked as before, saying it could not tell: a needless question costs a
   minute, a wrongly hidden one leaves a database over the limit. One Y/N question (asked before Hermes is
   closed, 2 minutes to answer, no answer = no) lists the databases, their sizes,
   a rough time (a guess from the size, 12 min per GiB, not measured) and, per
   database, whether the disk allows a verified copy first. On Y, after Hermes
   is closed and before `hermes update`, the gateway is stopped (it holds the
   databases, and Hermes refuses while any process does) and
   `hermes [-p <profile>] sessions optimize-storage --yes` runs per profile,
   smallest first, with stdin closed, its own timeout (10 min + 12 min per GiB,
   at most 6 h) and its output on screen. **A verified copy is made right
   before each compaction** when the disk allows it; if not, the question and
   the row say "no copy before compaction". The pre-compaction copy is kept (it
   is the only state from before the rewrite); a compacted database that is
   still over 1 GiB and has no copy from this run gets one afterwards. The
   gateway comes back as after any update (the verify step, or the end of an
   aborted run). Before the update, because Hermes' own snapshot then sees a
   database that may be under the limit again, and because the code that does it
   is the version the gate looked at. **Disk:** Hermes builds the new index
   before it drops the old one and then VACUUMs (a temporary copy plus the new
   file through the write-ahead log), so the peak is about twice the file; a
   database is skipped when free - 2 x size (- the copy, for a copy first) would
   drop below `min_free_gb`. The estimate is logged (`compaction disk
   estimate`). Hermes' own check (about one file's size) runs too. Hermes'
   refusal text (the holder list) is shown and logged; `--force` is never
   passed. A refusal, failure, timeout, "not enough disk" or a VACUUM that
   Hermes skipped (it still exits 0: the index was rebuilt but no space came
   back) is an attention row and a WARN and never fails or skips the update.
   Ctrl+C during it ends the run like any Ctrl+C (nothing updated, Hermes
   reopened); Hermes' steps resume next time, but its last step (VACUUM) is one
   statement that cannot be interrupted, so stopping can take a while (the
   screen says "waiting for Hermes to finish its current step"). A timeout
   resumes the index work; a VACUUM that was killed starts over. **Needs Hermes
   0.23.0 or newer** (`hermes --version`; a derived version such as
   `v0.22.7+300.g...` counts as 0.22.7; an unreadable version is "not capable")
   and a Hermes that has the `sessions optimize-storage` command and a known
   launcher. On an older Hermes there is no question, only a row. A
   `profiles/default` folder (Hermes reads "default" as the home itself, so
   `-p default` cannot reach it) is labelled `profiles/default` and is not
   compacted. Profile folders whose name is not a Hermes profile id
   (`[a-z0-9][a-z0-9_-]{0,63}`) are left out (logged).
3. **Own copy, attended or not.** Every database over 1 GiB that is not
   compacted (answer N, no answer, unattended, `--yes`, `compact: off`, an older
   Hermes) is copied **before Hermes is closed**, so the copy does not lengthen
   the downtime, with SQLite's `VACUUM INTO` on a read-only connection to
   `<hermes home>\backups\safe-update-statedb\<profile>-<yyyymmdd-hhmmss>.db`,
   checked with `PRAGMA quick_check`, and only then is the older copy of that
   profile removed (the newest is kept). The source is only read; a long copy
   keeps one read transaction open, so the write-ahead log grows meanwhile. That
   is harmless in WAL mode (Hermes' normal mode). If `PRAGMA journal_mode` is
   not `wal` (Hermes falls back to `delete` on some file systems) and Hermes has
   the database open, the copy is skipped with a row, because its read lock
   would block Hermes' writes. The disk need counts the file, the `-wal` next to
   it and 64 MiB. A copy that would leave less than `min_free_gb` free is not
   made, and the row says so. It runs in unattended runs too: it is a backup, not
   a change. A copy that fails is a WARN and a row; the update goes on.

| Key | Default | Meaning |
|---|---|---|
| `compact` | `"ask"` | `"ask"`: the question above (attended, Hermes 0.23+). `"off"`: never. |
| `backup` | `"on"` | `"on"`: the own copy. `"off"`: none. |
| `min_free_gb` | `10` | Decimal GB that must stay free on the Hermes drive after a copy or compaction (at least 1). |

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
