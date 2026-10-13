#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
# Part of hermes-safe-update (https://github.com/cloudn8ive/hermes-safe-update), MIT License:
# see the LICENSE file at the root of that repository.
"""Merge upstream changes into the user's EDITED copies of Hermes bundled skills.

Why: `hermes update` syncs bundled skills with a manifest (<skills>/.bundled_manifest: skill name ->
hash of the bundled copy last synced).  A skill whose user copy differs from that hash is
"user-modified (kept)" and is NEVER updated again, so upstream fixes pile up behind it and every
update prints a nag.  `hermes skills reset` is no answer (it re-baselines on the edited copy, so the
next upstream change silently overwrites the edits).

What this does, per profile (default + profiles/<name>), for each bundled skill that is
user-modified AND changed upstream:
  1. find the BASE tree (the bundled copy the manifest hash points at): first the saved snapshot
     <profile>/.bundled-merge-base/<name>/ (verified by hash), else git history of the checkout;
  2. 3-way merge every file (git merge-file; added/deleted files handled; delete-vs-edit = conflict);
  3. clean result: back the user's copy up to <home>/backups/skill-merge-<ts>/<profile>/<name>/,
     swap the merged tree in, save the new bundled copy as the next base, and set the manifest entry
     to the current bundled hash (core goes quiet; the copy stays protected because it still differs);
  4. any conflict / unknown base: change NOTHING for that skill and say how to look.

Keep-mine list: <home>/skill-merge.json {"keep_mine": [...]}  (every profile).  A kept skill is never
merged; each run re-baselines its manifest entry (nag stops, copy never overwritten) while the real
base is preserved, so `unkeep` can merge it later without losing upstream changes.

Snapshots live OUTSIDE <profile>/skills/ on purpose: skill scanners would index every SKILL.md in a
dot-directory there as a duplicate skill.

Usage:
  bundled_skill_merge.py [--home DIR] merge [--dry-run] [--profile P ...]
  bundled_skill_merge.py [--home DIR] keep NAME | unkeep NAME | list [--profile P ...]
Global options: --home DIR, --agent-dir DIR, --bundled DIR.
  Hermes home:  --home, else $HERMES_SAFE_UPDATE_HOME (set by hermes-safe-update for its hooks).
  Hermes code:  --agent-dir, else $HERMES_SAFE_UPDATE_CHECKOUT, else <home>/hermes-agent.
Run it with the Python that Hermes installs ($HERMES_SAFE_UPDATE_PYTHON in a hook); the wrappers
bundled-skill-merge.cmd / .sh do that.  It imports Hermes' own skill-sync code (tools.skills_sync, and
pm.environments to select Hermes' dependency set when the bare interpreter lacks them); if that cannot
be imported it stops with a clear "skipped" line and exit code 1 and changes nothing.
The LAST stdout line of `merge` is a one-line summary (for the safe-update summary card).
Exit code: 0 = the run completed (conflicts and unknown bases are reported in the summary, not failures);
1 = a warning: it could not run, or a skill's merge failed, or `keep` got a name that is not bundled; 2 = bad arguments.
Before it edits the manifest, a state file, a saved base or skill-merge.json it copies them into the
run's backup folder (<home>/backups/skill-merge-<ts>/.bookkeeping/, same layout as under the home).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import traceback
from pathlib import Path

CONFIG_NAME = "skill-merge.json"
BASE_DIRNAME = ".bundled-merge-base"
BOOKKEEPING_DIRNAME = ".bookkeeping"  # inside a run's backup folder
TMP_DIRNAME = ".bundled-merge-tmp"
STATE_NAME = "state.json"
GIT_LOG_LIMIT = 400
GIT_BUDGET_S = 120.0

_NOWIN = getattr(subprocess, "CREATE_NO_WINDOW", 0)


class MergeError(Exception):
    pass


class BookkeepingError(MergeError):
    """The skill folder WAS merged, but saving the new base / manifest entry afterwards failed."""


# --------------------------------------------------------------------------- core loading

class CoreUnavailable(Exception):
    """Hermes' own skill-sync code cannot be loaded (not found, or its internals changed)."""


# Private helpers of tools.skills_sync that this script relies on.  If a Hermes update renames one,
# say so instead of crashing half way through a merge.
_CORE_API = ("_dir_hash", "_matches_origin_hash", "_is_runtime_cache", "_discover_bundled_skills",
             "_get_bundled_dir")


