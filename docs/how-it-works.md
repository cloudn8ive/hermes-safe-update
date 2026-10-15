# How it works

This is the user-level description of one `hermes-safe-update` run. The
package layout, decisions and file formats are in [design.md](design.md).

A run is a list of stages. A stage either continues or stops the run with
an exit code. `--check` runs only the look-only stages: it never closes,
stops, updates or reopens anything, and never moves HEAD or touches the index
or working tree of the checkout. Like `git fetch`, it may refresh the local
mirror and fetch new commits and `v*` tags into the checkout's git data
(stage 2), and it writes its own log and timing history.

| # | Stage | What happens | Can stop the run |
|---|---|---|---|
| 0 | install check | Is this a managed source checkout with a `hermes` launcher? Packaged installs get advice instead. | exit 5; exit 2 if the launcher is missing |
| 1 | local | Is another update's marker held by a live process? Free disk at least `min_free_gb`? Dirty checkout (information only)? Current commit. | exit 4 |
| 2 | remote | Which channel (see below), then: is there an update? On main: local mirror, then GitHub API, then `hermes update --check --branch main`. On a release channel: Hermes' own `hermes update --check` (the release it names; a checkout already newer than the release waits for the next one only if stable is Hermes' unchosen default, otherwise the update would move it back and the rollback guard asks). Predicts Python and Node dependency reinstalls (against the release commit on a release channel). | up to date: exit 0. Check failed, release not resolvable, a stable request on a Hermes without channels, or an update that would move Hermes back to an older release (rollback guard; asks first when attended): exit 4 |
| 3 | procs | Classify running processes: desktop, gateway, backend, kernel. On Windows, probe whether Hermes' own gateway pause would work. | |
| 4 | wait | If anything will be stopped: sessions busy in the last `idle_window_s` (default 180 s) make it wait. Attended: `A` aborts, `F` goes on. Unattended: waits up to `idle_wait_max_s`. Then the `Y` confirmation (skipped by `--yes` and `--unattended`). | exit 3 |
| 5 | close | `pre-close` hooks, graceful close with `graceful_close_s` to finish, force only after that, stop leftover backends and kernels, then claim the update-in-progress marker. | exit 3 if the app will not exit |
| 6 | update | `hermes update --yes --keep-stash` plus `--branch main` (main), `--channel stable` (`update.channel: stable`) or nothing (a release channel Hermes chose), capped at `update_timeout_s`. Progress follows Hermes' own output lines. | |
| 7 | verify | If a gateway ran before the update: its `code_sha` equals the checkout commit and it is alive (restarted if not). No gateway before: none required, none started. `desktop_update_verify` passes. | result "problem": exit 1 |
| 8 | settings | Back up and copy desktop settings to the origin the app uses now if it changed (workaround for settings lost when the renderer moved; see settings-migration.md). | warning only |
| 9 | hooks | `post-update` hooks (verified only), while Hermes is still closed. | warning only |
| 10 | relaunch | Reopen the desktop app as `desktop.relaunch` says: `auto` if it was running, `always` also if it was already closed (verified, attended runs), `never` not at all; `--no-relaunch` beats all three. Then run the `post-relaunch` hooks. | warning only |
| 11 | cleanup | Remove old dependency sets (Windows, verified updates only). | warning only |
| 12 | cua | Refresh cua-driver if the update deferred it (verified updates only). | warning only |
| 13 | summary | Summary card, timing history saved, optional mirror offer. The `Version` row counts the commits really installed (git on the old and new HEAD). After a verified update, a quick best-effort look (API, then `git ls-remote`, a few seconds) adds a `Newer` row if `main` moved meanwhile; a `Channel` row names the channel and why; on a release channel the `Newer` row reports a newer published release, never main moving; an app that was not reopened gets a `Hermes app` row. | exit 0 verified, 1 problem |

## Which channel a run uses

