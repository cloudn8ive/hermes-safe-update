# hermes-safe-update: design

Status: architecture of v0.1.0. The tool grew out of a set of personal
Python scripts and keeps their behaviour where it was sound; binding
decisions are numbered D1-D18 below. For a user-level
description see [how-it-works.md](how-it-works.md); for settings see
[settings-migration.md](settings-migration.md).

## 1. What the tool does

One static Go binary that updates a Hermes **managed source-checkout**
install without cutting off running work:

1. checks that an update exists and that it is safe to start,
2. waits until no session is busy (asks before stopping anything),
3. closes the desktop app gracefully (force only after a timeout),
4. runs `hermes update --yes --keep-stash --branch main`,
5. verifies the result (gateway runs the new code, desktop build verified),
6. backs up and migrates the desktop's UI settings when the renderer origin
   changed,
7. runs the user's `post-update` hooks (Hermes still closed), reopens Hermes,
   runs the `post-relaunch` hooks, cleans up, prints a summary.

Everything personal (theme, numbers, start-tile restore, ...) is
configuration (`safe-update.yaml`) or a hook. The code is generic.

### 1.1 Which installs it handles (install kind)

| Install kind | Detected by (internal/hermes) | What the tool does |
|---|---|---|
| Managed source checkout (`%LOCALAPPDATA%\hermes\hermes-agent`, `~/.hermes/hermes-agent`) | `<home>/hermes-agent/.git` + launcher | updates it |
| macOS `Hermes.app` (DMG/ZIP) without a checkout | `/Applications/Hermes.app`, `~/Applications/Hermes.app` | steps aside: "use Check for Updates in the app"; exit 5 |
| Windows MSIX desktop without a checkout | MSIX package install location | steps aside: "updates come from the App Installer feed"; exit 5 |
| AppImage / deb / rpm desktop (Linux, self-built; best effort, untested) | running from `/tmp/.mount_*` or a package path | steps aside; exit 5 |
| Docker | `/.dockerenv`, container cgroup | steps aside: "pull the new image"; exit 5 |
| Nix | `/nix/store` launcher | steps aside: "update through Nix"; exit 5 |

Stage 0 (`stepAside`) runs before anything else and changes nothing. A
checkout that also has a packaged app installed is still a checkout
(`hermes update` keeps the installed app current itself).

### 1.2 Platforms

