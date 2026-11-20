# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/). Dates are UTC.

## [Unreleased]

## [0.9.1] - 2026-11-19

### Changed

- **Compaction is offered only where Hermes has work to do.** v0.9.0 offered
  `hermes sessions optimize-storage` for every `state.db` over 1 GiB, but a
  database already on the compact search-index layout has nothing to compact
  (Hermes: "Search index is already on the compact layout - nothing to do."),
  so the question, which stops the gateway, freed nothing. Safe Update now
  reads each oversized database read-only (`mode=ro`, `query_only`, 3 s busy
  timeout, never a write) and mirrors Hermes' `fts_optimize_available()`
  (v0.23.1, `hermes_state_search.py:592-604`): an older FTS layout, an
  interrupted optimize (`fts_rebuild_high_water`), leftover `fts_v22_trash_*`
  tables, or an empty external index with messages. Already compact: no
  question, no gateway stop, the row says `work 4.5 GB, default 1.3 GB
  over Hermes' 1 GiB snapshot limit (search index already compact; Safe Update
  keeps its own copy)`, and the own verified copy is made as before. Anything
  the probe cannot tell (locked or unreadable file, unknown schema, a stale
  index, Hermes 0.23.0 CJK markers, a Hermes other than release 0.23.0, 0.23.1,
  0.23.2 or 0.23.3, including derived `+N.g<sha>` builds) is
  "unknown": the compaction is offered as in 0.9.0, with "could not tell
  whether compaction helps". The schema names and keys are in
  `internal/hermes/contracts.go` with file:line citations.

### Dependencies

- Updated direct modules: go.yaml.in/yaml/v3 v3.0.5 -> v3.0.6, golang.org/x/sys
  v0.48.0 -> v0.50.0, golang.org/x/term v0.46.0 -> v0.48.0, modernc.org/sqlite
  v1.60.1 -> v1.62.0 (bundled SQLite 3.53.4 -> 3.54.1; indirect:
  go-humanize v1.1.0, go-strftime v1.1.0, modernc.org/libc v1.80.1).
  `NOTICE` and `docs/third-party.md` list the new versions.

### Security

- Built with Go 1.26.11, which fixes two reachable standard-library
  vulnerabilities (GO-2026-6859 in net/http, GO-2026-6809 in crypto/x509).

## [0.9.0] - not released separately (included in 0.9.1)

### Added

- **Session databases over Hermes' 1 GiB snapshot limit.** `hermes update` skips
  any `state.db` over 1 GiB in its pre-update snapshot (and its snapshot prune),
  per profile. Safe Update now (1) shows the sizes in every run, `check`
  included (`state.db  work 4.6 GB, default 1.3 GB over Hermes' 1 GiB
  snapshot limit`); (2) on an attended run, with Hermes 0.23.0 or newer, offers
  `hermes sessions optimize-storage` per oversized profile after Hermes is
  closed and before `hermes update`, with the gateway stopped (Hermes refuses
  while any process holds the database; its refusal and holder list are shown,
  `--force` is never passed); a refusal, failure, timeout, lack of disk or
  Ctrl+C never loses data and never makes the update fail; (3) keeps its own
  verified copy (`VACUUM INTO` on a read-only connection, `PRAGMA quick_check`,
  newest one per profile, in `backups\safe-update-statedb`) of every database
  still over the limit, on attended and unattended runs, never when it would
  leave less than 10 GB free. Older Hermes (before 0.23) is never offered the
  compaction: one row says so. New keys `statedb.compact` (`ask`|`off`),
  `statedb.backup` (`on`|`off`), `statedb.min_free_gb` (10). The wording of
  Hermes' skip lines and the version gate are in `internal/hermes/contracts.go`
  with tests. Fix round: the copy is made before the compaction (and before
  Hermes is closed when there is no compaction); the compaction's disk peak is
  counted as twice the file; a VACUUM that Hermes skipped is an attention row,
  not a success; no copy while Hermes holds a non-WAL database; the copy's disk
  need counts the `-wal`; `profiles/default` and non-profile folder names are
  handled; the question waits 2 minutes, not 10, and no longer promises that
  Ctrl+C stops at once.