Hermes updates from `stable` (the latest published release) or `main` (every
commit). `update.channel` in `safe-update.yaml` (`follow`, `stable` or `main`;
`--channel` for one run) decides what Safe Update does about it:

1. `main`: `--branch main`, as Safe Update always did before channels. It
   works on every Hermes version.
2. `stable`: `--channel stable`. A Hermes that does not list `--channel` in
   `hermes update --help` stops the run in the pre-flight (exit 4) before
   anything is closed.
3. `follow` (the default): read Hermes' own choice, offline and read-only. The
   install id comes from `hermes update --install-id`, the record from
   `update.installs.<id>.channel` in the Hermes home's `config.yaml`; with no
   record Hermes uses stable for a checkout whose `origin` is the official
   GitHub repository and main otherwise. Main (or a Hermes without channels)
   behaves exactly like mode `main`. Any other channel passes no channel flag
   and Hermes' `--check` is the judge. If the offline read fails, the bare
   `hermes update --check` names the channel on its first line.

A run never writes Hermes' channel setting. Only `hermes-safe-update channel
set stable|main` does, and it asks Hermes to (`hermes update --set-channel`).

On a release channel the stage 2 verdicts are Hermes': an available release
proceeds; "up to date" and "no newer release ... waits for the next release"
end the run with exit 0 (nothing to do); `Could not resolve the stable source
channel` (GitHub unreachable) is a pre-flight stop with Hermes' reason; text
that matches nothing is a warning and a stop, never a silent success.

### Rollback guard

If Hermes says the checkout is newer than the selected release, or git does
(`git merge-base --is-ancestor <release> HEAD`; Hermes v0.21.6 and v0.21.7
call an older release "available"), updating would move it back to that
release, and Hermes does not ask first. The git test runs in stage 2, before
anything is closed. A release commit that is not in the checkout is a forward
update in a full clone; in a shallow clone a move back cannot be ruled out. Safe Update
stops before it closes anything. Attended, it asks for an explicit `Y` naming
both versions (`--yes` does not skip it; any other key or a timeout is exit
4). `--unattended` never rolls back: exit 4, and the message names the
command to do it by hand (`hermes-safe-update run`, attended, or `hermes
update`). `--force` does not skip it, and a release check that fails or times
out stops the run (exit 4) with or without `--force`.

## Nothing changes before the point of no return

Stages 0 to 4 change nothing in Hermes or its settings. (Stage 2 may fetch
commits into the checkout from the local mirror, after refreshing the mirror
from GitHub if it is stale; the tool also writes its log, timing history and
cache files.) The first action that affects Hermes is stage 5, after
you have confirmed (or an unattended run has waited out the busy sessions).
Cancelling before that leaves Hermes running, no marker, and says "Nothing
was changed" on the card (exit 3).

## What "busy" means

A session counts as busy if, in any profile's `state.db`, it has not ended,
it had activity within the idle window, and it is not a cron session. The
database is opened read-only. If it cannot be read (locked, unknown schema,
corrupt) the tool treats that as busy and says so; it never guesses idle.

## Closing the desktop app

The app gets a normal close request, which lets it save its state. If it is
still open after `graceful_close_s`, the log explains why it probably is
(for example a "still working" confirmation dialog), and only then is it
force-closed. Leftover backend and kernel processes of this Hermes home are
stopped afterwards. Hermes' gateway is not touched here; Hermes' own update
pauses and resumes it.

## The gateway pause problem

On some builds `hermes update` on Windows maps every running gateway to a
profile first and aborts, before changing anything, when it cannot. The tool
detects this read-only in stage 3. If it is blocked, or if the real update
dies there, it stops the gateway, runs the update again (once), and starts
the gateway afterwards. If it cannot stop the gateway it aborts without
having changed anything and restarts what it stopped.

## Verification

A run is a success only when:

1. the gateway reports the checkout's new commit and is alive, and
2. Hermes' desktop-build check passes with no output.

Hermes sometimes exits 1 with "Fleet version check returned no rows" even
though the update is fine. If both checks above pass, the tool reports OK
and notes it. On Hermes v0.22 that line is a follow-up with exit code 0 and
shows as a "Not finished" row instead. Anything else is reported as a
problem, with the log path.

Hermes v0.22 folds several per-profile gateways into one on its first update;
only the main Hermes home's `gateway_state.json` is verified. A Hermes desktop
app from the MSIX or Store package running next to a source checkout is not
closed by Safe Update.

## After the update

Settings migration and the `post-update` hooks run before the desktop app is
reopened (the app must be closed to change its storage). Cleanup, cua-driver
and the `post-update` hooks run only for a verified update, and each is wrapped: a failure adds a warning and never
changes the result. The update-in-progress marker is released in all cases,
but only if this process still owns it.

## Files it reads and writes

Under the Hermes home unless noted:

| Path | Who owns it | What the tool does |
|---|---|---|
| `safe-update.yaml` | you | reads |
| `safe-update.json` | the tool | machine state (mirror location, "never offer"); atomic writes |
| `logs/safe-update.log` | the tool | appends; rotated at `logging.max_size_mb` |
| `logs/safe-update-timings.json` | the tool | time history for estimates; atomic writes |
| `.hermes-update-in-progress` | shared with Hermes | claims after close, refreshes every 120 s, releases |
| `backups/settings-*` | the tool | settings backups |
| `backups/settings-migrations.json` | the tool | migration marker (which origins were migrated) |
| `cache/safe-update-settings/` | the tool | work dir for settings migration |
| `.hsu-*` folders inside the desktop data folder | the tool | temporary work copies for the verified swap; removed afterwards |
| `cache/safe-update-gc.py` | the tool | embedded dependency-cleanup helper, written then run with Hermes' Python |
| `cache/safe-update-trash/` | the tool | locked files of removed dependency sets are renamed into it |
| `cache/update-mirror.lock`, `logs/update-mirror.log` | the tool | mirror refresh lock and log |
| `scripts/hermes_safe_update_mirror.py` | the tool | small shim run by the Hermes cron job (`mirror setup` only) |
| `installs/<install>/environments/<old generation>` (old dependency sets) | Hermes | **deleted** by dependency cleanup, see below |
| `state.db`, `profiles/*/state.db` | Hermes | read-only |
| `gateway_state.json` | Hermes | read-only |
| `config.yaml` | Hermes | read-only: only the channel record `update.installs.<id>.channel`. Never written by a run; `channel set` asks Hermes to write it |
| `hermes-agent/.git/config` | Hermes/git | read-only: whether `origin` is the official GitHub repository (Hermes' default channel) |
| the desktop `Local Storage` folder | Hermes | read for plans; written only by the verified swap |

Dependency cleanup (stage 10, Windows only, verified updates only) **deletes**
old dependency generations under the Hermes home. Locked files are renamed
into `cache/safe-update-trash` instead. `mirror setup` clones the Hermes
repository (a full download) into the mirror folder and creates a Hermes cron
job that runs the shim above.

## Time estimates

The footer shows elapsed time, a bar and "about N min left". Estimates are
the mean of your last 5 matching runs per step (matching means same mirror,
source and dependency conditions where recorded). Before there is history it
uses the seed numbers from `tunables.step_seconds`. The bar never moves
backwards. Estimates are a guide, not a promise.

Time estimates learn from your own past updates. The first few updates, and
especially updates that change Node packages, can take longer than the
estimate says. When the tool has fewer than 3 earlier runs like the current
one, it adds "(rough: ...)" to the estimate. The same qualifier
appears on the pre-flight "Update time" row and the confirm prompt; the
footer shows "(rough)".

## Output modes

A real console gets a footer, colours and a taskbar progress indicator.
`NO_COLOR` keeps the footer and drops only the colours. A pipe, a
non-console, `--plain`, `HERMES_SAFE_UPDATE_PLAIN` or `TERM=dumb` gets plain
lines. No decision in
the update depends on which one is used.