| OS / arch | Release binary | Status |
|---|---|---|
| windows/amd64 (Windows 10) | yes | unit-tested; one real unattended update (started by a scheduled task) ran and verified on Windows 10 22H2 amd64, and the Start-menu and Ctrl+K launches were tried. The settings migration was run in a sandbox on a copy of a real desktop settings store and in an automated test on synthetic data, both with the real Electron. On the real update the settings step failed, because that Hermes range removed renderer-server.ts; this was fixed afterwards (file:// detection, backup when detection fails) and covered by unit tests and a read-only dry run, but no real settings migration has been run since the fix |
| windows/amd64 (Windows 11) | yes | expected to work; not yet tested |
| windows/arm64 | yes | builds; not run on arm64 hardware |
| darwin/arm64, darwin/amd64 | yes | **untested**: cross-compiled and vetted; unit tests so far run on Windows only, CI will run them here once published |
| linux/amd64, linux/arm64 | yes | **untested**: same as darwin; Linux desktop packaging is disabled upstream, AppImage paths are unverified |

The matrix is the set of OS/arch pairs Hermes itself runs on (Tier 1 plus
Intel macOS, which upstream builds). On macOS and Linux the tool prints a
one-time warning and asks for confirmation; `--accept-untested` skips the
question and is required with `--unattended` (otherwise exit 3). `version`
and `--help` never ask.

## 2. Package layout

```
cmd/hermes-safe-update/   CLI parsing, wiring, exit codes
internal/apperr/          sentinel errors -> exit-code table
internal/config/          YAML + machine JSON + env + flags
internal/fsx/             atomic write, rename retry (D6)
internal/execx/           the only way to run children
internal/logx/            slog handler, path redaction, rotation
internal/timings/         steps, history file, estimator
internal/platform/        OS interfaces, stubs, fakes; per-OS backends
                          (*_windows.go, *_darwin.go, *_linux.go, *_unix.go)
internal/hermes/          install, sessions, gateway, contracts
internal/update/          the flow, hooks runner, summary
internal/ui/              VT renderer + plain renderer
internal/mirror/          optional local git mirror
internal/gc/              dependency-generation cleanup
internal/cua/             cua-driver refresh / logon task
internal/settings/        settings backup/migrate/revert + JS
internal/testutil/        golden files, fake clock
scripts/                  build-all.sh (canonical) + build-all.ps1
```

Dependency direction (no cycles): `config`, `fsx`, `apperr`, `logx`,
`execx`, `platform`, `timings` are leaves or near-leaves; `hermes`,
`mirror`, `gc`, `cua`, `settings`, `ui` depend on them; `update` depends on
all of those through interfaces; `cmd` wires concrete implementations.
Business logic never checks `runtime.GOOS`: it receives a
`*platform.Platform` (and `platform.NewFake()` in tests).

Third-party modules: `go.yaml.in/yaml/v3` (config). Also used: `golang.org/x/sys` (Windows APIs),
`golang.org/x/term` (POSIX raw keys), `modernc.org/sqlite` (pure-Go,
read-only `state.db`). New dependencies need a strong reason.

## 3. The flow

`internal/update` runs a list of `Stage{Key, Run, Skip}`. A stage returns
nil to continue or an `apperr` sentinel to stop. The footer advances on the
stage key (`timings.UpdateSteps`); inside the update stage, lines of
`hermes update` output advance it through `hermes.UpdateTriggers`.

| # | Stage key | Group | What | Stops with |
|---|---|---|---|---|
| 0 | stepAside | - | install kind != checkout -> advice | `ErrNotCheckout` (5) |
| 0 | - | - | launcher missing | `ErrLauncherMissing` (2) |
| 1 | local | Check | marker held by a live pid?; free disk >= `min_free_gb`; dirty checkout (info); HEAD | `ErrPreflightStop` (4, D1) |
| 2 | remote | Check | mirror -> GitHub API -> `hermes update --check`; npm/pydeps prediction | up to date: exit 0; check failed: 4 |
| 3 | procs | Check | process classes (desktop/gateway/backend/kernel); Windows pause-gate probe | - |
| 4 | wait | Close Hermes | if anything will be stopped (D2): busy sessions -> A/F prompt (attended) or wait (unattended, real elapsed time, D3) up to `idle_wait_max_s`; then Y confirm unless `--yes`/`--unattended` | `ErrCancelled` (3) |
| 5 | close | Close Hermes | pre-close hooks; graceful close; force after `graceful_close_s` with blocker diagnosis; leftover backends/kernels; **claim the marker only now** | `ErrCancelled` (3) if the app will not exit |
| 6 | gateway_stop .. restart | Update, Build, Finish | `hermes update` (no `--gateway`), timeout `update_timeout_s`, gate retry once | - |
| 7 | verify | Finish | gateway `code_sha` == HEAD and alive; `desktop_update_verify` (D17); known false fleet warning | sets `Facts.Verified`; never flipped later |
| 8 | settings | Finish | if verified and `auto_migrate`: backup -> migrate (Hermes is closed now) | WARN only |
| 9 | hooks | Finish | post-update hooks (verified only), Hermes still closed | WARN only |
| 10 | relaunch | Finish | if the desktop was running and not `--no-relaunch`; then post-relaunch hooks (whatever the result) | WARN only |
| 11 | cleanup | Finish | dependency-generation GC (verified only; Windows) | WARN only |
| 12 | cua | Finish | cua-driver refresh when the update deferred it (verified only) | WARN only |
| 13 | summary | - | card, timings saved (also in plain mode, D5), mirror offer | exit 0 verified / 1 problem |

`finally` (always, once the marker is claimed): stop the marker refresher,
release the marker if it is ours, relaunch if due, restart a gateway the
tool stopped on every abort path.

`check` runs stepAside, local, remote, procs, sessions, gc_preview,
cua_status, summary. It never stops, closes, updates or relaunches anything,
and never moves HEAD or the branch, or touches the index or the working tree
of the checkout (`TestCheckLeavesHeadIndexAndWorkingTreeAlone` pins this
against a sandbox with real git). What it does write (decision F1 in the build notes):

| Where | What | Why |
|---|---|---|
| local mirror (bare repo, if set up) | `git fetch --prune --no-tags` from GitHub; `cache/` state file and `logs/update-mirror.log` under HOME | `Seed` refreshes a mirror that is behind or that GitHub's API cannot confirm |
| checkout `.git` | objects, `refs/remotes/origin/main`, `refs/tags/v*`, `FETCH_HEAD`, reflog of the remote ref; `.promisor` markers on partial clones (git 2.53 workaround) | `Seed` fetches from the mirror (`url.<mirror>.insteadOf`, one-shot `-c`, never saved), exactly what `git fetch` would write; also what `hermes update --check` does on the fallback path |
| `logs/safe-update.log`, `logs/safe-update-timings.json` | one run's lines; one history record | normal logging (also in plain mode, D5) |
| `safe-update.json` | only if the user answers "never" to the mirror offer | the offer is shown on a real console only |

The checkout's git calls use `--no-optional-locks`, so `git status` does not
refresh the index's stat cache on disk.

### 3.1 Data flow

```
flags ─┐
env ───┼─> config.Load ─> *config.Config ─┐
yaml ──┤   (defaults < yaml < json < env < flags)
json ──┘                                   │
platform.New() ───────────────────────────┤
execx.New() ──────────────────────────────┼─> update.Deps ─> update.Execute(Flow(mode))
hermes.{Locator,Sessions,Gateway,Repo,...}─┤        │
mirror / gc / cua / settings services ─────┘        ├─> ui.Renderer (VT or plain)
                                                    ├─> slog -> logs/safe-update.log (redacted)
                                                    └─> timings history, machine state (atomic)
```

## 4. Files

| Path (under the Hermes home unless noted) | Owner | Format |
|---|---|---|
| `safe-update.yaml` | user | strict YAML; see `safe-update.example.yaml` |
| `safe-update.json` | tool | machine state (`mirror{path,set_up}`, `mirror_offer`); unknown fields preserved; atomic |
| `logs/safe-update.log` | tool | slog text, redacted, rotated at `logging.max_size_mb` |
| `logs/safe-update-timings.json` | tool | JSON history, atomic, retention 40+40 |
| `.hermes-update-in-progress` | shared with Hermes | `<pid>\n<epoch>\n`, refreshed every 120 s |
| `backups/settings-<stamp>/` | tool | Local Storage copy + UI json + `manifest.json`; newest 3 kept |
| `state.db`, `profiles/*/state.db` | Hermes | read-only SQLite (`mode=ro`, busy timeout); unreadable = busy |
| `gateway_state.json` | Hermes | read: `code_sha`, `pid` |

Missing and broken files:

- `safe-update.yaml` at the default location may be absent (= defaults). A
  path given with `--config` must exist: a missing file is a config error
  naming the path (exit 64), so a typo never silently drops the user's hooks,
  theme or tunables. Bad YAML, unknown keys and invalid values are exit 64
  with `file:line`.
- `safe-update.json` is tool-owned state, so it never blocks a command
  (a corrupt file is treated as empty state). Missing = empty state. Unreadable
  or corrupt = `config.Config.MachineErr` is set, a warning goes to stderr and
  the log, and the run continues with empty state. `SaveMachineState` never
  overwrites a corrupt file: it first renames it to
  `safe-update.json.corrupt-<UTC stamp>`, then writes the new state
  atomically. A valid file is replaced in place.

## 5. Errors and exit codes

Errors are wrapped with `%w`; callers use `errors.Is/As`. One function,
`apperr.ExitCode`, maps them (only `cmd` calls `os.Exit`).

| Code | Meaning |
|---|---|
| 0 | done: update verified, check finished, or already up to date |
| 1 | the update ran but needs attention (RESULT PROBLEM), or an unexpected error |
| 2 | the hermes launcher was not found |
| 3 | cancelled before anything changed (user, timeout, idle wait exceeded, app would not exit, untested platform declined, Ctrl+C) |
| 4 | pre-flight stop: another update holds the marker, low disk, update check failed (D1) |
| 5 | not a managed source checkout: nothing changed, advice printed |
| 6 | settings command refused because the desktop app is running |
| 64 | bad command line or invalid configuration |

Rules: post-update steps (settings, hooks, relaunch, GC, cua) can only add
WARN rows; they never change a verified result. Unrecognised Hermes output
is a WARN "Hermes output changed: ..." (D14), never silent success.

## 6. Logging

- `log/slog` text handler to `logs/safe-update.log`; level from
  `logging.level` / `--log-level`.
- Every message and string attribute passes through `logx.Redactor`: the
  user's home becomes `~`, the local app data dir `%LOCALAPPDATA%`
  (case-insensitive, either separator, only at a path boundary). Logs are
  meant to be pasted into bug reports.
- Never log localStorage values, tokens, environment dumps, session ids or
  session titles, or the mirror folder; log counts, sizes, key names. Busy
  sessions appear on the screen with id and title, in the log only as
  `session <n> (busy, last activity <N>s ago)`. The mirror folder appears on
  the screen (summary card, offer prompt) and in the log as `<mirror folder>`
  or not at all.
- Each child process: argv (redacted), exit code, duration. Each
  destructive step: before and after.

## 7. Console

- Renderer is chosen once: VT renderer when stdout is a real console, VT
  enabling succeeded, not `--plain`, not `HERMES_SAFE_UPDATE_PLAIN`, not
  `TERM=dumb`; `NO_COLOR` keeps the layout but drops colour. Otherwise the
  plain renderer. No logic depends on the choice.
- Prompts read single keys; only listed keys count (D3); Ctrl+C read as a
  key aborts. `--unattended` never prompts.
- Taskbar progress (Windows ITaskbarList3 or OSC 9;4 in Windows Terminal)
  via `platform.Progress`; no-op elsewhere.
- Dates in fixed English (D12); `math.RoundToEven` wherever a value is rounded
  (D11).

## 8. Decisions

Binding decisions:

- D1 pre-flight STOP exits 4. D2 confirm whenever anything gets stopped.
  D3 real elapsed time for the idle wait; prompts accept only listed keys.
  D4 one git resolver: bundled git first, then PATH. D5 plain mode records
  timings. D6 atomic state writes with sharing-violation retry. D7 no
  managed Python = degrade (gate unknown, GC skip, verify WARN), never
  require system Python. D8 settings key lists are `*` globs per profile.
  D9 target origin from `DEFAULT_PORT` in `renderer-server.ts` (or `file://`
  when the build has no such file and `main.ts` loads the renderer from
  disk), `--origin` overrides, a live fallback-port origin wins and is
  reported; if the origin cannot be detected the settings stage still takes
  a backup and names the exact `migrate-settings --origin <url>` command. D10 plain
  prefs old-wins on the first migration after an origin change, new-wins
  later. D11 `RoundToEven`. D12 English dates. D13 estimates = mean of the
  last 5. D14 all scraped wording in `internal/hermes/contracts.go`. D15
  native process snapshots. D16 elevation only behind `--unattended` /
  `--no-elevate` guards, quoted argv. D17 `desktop_update_verify` rc 0 +
  empty output = verified. D18 every flag of the earlier scripts is kept.

## 9. Package notes

Short notes on how individual packages work.

### 9.1 mirror, gc, cua

- mirror: location is machine state (`safe-update.json` `mirror.path`); a path
  counts only if `HEAD`, `objects/`, `refs/` exist. `Seed` copies main and
  `v*` tags from the mirror into the checkout using `-c
  url.<mirror>.insteadOf=<origin>` on its own git calls only. The checkout's
  origin and the environment of `hermes update` are never touched. One writer
  at a time via `cache/update-mirror.lock` (O_EXCL; stale after 30 min or a
  dead pid). `mirror refresh` (cron) prints nothing and exits 0 (ok, nothing to
  do, another refresh running) or 1 (fetch failed; reason in
  `logs/update-mirror.log`). Hermes runs cron scripts with Python or bash, so
  `Setup` writes a tiny `scripts/hermes_safe_update_mirror.py` shim that
  starts `<this exe> mirror refresh` with no console window. An existing job
  that points at the same shim is recognised and reused.
- gc: embedded `script/safe_update_gc.py`, written to `cache/safe-update-gc.py`
  and run with the managed Python `-I`. It calls Hermes' own collectors; only
  the delete step changes (locked files are renamed into
  `cache/safe-update-trash`). No managed Python = `Skipped` (D7).
- cua: acts only when the update output holds `hermes.CuaDeferred`. The
  elevated step is one PowerShell script passed as `-EncodedCommand`; every
  path is a single-quoted literal with quotes doubled. Never with
  `--unattended`/`--no-elevate`.

### 9.2 ui and estimator

- `ui.New(cfg, platform, w, getenv)` picks the renderer once: VT when stdout
  is a real console, `EnableVT` succeeded and neither `--plain`,
  `HERMES_SAFE_UPDATE_PLAIN` nor `TERM=dumb` is set; `NO_COLOR` keeps the VT
  layout and drops every colour parameter (bold stays). Otherwise `Plain`.
  Both drive the same `timings.Estimator`, so timings are recorded in plain
  mode too (D5). `WithEstimator(fn)` on either renderer lets the update flow
  call `SetConditions`/`ExpectSkip` under the renderer's lock.
- VT footer: always five lines (blank, rule, bar, step, stages), rewritten in
  place once a second by a ticker (and on every state change). The rows to go
  up are computed from the drawn line lengths at the current width, so a
  maximise/restore does not leave copies behind. Text is cut to width, never
  wrapped; prompts are wrapped by the renderer.
- Everything visual comes from `config.Theme` (colours, glyphs, hues, margin,
  label/status width, bar band, gradient). Glyph defaults are the Consolas
  set; a test fails if any rune outside the theme glyphs, group icons and
  ASCII is emitted by the renderer itself. Upstream glyphs in `hermes update`
  output are re-marked for display (`✓`→`√`, `⚠`→`!`, ...).
- Dates and clocks are fixed English without leading zeros (D12); rounding
  uses `math.RoundToEven` (D11).
- Estimator: mean of the last `estimate_window` matching runs (D13); condition
  pools same/fast/unknown/all; class factors; `k_t` overrun; monotonic bar.
  Go step list defaults sum to 607 s; `PlannedTotal("close")` = 528 s, 464 s with pydeps expected-skip.

### 9.3 settings

- `settings.New(Deps)` returns the `Service`. Deps: `hermes.Install` (Home,
  Checkout, UserData, Electron), `config.Settings`, `platform.Procs` +
  `Paths.DesktopProcessMarkers()` (running check via `hermes.Classify`),
  optional `execx.Runner`, Hermes versions for the manifest, a clock.
- The migrator (`migrator/main.js`, embedded) only reads and writes
  localStorage through Chromium on Hermes' bundled Electron: `dump` (values
  of the named origins) and `write` (set keys, flush, re-read, compare). All
  merge decisions are made in Go (`BuildPlan`). Values travel through files
  in a private work dir that is deleted after each run, never through argv
  or logs; reports list key names only. An inherited `ELECTRON_RUN_AS_NODE`
  (even empty) is handled by the script relaunching itself without it.