def load_core(home: Path, agent_dir: Path):
    """Import Hermes' skills_sync (hash + discovery helpers).  Raises CoreUnavailable if impossible."""
    # Always the home this run works on: a HERMES_HOME left in the shell (a manual run with --home) must
    # not make Hermes' code read another home.
    os.environ["HERMES_HOME"] = str(home)
    if str(agent_dir) not in sys.path:
        sys.path.insert(0, str(agent_dir))
    try:
        import tools.skills_sync as ss
    except ImportError as first:
        # Bare managed Python: select Hermes' dependency set, then try again.
        try:
            from pm.environments import activate_dependencies
            activate_dependencies(Path(agent_dir))
            for mod in [m for m in sys.modules if m.split(".")[0] in ("tools", "utils", "hermes_yaml")]:
                del sys.modules[mod]
            import tools.skills_sync as ss
        except (ImportError, RuntimeError, OSError) as exc:
            raise CoreUnavailable(
                f"cannot import Hermes' skill code from {agent_dir} ({first}; then {type(exc).__name__}: {exc}). "
                "Is HERMES_SAFE_UPDATE_CHECKOUT / --agent-dir the Hermes checkout, and was this run "
                "with the Python that Hermes installs?") from exc
    missing = [n for n in _CORE_API if not hasattr(ss, n)]
    if missing:
        raise CoreUnavailable("this Hermes version no longer has tools.skills_sync." + ", ".join(missing)
                              + "; this example needs an update")
    return ss


def find_git(home: Path) -> str | None:
    found = shutil.which("git")
    if found:
        return found
    for pattern in ("tools/git-*/cmd/git.exe", "tools/git-*/bin/git.exe", "tools/git-*/mingw64/bin/git.exe"):
        for cand in sorted(home.glob(pattern)):
            return str(cand)
    return None


def _run(cmd, *, input=None, timeout=60, env=None):
    return subprocess.run(cmd, input=input, capture_output=True, timeout=timeout, env=env,
                          stdin=subprocess.DEVNULL if input is None else None, creationflags=_NOWIN)


# --------------------------------------------------------------------------- small fs helpers

def _rmtree(path: Path) -> None:
    def onerror(func, p, _exc):
        try:
            os.chmod(p, stat.S_IWRITE)
            func(p)
        except OSError:
            pass
    if path.exists():
        shutil.rmtree(path, onerror=onerror)


def atomic_write(path: Path, data: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=str(path.parent), prefix=".bsm_", suffix=".tmp")
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="") as fh:
            fh.write(data)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


# Attribution: the manifest format (one "name:hash" line per skill; a plain-name v1 line reads as an
# empty hash) and the read / write idioms of read_manifest and set_manifest_entry follow Hermes Agent's
# tools/skills_sync.py (_read_manifest, _write_manifest): MIT, Copyright (c) 2025 Nous Research.
# Re-implemented here in a few lines (own atomic write), not copied: no Hermes code is bundled.
def read_manifest(path: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    try:
        for line in path.read_text(encoding="utf-8-sig").splitlines():
            line = line.strip()
            if not line:
                continue
            name, sep, h = line.partition(":")
            result[name.strip()] = h.strip() if sep else ""
    except OSError:
        pass
    return result


# Attribution: see read_manifest (the v2 format and the sorted "name:hash" write follow Hermes Agent's
# tools/skills_sync.py, MIT, Copyright (c) 2025 Nous Research).
def set_manifest_entry(path: Path, name: str, value: str) -> None:
    """Fresh read-modify-write of one entry, same v2 format and ordering core writes."""
    entries = read_manifest(path)
    entries[name] = value
    atomic_write(path, "".join(f"{n}:{h}\n" for n, h in sorted(entries.items())))


def tree_files(ss, d: Path) -> dict[str, bytes]:
    """{posix relpath: bytes} for every file _dir_hash would count."""
    out: dict[str, bytes] = {}
    if not d.is_dir():
        return out
    for p in sorted(d.rglob("*")):
        if p.is_file() and not ss._is_runtime_cache(p, d):
            out[p.relative_to(d).as_posix()] = p.read_bytes()
    return out


def write_tree(files: dict[str, bytes], dest: Path) -> None:
    dest.mkdir(parents=True, exist_ok=True)
    for rel, data in files.items():
        target = dest / Path(*rel.split("/"))
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)


# --------------------------------------------------------------------------- config (keep-mine)

def config_path(home: Path) -> Path:
    return home / CONFIG_NAME


def read_keep(home: Path) -> list[str]:
    """Kept names.  A missing file is an empty list; an unreadable one raises (never guess: a
    corrupted keep list must not turn into 'merge everything')."""
    p = config_path(home)
    if not p.exists():
        return []
    try:
        data = json.loads(p.read_text(encoding="utf-8-sig"))
        names = data.get("keep_mine", [])
        if not isinstance(names, list) or not all(isinstance(n, str) for n in names):
            raise ValueError("keep_mine must be a list of skill names")
        return sorted(set(names))
    except (OSError, ValueError, AttributeError) as exc:
        raise MergeError(f"{p} is unreadable ({exc}); fix or delete it") from exc