## [0.8.1] - not released separately (included in 0.9.1)

### Fixed

- **A hold by Hermes itself is no longer reported as an update.** `hermes update`
  can hold the build instead of updating (Hermes v0.22.7 and v0.23.0: "This
  build is newer than the <channel> release <sha12>: that release predates the
  gateway runtime your sessions use, so there is nothing to update to yet." and
  "Holding at this build until a newer <channel> release ships.", exit 0, HEAD
  unchanged). Safe Update used to end such a run with "Hermes is updated and
  verified". Now, when Hermes printed the hold, exited 0 and HEAD did not move,
  the card says "Hermes did not update: it held this build (the stable release
  0123456789ab predates the gateway runtime your sessions use)", has no Version
  row, a `Hermes held` row names `hermes-safe-update channel set main`, and the
  exit code is 0 (nothing failed, like "Nothing to update"). Hermes still ran
  its "already up to date" tail on the same code, so the verification, the
  settings step, the cleanup, the cua refresh, the plugin guard and the extras
  (plugin, skill and tool questions) run as after any update, and Hermes is
  reopened as usual. `post-update` hooks are skipped (no new code); `post-relaunch`
  hooks run, as `pre-close` did and Hermes was reopened. The verify line says
  "running this build <sha>" instead of "running the new code". If an
  unrestored stash replaced Hermes' "Holding" line, the "This build is newer ..."
  line alone counts as the hold (same guards: exit 0, no "Update complete!",
  HEAD unmoved) and the `Hermes held` row names the stash. A hold printed with
  an unreadable HEAD afterwards is a WARN and not treated as a hold. A hold with a HEAD that moved
  anyway is not a hold: HEAD wins, with a WARN. A repair or an unfinished-update
  run that meets the hold keeps its own headline ("Repair finished and
  verified") and gets the `Hermes held` row. The wording is in
  `internal/hermes/contracts.go` (any channel name; ASCII-mapped glyphs and CRLF
  tolerated) with synthetic fixtures.
- **Plugins installed with `--ref` are never offered.** Hermes v0.23.0 skips
  catalog plugins installed with `plugins install <name> --ref <sha>` in the
  no-name `plugins update`, but the named form that Safe Update uses moves them
  off the pin, and `plugins check-updates --json` does not say they are held.
  Safe Update now reads Hermes' own install record (`plugins/.install-metadata.json`,
  the same shape in v0.22.7 and v0.23.0, read-only): a catalog install with
  `pinned: true` and an empty `catalog.pin` (Hermes' `held_with_ref`), or a git
  install with `pinned: true`, is not asked about and not updated; the Plugins
  row says `notes-demo pinned to 89abcdef (not updated; hermes plugins update
  notes-demo moves it off the pin)`. A record that cannot be read or has an
  unknown shape (no file, bad JSON, no entry for the plugin, a `catalog_name`-only
  record) counts as unknown: the plugin is skipped with a note, never touched.
  Right before a named update the record is read again; a plugin found pinned
  then is shown like the probe's pinned row (not a failure, no "run it in a
  terminal" hint). A git plugin with no install record counts as unknown and is
  skipped, which is more cautious than Hermes.

### Added

- **The end-of-update plugin catalog notice of Hermes v0.23.0 is folded into the
  Plugins row.** `hermes update` ends with "N plugin update(s) from the new
  catalog in <this profile|label>: a, b" and an "apply:" line. Safe Update reads
  it (contract and fixture), logs it, hides the two lines from the screen and
  adds what it says to the Plugins row: the plugin probe that runs after every
  update stays the only source of the questions (so nothing is asked twice); a
  name only the notice has is shown with `hermes plugins update <name>`, other
  profiles' notices are named, and with `extras.plugins: off`, or when the
  extras stage does not run, the row carries the notice and the command.

## [0.8.0] - not released separately (included in 0.9.1)

### Added

- **Extras: plugins, hub skills and tools are kept in view on every channel.**
  After the Hermes verdict of every run (up to date, waiting for stable,
  `check`, and after a verified update, once Hermes is back) Safe Update asks,
  read-only, with time limits and side by side: `hermes plugins check-updates
  --json`, `hermes skills check` and `hermes pm doctor` (and, in `check` only,
  `hermes pm update --check`, listed and never applied). The card gets rows
  such as `Plugins  2 updates: holographic, homeassistant`, `Skills  up to
  date`, `Tools  git: files changed since install (see log)`; the detail is in
  the log. An attended run asks for each plugin (y/N, default no; the memory
  provider is named and goes last) and updates it with `hermes plugins update
  <name>` (never with force, so Hermes' security scan still refuses what it
  flags; a plugin that adds dependencies needs a terminal and the row says so),
  and asks once for the hub skills (`hermes skills update`). Hermes' own plugin
  update is all-or-nothing and never evicts a plugin, so the usual failure is
  "refused, nothing changed"; Safe Update writes no file around it and only
  checks afterwards (exit code, plugin still enabled, `memory.provider`
  unchanged) and names the command to run if not. Nothing is applied when
  this run's Hermes was not verified (after an unfinished update or too little
  disk, or an update that did not verify), in `--unattended`, `--yes`, `check`
  or a Hermes chat (the parent chain is checked too).