- Origins: a read-only scan of the LevelDB files for `META:`/`_` key
  prefixes proposes candidates and their recency; a `dump` of a copy
  confirms them and counts keys. Target (D9): `settings.origin`/`--origin`,
  else `DEFAULT_PORT` from `renderer-server.ts`, unless a different
  `127.0.0.1` origin in the store is newer (fallback port). Builds without
  `renderer-server.ts` whose `main.ts` loads the renderer with
  `pathToFileURL(resolveRendererIndex...)` (and does not import
  `renderer-server`) target `file://`; no fallback-port rule applies there.
  An unrecognised layout is an error, never a guess. Source = the most
  recently used other origin with keys.
- Merge (D8, D10): one-side keys copied; `keep_new` keys keep the new value;
  `union` keys merge objects / `[id, value]` pair lists (new wins per entry;
  not such JSON = plain rule); plain prefs old-wins on the first migration
  for a `source -> target` pair, new-wins after it. The pair is recorded in
  `<backup dir>/settings-migrations.json` (never pruned; corrupt = error)
  with a sha256 of the source origin's content: when the source still
  hashes the same, a later run writes nothing (removals in the target
  stay removed); a changed source gets the later-run rules.
- Source choice: most recently used non-target origin with keys; on a
  recency tie (one compacted .ldb) a previous migration target wins, then
  http over file://.