def write_keep(home: Path, names) -> None:
    p = config_path(home)
    data: dict = {}
    try:
        data = json.loads(p.read_text(encoding="utf-8-sig")) if p.exists() else {}
        if not isinstance(data, dict):
            data = {}
    except (OSError, ValueError):
        data = {}
    data["keep_mine"] = sorted(set(names))
    atomic_write(p, json.dumps(data, indent=2) + "\n")


# --------------------------------------------------------------------------- per-profile state

def read_state(profile_home: Path) -> dict:
    try:
        data = json.loads((profile_home / BASE_DIRNAME / STATE_NAME).read_text(encoding="utf-8"))
        return data if isinstance(data, dict) else {}
    except (OSError, ValueError):
        return {}


def write_state(profile_home: Path, state: dict) -> None:
    atomic_write(profile_home / BASE_DIRNAME / STATE_NAME, json.dumps(state, indent=2, sort_keys=True) + "\n")


def discover_profiles(home: Path, only: list[str] | None):
    out = []
    if (home / "skills" / ".bundled_manifest").is_file():
        out.append(("default", home))
    prof_root = home / "profiles"
    if prof_root.is_dir():
        for d in sorted(prof_root.iterdir()):
            if d.is_dir() and (d / "skills" / ".bundled_manifest").is_file():
                out.append((d.name, d))
    if only:
        wanted = set(only)
        out = [p for p in out if p[0] in wanted]
    return out


# --------------------------------------------------------------------------- git base lookup

class GitBase:
    """Finds the bundled tree whose _dir_hash equals a manifest origin hash, from git history."""

    def __init__(self, ss, git: str | None, bundled_dir: Path, scratch: Path):
        self.ss, self.git, self.bundled_dir, self.scratch = ss, git, bundled_dir, scratch
        self.cache: dict[tuple[str, str], dict[str, bytes] | None] = {}
        self.deadline = time.monotonic() + GIT_BUDGET_S
        self._root: Path | None = None
        self._root_failed = False

    def _env(self):
        env = dict(os.environ)
        env["GIT_NO_LAZY_FETCH"] = "1"  # partial clone: never go to the network from here
        env["GIT_TERMINAL_PROMPT"] = "0"
        return env

    def _root_dir(self) -> Path | None:
        if self._root or self._root_failed or not self.git:
            return self._root
        try:
            p = _run([self.git, "-C", str(self.bundled_dir), "rev-parse", "--show-toplevel"],
                     timeout=20, env=self._env())
            if p.returncode == 0 and p.stdout.strip():
                self._root = Path(p.stdout.decode("utf-8", "replace").strip()).resolve()
        except (OSError, subprocess.SubprocessError):
            pass
        self._root_failed = self._root is None
        return self._root

    def find(self, name: str, skill_dir: Path, want: str) -> dict[str, bytes] | None:
        key = (name, want)
        if key in self.cache:
            return self.cache[key]
        self.cache[key] = None
        root = self._root_dir()
        if root is None:
            return None
        try:
            rel = skill_dir.resolve().relative_to(root).as_posix()
        except ValueError:
            return None
        env = self._env()
        try:
            p = _run([self.git, "-c", "gc.auto=0", "-C", str(root), "log", "--format=%H",
                      f"-n{GIT_LOG_LIMIT}", "--", rel], timeout=60, env=env)
        except (OSError, subprocess.SubprocessError):
            return None
        if p.returncode != 0:
            return None
        seen: set[tuple] = set()
        for rev in p.stdout.decode().split():
            if time.monotonic() > self.deadline:
                break
            files = self._tree_at(root, rev, rel, env, seen)
            if files is None:
                continue
            tmp = Path(tempfile.mkdtemp(prefix="base-", dir=str(self.scratch)))
            try:
                write_tree(files, tmp)
                if self.ss._dir_hash(tmp) == want:
                    self.cache[key] = files
                    return files
            finally:
                _rmtree(tmp)
        return None

    def _tree_at(self, root: Path, rev: str, rel: str, env, seen: set):
        try:
            ls = _run([self.git, "-C", str(root), "ls-tree", "-r", "-z", rev, "--", rel], timeout=30, env=env)
        except (OSError, subprocess.SubprocessError):
            return None
        if ls.returncode != 0 or not ls.stdout:
            return None
        entries = []
        for rec in ls.stdout.split(b"\0"):
            if not rec:
                continue
            meta, _, path = rec.partition(b"\t")
            parts = meta.split()
            if len(parts) == 3 and parts[1] == b"blob":
                entries.append((path.decode("utf-8", "replace"), parts[2].decode()))
        sig = tuple(sorted(entries))
        if not entries or sig in seen:
            return None
        seen.add(sig)
        try:
            cat = _run([self.git, "-C", str(root), "cat-file", "--batch"],
                       input="".join(oid + "\n" for _, oid in entries).encode(), timeout=60, env=env)
        except (OSError, subprocess.SubprocessError):
            return None
        data, pos, files = cat.stdout, 0, {}
        for path, oid in entries:
            nl = data.find(b"\n", pos)
            if nl < 0:
                return None
            header = data[pos:nl].split()
            if len(header) != 3 or header[1] != b"blob":
                return None  # "<oid> missing"
            size = int(header[2])
            files[path[len(rel) + 1:]] = data[nl + 1:nl + 1 + size]
            pos = nl + 1 + size + 1
        return files


