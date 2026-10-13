# Example hook: keep your edited bundled skills up to date

A `post-update` hook for [hermes-safe-update](../../../README.md) that merges
upstream changes into the bundled Hermes skills you have edited.

## The problem it solves

`hermes update` syncs the skills that ship with Hermes using a manifest
(`<home>/skills/.bundled_manifest`: skill name, then a hash of the bundled
copy that was last synced). A skill whose folder no longer matches that hash
is reported as "user-modified (kept)" and is **never updated again**, so
upstream fixes pile up behind your edits and every update prints a reminder.
`hermes skills reset` does not help: it makes your edited copy the new
baseline, so the next upstream change overwrites your edits.

## What it does

For every profile (the default one and each `<home>/profiles/<name>`), and
every bundled skill that you edited **and** that changed upstream:

1. It finds the *base*: the bundled copy your edits started from. First a
   snapshot it saved earlier (`<profile>/.bundled-merge-base/<skill>/`,
   verified by hash), else the git history of the Hermes checkout.
2. It does a 3-way merge of every file with `git merge-file` (files added or
   deleted upstream are handled; "you edited it, upstream deleted it" is a
   conflict).
3. Clean result: your folder is copied to
   `<home>/backups/skill-merge-<date>-<time>/<profile>/<skill>/`, the merged
   folder replaces it, the new bundled copy is saved as the next base, and the
   manifest entry is set to the current bundled hash (Hermes stops reporting
   it, and still treats the folder as edited, so it never overwrites it).
4. **Any conflict, an unknown base, or an error before the swap: nothing is
   changed for that skill.** The output line says so and names the command to look at the
   difference (`hermes skills diff <skill>`). Fix it by hand; the next run
   tries again. (If only the bookkeeping after the swap fails, the skill is
   merged and the line says `merged, bookkeeping failed` with the backup.)

Skills you did not edit are left to Hermes. One skill failing never stops the
others.

The hook prints one line per skill it touched, and a summary as its **last**
line, which hermes-safe-update shows on its summary card, for example
`bundled skills: merged upstream changes into 1 edited skill (default); 1 need a decision; 1 kept as-is`.
A conflict or an unknown base is not a failure: it is counted in "need a
decision" in the summary line.

Exit codes (the wrappers pass them through). Any non-zero exit is a warning on
the card; it never affects the update itself, as for every hook.

| Code | Meaning |
|---|---|
| 0 | The run completed. That includes conflicts, unknown bases and kept skills (all in the summary line), `list`, `unkeep`, and a home with no profiles. |
| 1 | A warning. It could not run: no Hermes home, Hermes' skill code could not be loaded (see below), `skill-merge.json` is unreadable, or the wrapper has no `HERMES_SAFE_UPDATE_PYTHON`. Or a skill failed: its merge raised an error, or the merge was done but saving the new base / manifest entry failed (the line says `merged, bookkeeping failed` and names the backup; the summary counts it under "failed"). Or `keep` got a name that is not a bundled skill, or the script crashed (traceback above the last line). |
| 2 | Bad command-line arguments (usage printed). |

## Keep list: skills that must never be merged

`<home>/skill-merge.json`: `{"keep_mine": ["some-skill"]}`, for every profile.
A kept skill is never merged. On each run it is re-baselined so Hermes stops
reporting it and never overwrites your copy; its real base is preserved, so
`unkeep` can merge it later without losing upstream changes. Manage it with:

```
bundled-skill-merge list                     what is edited, kept, mergeable, in conflict
bundled-skill-merge keep NAME                never merge this skill
bundled-skill-merge unkeep NAME              the next run merges it again
bundled-skill-merge merge --dry-run          show what a run would do, change nothing
bundled-skill-merge merge --profile NAME     only that profile (repeatable)
```

(`bundled-skill-merge.cmd` on Windows, `sh bundled-skill-merge.sh` on macOS
and Linux.) `list` and `--dry-run` change no skill, manifest or setting (they
only use a temporary folder under `<home>/cache/scratch`, removed at the end).

## Files

| File | |
|---|---|
| `bundled_skill_merge.py` | The tool. Python 3, standard library plus Hermes' own skill code. |
| `bundled-skill-merge.cmd` | Windows wrapper: runs the script with `HERMES_SAFE_UPDATE_PYTHON`. No argument means `merge`. |
| `bundled-skill-merge.sh` | The same for macOS and Linux (`/bin/sh`; committed with the exec bit). |
| `safe-update.windows.yaml`, `safe-update.posix.yaml` | The hook entry for `safe-update.yaml`. |

## Setup