- **Plugin guard around the Hermes update.** The eviction of 22 Oct (a plugin
  switched off and `memory.provider` cleared) happens inside `hermes update`,
  not in a plugin update. Safe Update reads `plugins.enabled`,
  `plugins.disabled` and `memory.provider` in the pre-flight and again after
  every Hermes update, repair or finish; if Hermes switched a plugin off or
  cleared the provider the card says so (a plugin you had disabled yourself,
  or a provider you changed before the run, does not count), Hermes' reason
  line goes to the log, and an attended run asks once (Y only) to put it back
  with Hermes' own `hermes plugins enable <name>` and, if the provider was
  cleared, `hermes config set memory.provider <name>`. `--unattended`, `--yes`
  and `check` print the commands. `config.yaml` is never edited.
  Every `hermes` child of the extras is started with a tree kill, and the
  apply steps run on their own time budget, not in the five-minute clean-up
  window of an up-to-date run.
  Settings: `extras.plugins` and `extras.skills` (`ask`, `report`, `off`),
  `extras.tools` (`report`, `off`). The Hermes checkout, the Python/Node pins
  and the tools themselves are never changed.
- **Stale stable row.** On a release channel, when the latest stable release
  is older than `extras.stale_days` (default 14; 0 = off) or main has commits
  since it whose subject names a CVE, a GHSA or "security", the card shows the
  age and count and the one-time way out, `hermes-safe-update --channel main`.
  Release date and subjects come from git only (the mirror's main, else the
  checkout's `origin/main` as last fetched); nothing is fetched and nothing is
  done automatically.

- **Stable hold: a run on the stable channel never moves Hermes back.**
  Hermes, with a stored stable record, moves a checkout that is *newer* than
  the latest release back to that release with no question (for example a
  build 230 commits ahead of stable v0.22.4). Safe Update now runs `hermes update` on
  the stable channel only when the release is proven to contain the installed
  commit (Hermes' own "newer than the selected release", then git
  `merge-base --is-ancestor` both ways, then GitHub's compare when the release
  commit is not in the checkout yet). A release that is older than your build,
  diverged from it, or cannot be proven to contain it (a shallow clone, git or
  GitHub not answering, no release named) ends the run like an up-to-date one:
  exit 0, nothing closed or changed, also with `--unattended` and `check`, a
  green card "Waiting for stable: this build (v0.22.4+230.g36f7966) is 230
  commits newer than stable v0.22.4; Safe Update stays on it until a stable
  release includes it." and no repair offer. When a stable release contains
  your build, the run updates to it as before. While it waits, `repair`,
  `--force` and finishing an unfinished update are refused (exit 4, nothing
  changed) because each would pass the stable update to Hermes; the message
  names the one-run way (`--channel main`, which also updates Hermes to the
  newest main commits). The `channel` command explains the wait. Main never
  waits. The checkout is looked at once more after the confirm prompt or the
  wait for idle chats, right before Hermes is closed: if its commit changed
  meanwhile (a manual `hermes update --branch main` finished), the run stops
  with exit 4 and nothing closed instead of passing the stable argv with a
  decision made on the old commit. When Hermes' stable record is set and its
  check says "up to date" or "waits for the next release", a run that could
  still run `hermes update` (finishing an unfinished update, `repair`,
  `--force`, the `R` offer) first has to prove that the release contains your
  build, like an "available" release; it does not rely on Hermes keeping that
  verdict for checkouts without a record.
- **`update.older_release: wait | ask` and `--allow-rollback`.** `wait` is the
  default described above. `ask` keeps the v0.5.0 behaviour (an attended run
  asks for an explicit Y before moving back; `--unattended` and `check` stop
  with exit 4), so the move back stays reachable without a flag;
  `--allow-rollback` (on `run`, `repair`, `check`) is `ask` for one run. The
  wait screen names both. A `safe-update.yaml` that sets `older_release` is
  not accepted by v0.5.0 (the loader is strict).

### Changed

- **Questions before the repair offer.** On an attended up-to-date run the
  order is now: verdict, probes, one question per plugin, the skills question,
  then "Press R to repair" (any other key now "ends the run without
  repairing", which stays true after a plugin was updated). If R is pressed,
  the plugin settings are read again right before the repair's `hermes update`
  so the guard compares against the state after your plugin updates, and the
  extras stage does not run twice. The waiting-for-stable screen has no offer
  and keeps its order (verdict, then extras, then the card).
- Plugins, Skills and Tools rows are printed once on screen (in the end card);
  the log keeps both the stage line and the card line.
- The hold texts name the channel the run is on, not always "stable".
- Hermes' own "Holding at this build: ... predates the gateway runtime" (its
  `update --check` wording) is read as the hold, never as unknown.
- `channel set stable` while the checkout is ahead of the release: the warning
  now says what really protects it (`update.older_release` wait or ask) and
  that Hermes' own `hermes update` moves Hermes back without asking.
- Dev builds report `vX.Y.Z-dev+N.sha` when the top CHANGELOG heading is marked
  Unreleased (no tag needed).

- **Upgrade note: a stable run on a build newer than the release now ends
  with exit 0.** With v0.5.0 the same situation stopped with exit 4 ("would
  move Hermes back"), or asked in an attended run. Set
  `update.older_release: ask` to get that back.

- **The dependency lines no longer say "unchanged" after an update that did
  not finish.** The check compares the dependency files of your checkout with
  the target's, so "unchanged" only meant that the new commits do not touch
  them. When the last update did not finish (the same signals as the `last
  update` row), the pre-flight lines and the check summary say "unchanged in
  the new commits; the last update did not finish, so Hermes may still
  reinstall the Python packages" or "... rebuild the Node packages", because that
  update may have left an install owed that the comparison cannot see. Safe
  Update does not read Hermes' own owed-work marker for this: it lives in
  Hermes' internal install state, is keyed by a hash of the checkout path and
  says the update tail is owed, not which step failed.

### Fixed

- Hermes symbols the console font lacks (the diamond bullet of "Pre-update
  snapshot", hourglass, arrows, emoji, braille art, ...) are mapped to
  renderable stand-ins on screen. Only on the classic Windows console host
  (no Windows Terminal, ConEmu or `TERM_PROGRAM`) does anything else outside
  the console font's character set become `?`; elsewhere CJK and emoji are
  shown as they are. The mapping now covers every Hermes-derived text on
  screen (warnings, rows, cards, questions), not only the update output. The
  log keeps the original text. Tabs and carriage returns in Hermes' lines are
  expanded for the screen (tab stops count from the gutter, escape sequences
  take no column), so the column count used for wrapping is right.
- With nobody at the console, only the first extras question waits for its
  timeout; the other plugin and skills questions are skipped (the rows say
  so) and the repair offer follows as usual.
- The hold texts on a beta channel also name it in the `Stale stable` row and
  in the "until stable passes it" line.
- A prompt-watch cut no longer applies to a child that exited 0; the plugin
  snapshot for the guard is retaken only after a plugin update actually ran.
- A Hermes plugin command stuck at a `[y/N]` question nobody can answer is cut
  after 60 seconds of silence (not 15 minutes; the cut is logged with the question) with the command to run in a
  terminal; 15 minutes stays the hard cap.
- Ctrl+C during a plugin or skills step says at once that the step is being
  finished and for how long at most.

- **Card label "Nothing to upd".** A free line whose text before the first
  colon is as wide as the label column (the sentence "Nothing to update: ...")
  was cut like a label; it is now printed as text.
- **Hermes' check mark in the echoed `update --check` line** rendered as a box
  in conhost (no U+2713 in the font); Hermes' marks in free-text lines are now
  mapped to the tool's own glyphs, as the update output already was. The log
  keeps the raw text.

- **The desktop settings migration finds Hermes' Electron where npm now puts
  it.** Hermes pinned Electron (commit 21693bdf29) and npm hoisted it from
  `apps/desktop/node_modules/electron` to `node_modules/electron` in the
  checkout root. The old folder is gone after such an update, so the Settings
  stage warned "the bundled Electron of Hermes was not found" although the
  update itself was fine. Safe Update now looks in `apps/desktop/node_modules`
  first (older Hermes), then in the checkout root, and honours `path.txt` in
  whichever folder holds the package. It also looks again after `hermes
  update`, because the update is what moves the package. When it is in
  neither place, the warning names both folders.

## [0.5.0] - 2026-10-20

### Changed

- **Upgrade note: two runs that used to end at once can now go further.**
  An interactive run that finds Hermes up to date no longer exits straight
  away when your output and your keyboard are both a real console: it shows
  one window of up to 10 minutes (`--delay N` and `tunables.confirm_timeout_s`
  set it) that offers `R` to repair (see Added). Any other key, Ctrl+C or the
  timeout ends it as before, with the same "up to date" card and exit 0. A
  shortcut window therefore stays on that question. It is not asked with
  `--unattended`, `--yes`, `--force` or `check`, nor when output or keyboard
  is redirected. Second, an `--unattended` (or `--yes`) run that finds no new
  commits but an update that did not finish now closes Hermes and finishes
  it, where it used to end "up to date" with exit 0 (see Added). A scheduled
  task with nothing unfinished behaves as before. A run that asks nobody
  and may not retry an unfinished update ends with exit 1 and a card that
  says so (see Added). No exit code changed its meaning; a finishing run ends
  like any update (0, 1, 3 or 4).

### Added

- **A last update that did not finish is noticed, and finished.** A
  read-only look in the pre-flight, never a stop, asks three questions, and
  one yes is enough: does Hermes' newest update record say the update was
  still running although the process that ran it is gone, or that it was
  interrupted; does Safe Update's own log show an earlier run that got as far
  as `hermes update` and never wrote its final `done` line; is there a
  leftover update marker whose owner is gone. A `last update` row says what
  was found and when, for example "did not finish (stopped during building
  the apps at yesterday 8:56 PM)". With no new commits the run no longer ends
  with "up to date": it goes on to close Hermes and runs the usual `hermes
  update`, which, as far as Hermes' code shows, redoes the work an update
  still owes (dependencies, builds, restart of the gateways) without
  fetching. The 10-minute question says so ("The last update did not finish
  (...). Hermes will close to finish it; that takes about N min. Press Y to
  finish it now ..."); with new commits it adds one sentence to the usual
  question. Anything that cannot be read counts as "unknown" and changes
  nothing; an older Hermes without update records is looked at through the
  log and the marker only. If you ran `hermes update` by hand after the
  crash and it worked, the log no longer counts the stopped run as unfinished.
  In a profile's home the records are read from the root Hermes home, where
  Hermes writes them. The stage is worded as a phrase ("stopped during pulling
  the new code"). Tested with fakes; not yet run after a real crash or against
  a real Hermes.
- **`--unattended` and `--yes` finish an unfinished update without asking,
  but once.** A run that closes Hermes to finish it writes that in the log; a
  later run that asks nobody and finds the same unfinished update does not
  close Hermes again. It ends with exit 1 ("needs attention", so a scheduled
  task shows it) and a card, not "Hermes is up to date": "The last update did
  not finish, and it was not retried: nothing was changed", naming the reason
  and with how to finish it (run Safe Update from the Start menu or
  a terminal; it asks first). The skip is also logged as a WARN line, which
  stays at every `logging.level`. The same happens when Safe Update's own log
  cannot be read, because it then cannot tell whether an earlier attempt
  failed (it would rather not close Hermes); the card says so, and that
  `logging.level` warn or error hides the lines it looks for. A scheduled task therefore cannot
  close Hermes every day over the same failure. An interactive run still
  asks, and the question says the earlier attempt failed. A run that finishes
  an unfinished update, with or without new commits, and whose `hermes update`
  fails is reported as a problem, and no gateway is started on the tree the
  crash left behind. `check` shows the update in the pre-flight (`last
  update` row) and in its summary (`Last update` row: "The last update did
  not finish. Hermes was not touched.") and closes nothing. Tested with
  fakes.
- **`R` = repair on the up-to-date screen.** An interactive run that finds
  Hermes up to date offers: "Press R to repair this install: Hermes closes
  and the update steps run again (dependencies, builds, gateway restart),
  about N min. Any other key exits without changes (auto-exit in 10 min)."
  Only `R` goes on, and it counts as your confirmation, so there is no second
  `Y`. Every safety step still applies: waiting for busy sessions, the
  checkout-lock look right before Hermes closes, the pre-flight stops. It is
  not offered when the run already knows it would stop (not enough disk, run
  from inside a Hermes chat, missing ARM64 build tools with
  `preflight.arm64_toolchain: stop`); a line says why and the run ends as "up
  to date", exit 0. In the default `warn` mode the offer is made and the ARM64
  warning is not printed on a run that repairs nothing; it comes once after the
  `R`. A repair or finishing
  run now also looks for the ARM64 build tools (it skipped that when no commit
  was waiting, although the dependency step runs). Keys typed while the
  pre-flight ran are thrown away first (Windows), so they cannot answer it.
  Tested with fakes.
- **`hermes-safe-update repair`**: a run that goes ahead without new commits,
  with the repair wording and the usual `Y` question (`--yes` and
  `--unattended` skip it, as for any run). It is the same as pressing `R`.
  With new commits it is an ordinary update; with an unfinished update it is
  a finishing run. `--force` is unchanged. It is not allowed with `--check`.
  On macOS and Linux it asks the untested-platform question like `run`. Added
  to `--help`. Tested with fakes.
- **A run without new commits is verified, estimated and recorded as
  such.** The checkout staying where it was is correct there: no false
  problem, the `Version` row says "unchanged; no new commits", and the card
  headline reads "Unfinished update finished and verified" or "Repair
  finished and verified" (the problem wording on failure; every existing row
  stays). The progress bar and the "about N min" leave out the steps that
  need new commits. The timing record says 0 commits and is not used to
  learn the times of ordinary updates. The seconds you spend answering the
  repair offer are not counted as time spent checking. Tested with fakes.
- **A `Desktop rebuild` row when the desktop app does not verify after a
  finishing or repair run.** It names `hermes desktop --force-build` (several
  minutes). Safe Update never runs it: a finishing run only redoes what Hermes
  thinks is owed, so a half-packaged desktop app may not be rebuilt.

### Fixed

- **A line longer than the console window no longer continues at column 0.**
  In a console window the continuation now sits under its text: after the
  `!` or check-mark glyph of a warning, inside the `|` margin of Hermes'
  output (the margin repeats), under the value column of rows and summary
  cards, and under the command of a long command line. Words are split at
  spaces, a path or address longer than a line is cut hard, and the width is
  read when the line is printed, so a resized window wraps to its new size.
  The log file, `--plain` output, files and pipes keep whole lines. Checked
  with tests of the rendered text at fixed widths and two 80-column session
  screens.
- **The skill-merge example works with Hermes' renamed `dir_hash`.** Hermes
  (commit 93fafc4d6f, 2026-10-18) made `_dir_hash` public as `dir_hash` and
  kept no old name, so `examples/hooks/bundled-skill-merge` stopped with "this
  example needs an update" (exit 1, nothing changed) and merged nothing. It
  now takes each of its five Hermes helpers by the public name or the older
  private one, so it runs on Hermes from before and after the rename. When
  neither name exists it still stops, and names both. If you copied the
  script elsewhere, copy the new version over it. Tested with fake Hermes
  cores of both kinds.

## [0.4.0] - 2026-10-15

### Changed

- **Upgrade note: a scheduled task that runs Safe Update with "Run with
  highest privileges" now ends with exit 4 and changes nothing** (see the
  administrator-terminal stop below). Remove that setting from the task, or
  add `--allow-elevated` to its arguments (not recommended: the update then
  leaves files your normal account cannot update later).
- **The final "Press Y" prompt now waits 10 minutes by default (it was 30
  seconds).** `tunables.confirm_timeout_s` and `--delay N` still set it. If
  you answer more than a minute after it appeared, Safe Update looks at the
  sessions once more before closing Hermes; a chat that became busy sends the
  run back to waiting for idle. Skipped after F, with `--yes` and with
  `--unattended`.
- **Countdowns of two minutes or more read in minutes** ("auto-abort in
  10 min"), shorter ones in seconds: on the final prompt, the rollback
  prompt, `channel set` and the progress footer. In `--plain` mode a long
  countdown prints once a minute, then every 10 seconds in the last two
  minutes.
- **`check` exits 4 on a held checkout lock or an unreadable `tools\` entry
  too**, like `run`. On macOS and Linux the lock, elevation and `tools\`
  tests do nothing (the log says "checkout lock: not probed" and "elevation
  check unavailable").
- **Exit 2 now means "no way to run Hermes was found".** A missing `hermes`
  launcher no longer ends `run` or `check` with exit 2 when Hermes can still
  be run through its managed Python (see Fixed).

### Added

- **A stop, exit 4 and nothing closed, when Hermes' checkout lock is held**:
  an update still running, or a process that a killed update left behind. The
  message names the holder, or lists what the dead owner started and the
  `taskkill` remedy. Safe Update only tries the lock and gives it back at
  once; it never creates the file or stops a process. `check` shows the same
  stop, and if the probe cannot be asked the run goes on. The lock is looked
  at again right before Hermes closes whenever the last look is over a minute
  old, in every mode (long prompt, long wait for idle sessions, F, `--yes`,
  `--unattended`), so an update that started meanwhile ends the run instead of
  failing after Hermes was closed. Windows only.
- **A `run` from an administrator terminal now stops before closing
  anything**, exit 4. An elevated update leaves files in Hermes' `tools\`
  folder that your normal account cannot update later. `--allow-elevated`
  goes on anyway; `check` only warns. Only the full half of a split UAC token
  counts (a standard user, or an administrator with UAC off, is not
  affected). Tested with a fake token, not an elevated run. Windows only.
- **Entries in Hermes' `tools\` folder that your account cannot open now stop
  the run before anything is closed**, exit 4. An elevated install or update
  leaves them behind, and the next update would fail on them with `[WinError
  5]`. The message prints the `takeown` / `icacls` commands that repair them
  and names the way to go on anyway: `preflight.acl_check: "warn"` (new
  setting, `stop | warn | off`, default `stop`). It is a real, read-only open
  with your own account, shallow (`tools\` and one level below, a few other
  folders), capped at 400 opens and 2 seconds (it says when it stopped
  early; Ctrl+C ends it). A refused entry is opened a second time after about
  0.4 seconds, so a file Windows refuses for a moment while deleting or
  scanning it does not stop the run. Elsewhere, unreadable entries are only a
  warning. Windows only.
- **A warning for missing ARM64 build tools** (Windows ARM64, Python
  dependencies about to be rebuilt): without the Visual Studio ARM64 C++
  tools or `clang.exe`, Hermes would start the Build Tools installer in the
  middle of the update. New setting `preflight.arm64_toolchain: warn | stop |
  off`, default `warn`. Safe Update installs nothing. Not run on an ARM64
  machine.
- **New summary rows**, all informational (none changes the result or the exit
  code): `Gateway check`, `Stale gateway`, `Gateway rescue`, `Double launch`
  (hermes-agent #126821) and `Launcher`.
- **If `hermes gateway start` fails and Hermes' own hint names a registered
  Scheduled Task**, Safe Update runs that task once and waits up to a minute
  for the gateway. It never guesses a task name and ignores one that starts
  with `/` or `-` (an option of `schtasks`). Run only against a fake Task
  Scheduler.

### Fixed

- **A good update is no longer reported as a problem when only Hermes' own
  check of its gateway relaunch failed** (hermes-agent #123971). Safe Update
  accepts that exit 1 only when a gateway ran before, "Update complete!" was
  printed, every failure line is one of Hermes' three relaunch wordings (a
  line starting with `Error:`, `ERROR:`, `error:`, `fatal:` or `FATAL:`
  counts as a failure line), and its own check finds the gateway on the new
  commit and the desktop build verified. An amber `Gateway check` row names
  the Hermes line. Current Hermes reports this case with exit 0, so it
  matters for an update run by an older Hermes; tested only with synthetic
  Hermes output.
- **A gateway that is alive after the update but still on the old code is
  restarted once and checked again, instead of failing the verify.** Only
  that gateway is stopped (`gateway stop`, then a kill of that one process;
  never `gateway stop --all`, which on macOS and Linux stops every profile's
  gateways) and it is started with a plain `gateway start`, also while other
  profiles' gateways run. If it does not come back, the `Stale gateway` row
  and the log say so. Not done when Hermes left its gateways stopped on
  purpose or the update stopped partway after the code changed. Tested with
  fake gateways and processes, not with a second real gateway.
- **A gateway that ran before the update and is down afterwards is started
  again also when the verify step never ran** (an update interrupted or
  aborted after Hermes was closed). A `Gateway rescue` row says that Safe
  Update brought it back and how long it was down. Not started when Hermes
  left it stopped on purpose ("Paused gateway(s) left stopped") or after an
  update that stopped partway after the code changed; the row says why.
  `gateway start` also counts as failed when Hermes prints "Gateway start via
  ... FAILED" with exit 0.
- **After an update that stopped partway** (non-zero exit, no "Update
  complete!") and moved the code, the verify step no longer starts a gateway
  that is down, including one Safe Update stopped itself; before, it could
  start one on a half-updated tree. A clean exit still starts it, and Hermes'
  own hold is respected there too.
- **The update can replace or delete the `hermes` launcher.** Safe Update
  looks for it again after `hermes update`: if it moved, every later `hermes`
  command of the run follows it; if none works, Hermes is run through its
  managed Python with the command Hermes itself prints, also when the install
  has no launcher at the start. A `Launcher` row says when this happened. The
  printed command was checked read-only against a real install; a launcher
  replaced mid-update has been tested only with synthetic installs.

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

[Unreleased]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.9.1...HEAD
[0.9.1]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.5.0...v0.9.1
[0.5.0]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/cloudn8ive/hermes-safe-update/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/cloudn8ive/hermes-safe-update/releases/tag/v0.1.0