# --------------------------------------------------------------------------- 3-way merge

def merge_file(git: str, scratch: Path, base: bytes, ours: bytes, theirs: bytes):
    """git merge-file on three byte strings.  Returns (clean, merged_bytes_or_None)."""
    d = Path(tempfile.mkdtemp(prefix="mf-", dir=str(scratch)))
    try:
        (d / "mine").write_bytes(ours)
        (d / "base").write_bytes(base)
        (d / "upstream").write_bytes(theirs)
        p = _run([git, "merge-file", "-p", "-L", "mine", "-L", "base", "-L", "upstream",
                  str(d / "mine"), str(d / "base"), str(d / "upstream")], timeout=60)
        if p.returncode == 0:
            return True, p.stdout
        return False, None
    finally:
        _rmtree(d)


def _is_pure_crlf(data: bytes) -> bool:
    """Every line ends CRLF and there is no other CR: safe to round-trip through LF."""
    return b"\r\n" in data and data.count(b"\r") == data.count(b"\r\n") == data.count(b"\n")


def three_way(git: str, scratch: Path, base: dict, ours: dict, theirs: dict):
    """Per-file 3-way merge.  Returns (result files, stats, conflicts[(path, why)])."""
    result: dict[str, bytes] = {}
    stats = {"merged": [], "upstream": [], "added": [], "deleted": [], "mine": 0}
    conflicts: list[tuple[str, str]] = []
    for rel in sorted(set(base) | set(ours) | set(theirs)):
        b, o, t = base.get(rel), ours.get(rel), theirs.get(rel)
        if o == t:
            res = o
        elif o == b:  # user never touched it: take upstream (incl. add / delete)
            res = t
            (stats["added"] if b is None else stats["deleted"] if t is None else stats["upstream"]).append(rel)
        elif t == b:  # upstream never touched it: keep user's version (incl. user add / delete)
            res = o
            stats["mine"] += 1
        else:  # both sides changed it differently
            if b is None:
                conflicts.append((rel, "added on both sides with different content"))
                continue
            if o is None:
                conflicts.append((rel, "you deleted it, upstream edited it"))
                continue
            if t is None:
                conflicts.append((rel, "you edited it, upstream deleted it"))
                continue
            # Windows-edited copies are often all-CRLF while the bundled files are LF: merge on LF and put
            # the user's line endings back, otherwise every line "differs" and any upstream change conflicts.
            crlf = _is_pure_crlf(o) and b"\r" not in b and b"\r" not in t
            clean, merged = merge_file(git, scratch, b, o.replace(b"\r\n", b"\n") if crlf else o, t)
            if clean and crlf:
                merged = merged.replace(b"\n", b"\r\n")
            if not clean:
                conflicts.append((rel, "overlapping edits"))
                continue
            res = merged
            stats["merged"].append(rel)
        if res is not None:
            result[rel] = res
    return result, stats, conflicts


# --------------------------------------------------------------------------- engine