1. Copy this folder somewhere permanent that is not inside the Hermes
   checkout (an update replaces the checkout's files).
2. Add the hook from `safe-update.windows.yaml` or `safe-update.posix.yaml` to
   your `safe-update.yaml` and change the path to where you put the folder.
   `~` and `$VAR` are not expanded in `run`, so write the full path.
   ```yaml
   hooks:
     - name: "Skill merge"
       when: "post-update"
       run: ["/bin/sh", "/home/you/hermes-hooks/bundled-skill-merge/bundled-skill-merge.sh"]
       timeout_s: 240
   ```
   On Windows: `run: ["cmd.exe", "/c", 'C:\Users\you\hermes-hooks\bundled-skill-merge\bundled-skill-merge.cmd']`.
3. Try it first: `list`, then `merge --dry-run` (see below for the environment
   a manual run needs).

It needs `git` for the file merges. On Windows it uses `git` from PATH, else
Hermes' own under `<home>/tools/git-*`. On macOS and Linux it uses `git` from
PATH only.

## How it finds Hermes

hermes-safe-update gives every hook these environment variables (see
[docs/configuration.md](../../../docs/configuration.md#hooks)); the wrappers
and the script use them, so nothing is hard-coded and nothing depends on where
this folder is:

- `HERMES_SAFE_UPDATE_PYTHON`: the Python Hermes installs for itself, looked
  up when the hook starts. After an update that is the new interpreter. The
  wrapper stops with a one-line message and exit code 1 if it is not set or
  does not exist.
- `HERMES_SAFE_UPDATE_HOME`: the Hermes home (the root, not a profile).
- `HERMES_SAFE_UPDATE_CHECKOUT`: the Hermes source checkout (default
  `<home>/hermes-agent`).

For a manual run outside a hook, set `HERMES_SAFE_UPDATE_PYTHON` and
`HERMES_SAFE_UPDATE_HOME` yourself (or give the home as `--home DIR` before
the command, and the checkout as `--agent-dir DIR`), then run the wrapper.
Windows (cmd):

```
set HERMES_SAFE_UPDATE_PYTHON=C:\path\to\hermes\tools\python-VERSION\python.exe
set HERMES_SAFE_UPDATE_HOME=C:\path\to\hermes
bundled-skill-merge.cmd list
```

macOS/Linux:

```
HERMES_SAFE_UPDATE_PYTHON=/path/to/python3 HERMES_SAFE_UPDATE_HOME=/path/to/hermes-home \
  sh bundled-skill-merge.sh list
```

## Depends on Hermes internals

The script imports `tools.skills_sync` from the Hermes checkout (the hash and
discovery helpers Hermes itself uses; it needs them to agree with Hermes about
what "edited" means). If the bare Python lacks Hermes' packages it calls
`pm.environments.activate_dependencies` from the checkout to select them.
These are private Hermes interfaces: a future Hermes version can rename them.
If the import fails, or a helper it needs is gone, the script prints
`bundled skills: skipped - ...` with the reason, exits 1 and **changes
nothing**. Then this example needs an update; it is not a data risk.

It writes into your Hermes home: the skill folders you edited,
`skills/.bundled_manifest`, `skill-merge.json`, `backups/`, and per profile
`.bundled-merge-base/` (snapshots and `state.json`, kept outside `skills/` on
purpose so skill scanners do not index them twice).

**Backups.** Every run that edits something creates
`<home>/backups/skill-merge-<date>-<time>/` first. In it:

- `<profile>/<skill>/`: your skill folder as it was before the merge.
- `.bookkeeping/`: the other files, copied before the run first edits them,
  in the same layout as under the home, so restoring is copying them back:
  `skill-merge.json` (for `keep` and `unkeep`), and per profile
  `<profile>/skills/.bundled_manifest`,
  `<profile>/.bundled-merge-base/state.json` and
  `<profile>/.bundled-merge-base/<skill>/` (the old saved base). The profile
  `default` is the home itself; another profile is `<home>/profiles/<name>`.
  A file that did not exist yet is not copied.

If a copy fails, that skill is left unchanged. Read `backups/skill-merge-*`
before you delete anything there; nothing removes old ones for you.

## Platforms and what was checked

- **Windows:** run with the `.cmd` wrapper against a scratch copy of a Hermes
  home (a synthetic set of four edited skills: clean merge, conflict, edited
  with no upstream change, kept): `list`, `merge --dry-run`, `merge` (twice),
  `keep`, `unkeep`. The merged skill had both your edit and the upstream edit,
  the conflict was left untouched, the backup existed.
- **Linux:** the same sequence with the `.sh` wrapper under `dash` and
  `bash --posix` on WSL2 (Debian, system Python 3.13), against a scratch copy.
  That used the system Python, not a Python installed by Hermes on Linux.
- **macOS: untested.** hermes-safe-update does not verify the layout of
  Hermes' managed Python on macOS and Linux; if `HERMES_SAFE_UPDATE_PYTHON`
  is empty there, the wrapper says so and exits 1.
- Automated tests in the repository cover the wrappers (with a stand-in
  Python), that the snippets load, the script's "skipped" messages, and the
  script's merge, backup, `keep`/`unkeep`, exit-code and `HERMES_HOME`
  behaviour against a stand-in for Hermes' skill code (a synthetic home, one small skill). They do not run against Hermes' real code.

## Licence

MIT, the same licence as hermes-safe-update (see [LICENSE](../../../LICENSE)).
Original code, apart from the manifest read/write idiom (`read_manifest` and
`set_manifest_entry`), which follows Hermes Agent's `tools/skills_sync.py`
(MIT, Copyright (c) 2025 Nous Research). No Hermes code is bundled; the script
imports the user's installed copy at run time.
