# Desktop settings migration

## The problem

The Hermes desktop app keeps its interface preferences in Chromium's local
storage. Local storage is keyed by the page's origin (its address). On
2026-10-01, for about twelve hours (Hermes `main` commits 587e673e2a to
03477e9ae2), the app's page was served from `http://127.0.0.1:47891` instead
of `file://`. A new origin starts empty, so the app came up without your
preferences even though the update itself succeeded. The values stay in the
same `Local Storage` folder under their origin.

Hermes reverted that change the same day
([#130852](https://github.com/NousResearch/hermes-agent/pull/130852); the
report, [#130635](https://github.com/NousResearch/hermes-agent/issues/130635),
was closed). Settings saved before the affected build are read back from
`file://`; settings changed while on it stay under the other origin and do not
carry over (Hermes' note on the closed report). hermes-safe-update offers a
workaround for that gap: it copies the values to the origin the app now uses,
with a backup and `revert-settings` to undo it. If there is nothing to copy,
the step does nothing. It never
edits the storage files by hand. It runs Hermes' own bundled Electron so
that Chromium itself reads and writes the data.

## Commands

| Command | What it does |
|---|---|
| `migrate-settings` | Back up, then migrate. Refuses while the desktop app runs (exit 6). |
| `migrate-settings --dry-run` | Print the plan; change nothing. Allowed while Hermes runs (it works on a copy). |
| `migrate-settings --origin URL` | Use this target origin instead of the detected one. |
| `backup-settings` | Back up now. Refuses while the desktop app runs (exit 6). |
| `list-backups` | List backups (id, UTC time, reason). |
| `revert-settings [--backup ID]` | Restore a backup (newest by default). Refuses while the desktop app runs. |

After every verified update the tool runs the equivalent of
`migrate-settings` itself (`settings.auto_migrate`, on by default), while
Hermes is still closed.

## Which origins

- **Target**: `settings.origin` or `--origin` if set. Otherwise the
  `DEFAULT_PORT` found in the installed build's
  `apps/desktop/electron/renderer-server.ts`. If the store already holds a
  different `http://127.0.0.1:<port>` origin that was used more recently (the
  app fell back to another port because 47891 was busy), the tool migrates
  into that one and says so. Newer Hermes builds have no
  `renderer-server.ts` and load the renderer from disk again; when
  `apps/desktop/electron/main.ts` shows that (`pathToFileURL(resolveRendererIndex`
  and no import of `renderer-server`) the target is `file://`. If neither
  layout is recognised the tool does not guess: the update's settings stage
  takes a backup anyway and prints its id and the command to run
  (`hermes-safe-update migrate-settings --origin <url>`).
- **Source**: the most recently used other origin that has keys. Often
  `file://`; with a `file://` target (builds without `renderer-server.ts`) it
  is the `http://127.0.0.1:<port>` origin. After a later port change it is the
  previous port. When two
  origins look equally recent (common after LevelDB compaction), an origin
  that was a migration target before wins, then `http` over `file://`, then
  the shorter address. `file://` never wins a tie.

## Merge rules

For each key in the source origin:

| Situation | Action |
|---|---|
| Key only in the source | copied (`add`) |
| Same value on both sides | nothing (`same`) |
| Key matches `settings.keep_new` | the target's value stays (`keep-new`) |
| Key matches `settings.union` | merged (`union`): objects and `[id, value]` pair lists are combined, and the target's entry wins for the same id. A target that already contains every source entry counts as `same`. If the values are not such JSON, the plain rule below applies. |
| Any other key (a plain preference) | **first migration** for this source-to-target pair: the source value wins (`old-wins`). **Later runs:** the target value wins (`new-wins`). |
| Key only in the target | untouched (`only-new`) |

Why plain preferences are source-wins the first time: the source is the
origin you used most recently and holds the choices you made there, while the
target holds older values or defaults. After that, the target is the one you
are using. (The label in the report is still `old-wins`.)

Default key lists (patterns use `*` as the only wildcard):

- `keep_new`: `hermes.desktop.lastRoute.profile.*`,
  `hermes.desktop.lastSessionId.profile.*`, `hermes.desktop.freshDraftKey`,
  `hermes.updates.last-passive-check`, `hermes.desktop.tips.next.v1`
- `union`: `hermes.desktop.threadScroll.v1.profile.*`,
  `hermes.desktop.sessionSeenCounts`, `hermes.desktop.unreadFinishedSessions`,
  `hermes.desktop.sessionOwnerHints.v1`, `hermes.desktop.toolDisclosure.v1`

A conflict (`old-wins` or `new-wins` on a plain key with different values) is
listed by key name in the report so you can see what changed.

### Not bringing things back

The tool records `source -> target` with a SHA-256 of the source's content
in `<backup dir>/settings-migrations.json` (it never deletes this file).
When the source still hashes the same on a later run, nothing is written, so
keys or entries you removed from the new origin stay removed. If the source
changed (you ran an old build again), the later-run rules apply.

A run that would write nothing also takes **no backup**, so the automatic
run after every update cannot push your pre-migration backup out of the
retention window.

## What a migration does

1. Refuse if the desktop app is running, or if the process list cannot be read.
2. Plan on a read-only copy. Nothing to write? Report it and stop.
3. Back up (`before migration`).
4. Copy the backup's store next to the live one (same drive).
5. Write the keys on that copy through Electron.
6. Re-plan in a fresh process: it must find nothing left to write.
7. Check again that Hermes is still closed.
8. Swap the copy in with two renames.
9. Check the live store the same way. If that fails, rename the original
   back (automatic rollback).

If anything fails before step 8, the live store is byte-for-byte what it was.

## Dry-run and report format

`migrate-settings --dry-run` prints:

```
from file:// to http://127.0.0.1:47891 (<why this target>)
  add       hermes.desktop.example.key
  old-wins  hermes.desktop.other.key
  ...
would write 12 key(s); nothing was changed (dry run)
```

Only key names are printed, never values (values can contain session titles
and paths). A real run prints one line, for example:

```
settings: migrated file:// -> http://127.0.0.1:47891 (add 40 | union 3 | old-wins 5 | keep-new 2 | new-wins 0 | same 7 | only-new 4 | 0 conflicts); backup settings-20261001-120000
```

Other first words: `settings: nothing to migrate (...)`,
`settings: <source> unchanged since the last migration to <target>; nothing to write`,
`settings: already migrated ...`, `settings: no settings store yet; nothing to migrate`.
Exit code 0 for all of these.

## Backups

Folder: `<hermes home>\backups\settings-<UTC yyyymmdd-hhmmss>[-N]\` (change
with `settings.backup_dir`).

Contents: the desktop's `Local Storage` folder (without LevelDB's `LOCK`
file), the top-level `*.json` files of the desktop's data folder, a copy of
the migration marker, and `manifest.json` (SHA-256 of every file, key counts
per origin when Electron is available, reason, UTC time, and the Hermes
version before and after the update; a stand-alone `backup-settings` or
`migrate-settings` records the current version for both).

The newest `settings.backup_retention` (default 3) are kept. Only folders
named `settings-*` that have a manifest are counted or removed.

## Revert

```
hermes-safe-update list-backups
hermes-safe-update revert-settings --backup settings-20261001-120000
```

With no `--backup` the newest is used. Revert refuses while the desktop app
runs, verifies every hash in the backup, stages it next to the live store
and verifies again, takes a "before revert" backup of the current state,
swaps, restores the JSON files, verifies the live hashes (rolling back on a
mismatch) and restores the migration marker to its state at backup time.
Reverting is itself reversible: use the "before revert" backup.

## Port fallback

If Hermes could not bind port 47891 and used another, its origin is
`http://127.0.0.1:<other>`. The tool prefers whichever 127.0.0.1 origin the
store shows as most recently used, over the compiled-in default, and the
first line of the plan says "store already uses fallback port ...". If it
guesses wrong, pass `--origin`.

## Limits

- Needs Hermes' bundled Electron (inside the checkout's `apps/desktop`). If
  it is missing, the command fails with a clear message and changes nothing.
- Tested with the real Electron on Windows, in a sandbox on a copy of a real
  desktop settings store and in an automated test on synthetic data. macOS and
  Linux paths for the desktop data folder exist but are untested.