class Engine:
    def __init__(self, home: Path, agent_dir: Path, bundled: Path | None, dry: bool = False, out=print):
        self.home, self.agent_dir, self.dry, self.out = home, agent_dir, dry, out
        self.ss = load_core(home, agent_dir)
        self.bundled = bundled or self.ss._get_bundled_dir()
        self.git = find_git(home)
        self.scratch_root = home / "cache" / "scratch"
        self.scratch_root.mkdir(parents=True, exist_ok=True)
        self.scratch = Path(tempfile.mkdtemp(prefix="bsm-", dir=str(self.scratch_root)))
        self.skills = sorted(self.ss._discover_bundled_skills(self.bundled), key=lambda x: x[0])
        self._bhash = {name: self.ss._dir_hash(d) for name, d in self.skills}
        self.gitbase = GitBase(self.ss, self.git, self.bundled, self.scratch)
        self.stamp = time.strftime("%Y%m%d-%H%M%S")
        self.keep = set(read_keep(home))
        self.backup_root = home / "backups" / f"skill-merge-{self.stamp}"
        n = 1
        while self.backup_root.exists():  # two runs inside one second must not share a backup folder
            n += 1
            self.backup_root = home / "backups" / f"skill-merge-{self.stamp}-{n}"
        self.results: list[dict] = []
        self._backed_up: set[Path] = set()

    def close(self):
        _rmtree(self.scratch)

    # -- helpers
    def backup_before_edit(self, root: Path, path: Path, label: str | None = None) -> None:
        """Copy a bookkeeping file or folder into this run's backup folder before the run edits it, once
        per run (the first copy is the state from before the run).  The copy mirrors the layout under
        <root>: <backup>/.bookkeeping/<label>/<path relative to root>, or without <label> for the
        home-level skill-merge.json.  A path that does not exist yet has nothing to save.  If the copy
        fails the error propagates, so the caller has not edited anything yet."""
        if path in self._backed_up:
            return
        self._backed_up.add(path)
        if not path.exists():
            return
        dest = self.backup_root / BOOKKEEPING_DIRNAME / (label or "") / path.relative_to(root)
        dest.parent.mkdir(parents=True, exist_ok=True)
        if path.is_dir():
            shutil.copytree(path, dest)
        else:
            shutil.copy2(path, dest)

    def backup_profile_files(self, profile: str, profile_home: Path, name: str) -> None:
        """Manifest, state file and the saved base of one skill: everything a merge or keep rewrites."""
        for path in (profile_home / "skills" / ".bundled_manifest",
                     profile_home / BASE_DIRNAME / STATE_NAME,
                     profile_home / BASE_DIRNAME / name):
            self.backup_before_edit(profile_home, path, profile)

    def _say(self, profile, name, msg):
        self.out(f"[{profile}] {name}: {msg}")

    def _diff_cmd(self, profile, name):
        return f"hermes skills diff {name}" if profile == "default" else f"hermes -p {profile} skills diff {name}"

    def _base_for(self, profile_home, name, sdir, want):
        """(files, source) of the tree whose hash == want, or (None, why)."""
        snap = profile_home / BASE_DIRNAME / name
        if snap.is_dir() and self.ss._dir_hash(snap) == want:
            return tree_files(self.ss, snap), "snapshot"
        files = self.gitbase.find(name, sdir, want)
        if files is not None:
            return files, "git history"
        return None, ("no git" if not self.git else "not in snapshot or git history")

    def _save_snapshot(self, profile, profile_home, name, files: dict[str, bytes], expect_hash: str):
        self.backup_before_edit(profile_home, profile_home / BASE_DIRNAME / name, profile)
        base_dir = profile_home / BASE_DIRNAME
        tmp_root = profile_home / TMP_DIRNAME
        tmp_root.mkdir(parents=True, exist_ok=True)
        stage = tmp_root / f"snap-{name}-{os.getpid()}"
        _rmtree(stage)
        write_tree(files, stage)
        if self.ss._dir_hash(stage) != expect_hash:
            _rmtree(stage)
            raise MergeError("snapshot hash mismatch")
        base_dir.mkdir(parents=True, exist_ok=True)
        final = base_dir / name
        old = tmp_root / f"snapold-{name}-{os.getpid()}"
        _rmtree(old)
        if final.exists():
            os.replace(final, old)
        os.replace(stage, final)
        _rmtree(old)

    def _cleanup_tmp(self, profile_home):
        t = profile_home / TMP_DIRNAME
        try:
            t.rmdir()
        except OSError:
            pass

    # -- classification
    def plan(self, profile, profile_home, only: set[str] | None = None):
        """Yield one dict per bundled skill that is user-modified (core's own test)."""
        ss = self.ss
        skills_dir = profile_home / "skills"
        manifest = read_manifest(skills_dir / ".bundled_manifest")
        state = read_state(profile_home)
        for name, sdir in self.skills:
            if only is not None and name not in only:
                continue
            origin = manifest.get(name)
            if not origin:
                continue
            dest = skills_dir / sdir.relative_to(self.bundled)
            if not dest.is_dir():
                continue
            bh = self._bhash[name]
            user_hash = ss._dir_hash(dest)
            if ss._matches_origin_hash(dest, origin, user_hash):
                continue  # pristine: core's own sync updates it
            pending = (state.get(name) or {}).get("base_hash")
            changed = bh != origin or bool(pending and pending != bh)
            yield {"profile": profile, "home": profile_home, "name": name, "sdir": sdir, "dest": dest,
                   "bundled_hash": bh, "origin": origin, "want": pending or origin, "pending": pending,
                   "changed": changed, "kept": name in self.keep, "user_hash": user_hash}

    # -- actions
    def evaluate(self, item):
        """Compute the merge for an upstream-changed, user-modified skill without writing anything.
        Returns item updated with status: mergeable | conflict | no-base | error."""
        base, src = self._base_for(item["home"], item["name"], item["sdir"], item["want"])
        if base is None:
            item.update(status="no-base", why=src)
            return item
        if not self.git:
            item.update(status="error", why="git not found")
            return item
        ours = tree_files(self.ss, item["dest"])
        theirs = tree_files(self.ss, item["sdir"])
        result, stats, conflicts = three_way(self.git, self.scratch, base, ours, theirs)
        item.update(base=base, base_src=src, ours=ours, theirs=theirs, result=result, stats=stats,
                    conflicts=conflicts, status="conflict" if conflicts else "mergeable")
        return item

    def apply_merge(self, item):
        ss, profile_home, name, dest = self.ss, item["home"], item["name"], item["dest"]
        tmp_root = profile_home / TMP_DIRNAME
        tmp_root.mkdir(parents=True, exist_ok=True)
        stage = tmp_root / f"stage-{name}-{os.getpid()}"
        old = tmp_root / f"old-{name}-{os.getpid()}"
        _rmtree(stage)
        _rmtree(old)
        write_tree(item["result"], stage)
        # backup of the user's copy first (anything wrong -> nothing has changed yet)
        bdest = self.backup_root / item["profile"] / name
        shutil.copytree(dest, bdest)
        self.backup_profile_files(item["profile"], profile_home, name)
        try:
            os.replace(dest, old)
        except OSError as exc:
            _rmtree(stage)
            raise MergeError(f"could not move your copy aside ({exc}); unchanged") from exc
        try:
            os.replace(stage, dest)
        except OSError as exc:
            try:
                os.replace(old, dest)
            except OSError:
                pass
            _rmtree(stage)
            raise MergeError(f"could not swap merged copy in ({exc}); restored") from exc
        _rmtree(old)
        item["backup"] = str(bdest)
        # From here the skill IS merged: a failure below must not be reported as "unchanged".
        try:
            # new base = the bundled copy we merged up to; then tell core the manifest is current
            self._save_snapshot(item["profile"], profile_home, name, item["theirs"], item["bundled_hash"])
            set_manifest_entry(profile_home / "skills" / ".bundled_manifest", name, item["bundled_hash"])
            state = read_state(profile_home)
            if name in state:
                del state[name]
                write_state(profile_home, state)
        except Exception as exc:
            raise BookkeepingError(f"merged, bookkeeping failed ({type(exc).__name__}: {exc}); "
                                   f"backup at {bdest}") from exc

    def apply_keep(self, item):
        """Kept skill: stop the nag without touching the user copy; preserve the real base."""
        profile_home, name = item["home"], item["name"]
        self.backup_profile_files(item["profile"], profile_home, name)
        state = read_state(profile_home)
        note = ""
        if not item["pending"]:
            base, src = self._base_for(profile_home, name, item["sdir"], item["origin"])
            if base is not None:
                self._save_snapshot(item["profile"], profile_home, name, base, item["origin"])
                state[name] = {"base_hash": item["origin"]}
                write_state(profile_home, state)
            else:
                note = f" (real base not found: {src}; unkeep would need a manual merge)"
        set_manifest_entry(profile_home / "skills" / ".bundled_manifest", name, item["bundled_hash"])
        return note

    def refresh_snapshot(self, item):
        """User-modified, upstream unchanged: the bundled copy IS the base.  Keep a copy so the next
        upstream change can be merged even when git history cannot supply the old tree."""
        snap = item["home"] / BASE_DIRNAME / item["name"]
        if snap.is_dir() and self.ss._dir_hash(snap) == item["origin"]:
            return False
        if self.dry:
            return True
        self._save_snapshot(item["profile"], item["home"], item["name"], tree_files(self.ss, item["sdir"]), item["origin"])
        return True

    # -- driver
    def run(self, profiles, only: set[str] | None = None):
        for profile, phome in profiles:
            for item in self.plan(profile, phome, only):
                name = item["name"]
                try:
                    self._handle(item)
                except BookkeepingError as exc:  # merged, but the base / manifest update failed
                    item.update(status="bookkeeping", why=str(exc))
                    self._say(profile, name, f"ERROR {exc}")
                except Exception as exc:  # one skill must never stop the rest
                    item.update(status="error", why=f"{type(exc).__name__}: {exc}")
                    self._say(profile, name, f"ERROR {item['why']}; unchanged. Look: {self._diff_cmd(profile, name)}")
                self.results.append(item)
            self._cleanup_tmp(phome)

    def _handle(self, item):
        profile, name = item["profile"], item["name"]
        if not item["changed"]:
            made = self.refresh_snapshot(item)
            item["status"] = "modified"
            if made:
                item["snapshot_saved"] = True
            return
        if item["kept"]:
            item["status"] = "kept"
            if item["origin"] == item["bundled_hash"]:
                item["quiet"] = True  # already re-baselined on an earlier run: nothing to do
                return
            if self.dry:
                self._say(profile, name, "kept (would re-baseline the update nag; your copy is never touched)")
            else:
                note = self.apply_keep(item)
                self._say(profile, name, "kept - update nag stopped, your copy untouched" + note)
            return
        self.evaluate(item)
        st = item["status"]
        if st == "no-base":
            self._say(profile, name, f"base version not found ({item['why']}); unchanged. Look: {self._diff_cmd(profile, name)}")
        elif st == "error":
            self._say(profile, name, f"ERROR {item['why']}; unchanged")
        elif st == "conflict":
            files = "; ".join(f"{p} ({w})" for p, w in item["conflicts"])
            self._say(profile, name, f"CONFLICT in {files}; unchanged. Look: {self._diff_cmd(profile, name)}")
        else:
            s = item["stats"]
            desc = (f"{len(s['merged'])} merged, {len(s['upstream'])} taken from upstream, "
                    f"{len(s['added'])} added, {len(s['deleted'])} deleted upstream, {s['mine']} yours kept")
            if self.dry:
                self._say(profile, name, f"would merge cleanly ({desc}; base from {item['base_src']})")
            else:
                self.apply_merge(item)
                item["status"] = "merged"
                self._say(profile, name, f"merged ({desc}; base from {item['base_src']}); backup {item['backup']}")

    def failed(self) -> bool:
        """True when a skill the run tried to change ended in an error (including a merge whose
        bookkeeping failed).  Conflicts and unknown bases are decisions for the user, not failures."""
        return any(r["status"] in ("error", "bookkeeping") for r in self.results)

    def summary(self) -> str:
        merged = [r for r in self.results if r["status"] in ("merged", "mergeable", "bookkeeping")]
        need = [r for r in self.results if r["status"] in ("conflict", "no-base", "error")]
        failed = [r for r in self.results if r["status"] in ("error", "bookkeeping")]
        kept = [r for r in self.results if r["status"] == "kept" and not r.get("quiet")]
        profs = sorted({r["profile"] for r in merged}, key=lambda p: (p != "default", p))
        verb = "would merge" if self.dry else "merged"
        if merged:
            text = (f"{verb} upstream changes into {len(merged)} edited skill{'s' if len(merged) != 1 else ''} "
                    f"({', '.join(profs)}); {len(need)} need a decision")
        else:
            text = f"no edited skill needed a merge; {len(need)} need a decision"
        if failed:
            text += f"; {len(failed)} failed (see above)"
        if kept:
            text += f"; {len(kept)} kept as-is"
        return ("dry run: " if self.dry else "") + "bundled skills: " + text