- Migrate: refuse while the desktop runs (exit 6) -> plan on a read copy;
  nothing to write (no source / source unchanged / already migrated) =
  `Changed=false` with NO backup -> backup -> copy the backup's store next
  to the live one (same volume) -> plan -> Electron `write` on the copy ->
  fresh-process `dump` must re-plan to zero writes -> check Hermes is still
  closed -> swap by two renames -> fresh-process dump of the live store must
  re-plan to zero -> else rename the original back. `Plan` (dry run) works
  on a copy and is allowed while Hermes runs.
- Backup: `Local Storage` (minus LevelDB's `LOCK`) + top-level `*.json` of
  userData, copied into a hidden temp dir then renamed to
  `settings-<UTC yyyymmdd-hhmmss>[-N]`; `manifest.json` = sha256 per file,
  key counts per origin (when Electron is available), reason, the Hermes
  version before and after (`hermes_before` / `hermes_after`; both the
  current version for a stand-alone `backup-settings` / `migrate-settings`); a
  copy of the migration marker. Retention `settings.backup_retention`
  (default 3); only `settings-*` dirs with a manifest are counted or pruned.
- Revert: refuse while the desktop runs; verify every hash of the backup;
  stage its store next to the live one and verify again; take a "before
  revert" backup; swap; restore the UI json files; verify the live hashes
  (rollback on mismatch); restore the marker to its backed-up state.
- Tests: fakes for the store and processes; failure injection asserts the
  live userData is byte-identical. `go test -tags electron ./internal/settings`
  runs the real bundled Electron on a synthetic userData in `t.TempDir()`
  (`HSU_ELECTRON` overrides the Electron path).

### 9.4 update, Windows platform, cmd

- `update.NewRun(Deps)` + `update.Execute(ctx, run, update.Flow(mode))`.
  `Deps.Wire` builds the install-dependent services (gateway, repo, verifier,
  marker, gc, cua, mirror, settings, hooks) once `stepAside` has located the
  install; tests pre-wire `Deps` with fakes instead. The flow changes the
  estimator only through the renderer's `WithEstimator` (the renderer's
  ticker reads it), never directly.