# --------------------------------------------------------------------------- commands

def cmd_merge(eng: Engine, profiles, only=None):
    """Exit code: 1 if a skill's merge failed (a warning on the card), else 0."""
    eng.run(profiles, only)
    return 1 if eng.failed() else 0


def cmd_list(eng: Engine, profiles):
    eng.dry = True  # never write from `list`
    keep = sorted(eng.keep)
    print("keep-mine list (" + str(config_path(eng.home)) + "): " + (", ".join(keep) if keep else "(empty)"))
    quiet = eng.out
    eng.out = lambda *_a, **_k: None
    try:
        for profile, phome in profiles:
            rows = []
            for item in eng.plan(profile, phome):
                name = item["name"]
                if item["kept"]:
                    upd = "; upstream has newer changes" if item["bundled_hash"] != item["want"] else ""
                    state = "modified - KEPT, never merged" + upd
                elif not item["changed"]:
                    state = "modified, no upstream change"
                else:
                    eng.evaluate(item)
                    st = item["status"]
                    if st == "mergeable":
                        s = item["stats"]
                        state = (f"modified, upstream changed - mergeable ({len(s['merged'])} merged, "
                                 f"{len(s['upstream'])} upstream, {len(s['added'])} added, {len(s['deleted'])} deleted)")
                    elif st == "conflict":
                        state = "modified, upstream changed - CONFLICT in " + ", ".join(p for p, _ in item["conflicts"])
                    elif st == "no-base":
                        state = f"modified, upstream changed - base not found ({item['why']})"
                    else:
                        state = f"modified, upstream changed - error ({item.get('why')})"
                rows.append((name, state))
            quiet(f"[{profile}] " + (f"{len(rows)} user-modified bundled skill(s)" if rows else "no user-modified bundled skills"))
            for name, state in rows:
                quiet(f"    {name:34s} {state}")
    finally:
        eng.out = quiet
    stale = [n for n in keep if n not in dict(eng.skills)]
    if stale:
        print("note: kept but no longer bundled: " + ", ".join(stale))
    return 0


def main(argv=None) -> int:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    ap = argparse.ArgumentParser(prog="bundled_skill_merge.py", description=__doc__.split("\n\n")[0])
    ap.add_argument("--home", type=Path, default=None,
                    help="Hermes home (default: $HERMES_SAFE_UPDATE_HOME, set for safe-update hooks)")
    ap.add_argument("--agent-dir", type=Path, default=None,
                    help="Hermes checkout (default: $HERMES_SAFE_UPDATE_CHECKOUT, else <home>/hermes-agent)")
    ap.add_argument("--bundled", type=Path, default=None, help="bundled skills dir (default: Hermes' own)")
    sub = ap.add_subparsers(dest="cmd", required=True)
    m = sub.add_parser("merge", help="merge upstream changes into edited bundled skills")
    m.add_argument("--dry-run", action="store_true")
    m.add_argument("--profile", action="append")
    k = sub.add_parser("keep", help="never merge this skill; stop its update nag")
    k.add_argument("name")
    u = sub.add_parser("unkeep", help="undo keep: the next run merges it again")
    u.add_argument("name")
    ls = sub.add_parser("list", help="show kept skills and per-profile state")
    ls.add_argument("--profile", action="append")
    args = ap.parse_args(argv)

    home_arg = args.home or (Path(os.environ["HERMES_SAFE_UPDATE_HOME"]) if os.environ.get("HERMES_SAFE_UPDATE_HOME") else None)
    if home_arg is None:
        print("bundled skills: skipped - no Hermes home (run it as a hermes-safe-update hook, "
              "or pass --home DIR)")
        return 1
    home = home_arg.resolve()
    agent_arg = args.agent_dir or (Path(os.environ["HERMES_SAFE_UPDATE_CHECKOUT"]) if os.environ.get("HERMES_SAFE_UPDATE_CHECKOUT") else None)
    agent_dir = (agent_arg or home / "hermes-agent").resolve()
    try:
        eng = Engine(home, agent_dir, args.bundled.resolve() if args.bundled else None,
                     dry=getattr(args, "dry_run", False))
    except CoreUnavailable as exc:
        print(f"bundled skills: skipped - {exc}")
        return 1
    except MergeError as exc:
        print(f"bundled skills: not merged - {exc}")
        return 1
    except Exception as exc:  # unexpected failure while loading: never break an update
        traceback.print_exc()
        print(f"bundled skills: skipped - cannot load Hermes core ({type(exc).__name__}: {exc})")
        return 1
    try:
        profiles = discover_profiles(home, getattr(args, "profile", None))
        if args.cmd == "merge":
            if not profiles:
                print("bundled skills: no profiles found")
                return 0
            rc = cmd_merge(eng, profiles)
            print(eng.summary())
            return rc
        if args.cmd == "list":
            return cmd_list(eng, profiles)
        # keep / unkeep
        names = set(eng.keep)
        if args.cmd == "keep":
            if args.name not in dict(eng.skills):
                print(f"'{args.name}' is not a bundled skill (see: hermes skills list-modified)")
                return 1
            names.add(args.name)
            eng.backup_before_edit(home, config_path(home))
            write_keep(home, names)
            eng.keep = names
            print(f"keep: '{args.name}' will never be merged (list: {config_path(home)})")
            eng.run(profiles, {args.name})
            return 1 if eng.failed() else 0
        if args.name not in names:
            print(f"unkeep: '{args.name}' was not in the keep list")
            return 0
        names.discard(args.name)
        eng.backup_before_edit(home, config_path(home))
        write_keep(home, names)
        print(f"unkeep: '{args.name}' removed; the next `merge` run merges upstream changes into it again "
              f"(preview: bundled_skill_merge.py merge --dry-run)")
        return 0
    except MergeError as exc:
        print(f"bundled skills: not merged - {exc}")
        return 1
    except Exception as exc:
        traceback.print_exc()
        print(f"bundled skills: crashed ({type(exc).__name__}: {exc}); see output above")
        return 1
    finally:
        eng.close()


if __name__ == "__main__":
    # `python script.py` puts this folder first on sys.path; stray modules here must not shadow the stdlib.
    _here = str(Path(__file__).resolve().parent)
    sys.path[:] = [p for p in sys.path if os.path.normcase(os.path.abspath(p or ".")) != os.path.normcase(_here)]
    raise SystemExit(main())