- `finally` (always): stop the marker refresher, release the marker if
  claimed, relaunch the desktop if it was closed and is not back yet,
  restart a gateway the tool stopped, show the cancel card when nothing was
  changed, save timings (plain mode too, D5). Ctrl+C during `hermes update`
  reaches the child through the shared console; the tool never cancels it
  (only `update_timeout_s` kills it) and the run ends with exit 1, since
  things may already have changed.
- Hooks (`update.ExecHooks`): argv only, timeout (default 300 s) + WaitDelay
  via execx, a timeout stops the hook's whole process tree
  (`execx.Cmd.KillTree` -> `Procs.KillTree`, as for `hermes update`),
  working dir = the hook's `dir` or the Hermes home, env
  `HERMES_SAFE_UPDATE_STAGE` / `HERMES_SAFE_UPDATE_RESULT`. The last
  non-blank output line is the summary row; non-zero exit or timeout = WARN.
  `pre-close` runs at the start of the close stage (result `unknown`),
  `post-update` after a verified update only, with Hermes still closed
  (stage 9, before the relaunch), `post-relaunch` right after a successful
  relaunch, whatever the result.
- Timings history (`timings.LoadHistory/SaveHistory`): JSON with
  indent 1; a malformed record is skipped, a corrupt file is replaced on
  save; full updates (a `desktop` or `verify` step) and everything else are
  kept 40 + 40, original order.
- Windows backends: `%LOCALAPPDATA%\hermes` (+ `HERMES_DATA_DIR_SUFFIX`;
  `HERMES_HOME` is ignored) with the known-folder API and
  `%USERPROFILE%\AppData\Local` as fallbacks; Toolhelp snapshot +
  `NtQueryInformationProcess(ProcessCommandLineInformation)` for command
  lines (no WMI, D15); `KillTree` walks the snapshot (children created after
  their parent only, pid reuse) and refuses this tool's own pid, its
  ancestors and any tree it runs in. `RequestClose` posts `WM_CLOSE` to the
  pid's own top-level windows only; `Launch` = `explorer.exe <exe>` (exe must
  exist: explorer shows a modal dialog otherwise). Keys via
  `ReadConsoleInput` (Ctrl+C is a key, arrows are `Other`). Progress: OSC
  9;4 when `WT_SESSION` is set, else `ITaskbarList3` on a locked COM thread,
  no-op off-console. Icon + AppUserModelID: the console re-execs itself with
  the hidden `--appid <hwnd>` flag (§6.3). Logon tasks: `schtasks /Query /XML`
  (UTF-16 handled); `RunElevated` = `ShellExecuteEx` "runas" with
  `windows.EscapeArg` quoting.
- `cmd`: `run`/`check` = the flow; `mirror setup|refresh|status`;
  `migrate-settings [--dry-run] [--origin]`, `backup-settings`,
  `revert-settings [--backup ID]`, `list-backups`. `mirror refresh` is
  silent (cron) and never prompts. `safe-update.json` is read from the home
  that `paths.hermes_home` names. `scripts/hermes-safe-update.cmd` and
  `hermes-update-check.cmd` keep the names of the earlier scripts' shims: exe next to the
  shim, else `%LOCALAPPDATA%\Programs\hermes-safe-update`, else `where
  hermes-safe-update.exe` (PATH).
