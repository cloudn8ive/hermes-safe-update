"""Reclaim superseded Hermes dependency generations (Windows).

Embedded in hermes-safe-update and run with Hermes' managed Python (-I).

After an update Hermes runs its own collectors to delete old dependency
environments. uv hardlinks every generation's files to one NTFS file, and
Windows refuses to delete ANY name of a DLL/.pyd that a live process has
loaded, even through another generation's path. The first such file raises
WinError 5, Hermes logs "dependency generation cleanup skipped" and stops, so
no old generation is ever removed.

Renaming such a hardlink IS allowed while the image is loaded. This script
calls Hermes' own collectors (same lock, lease, selected-generation and age
rules) and replaces only the delete step: a file Windows refuses to delete is
moved into <home>/cache/safe-update-trash on the same volume. The trash holds
extra names for files the live generation still uses, so it costs almost no
disk; every run purges whatever is no longer locked.

  python -I safe_update_gc.py --home <hermes home> [--checkout <dir>] [--dry-run]

Exit code 0 = done (or nothing to do), 1 = cannot load Hermes / collector
error (never fatal for an update).
"""
from __future__ import annotations

import argparse
import contextlib
import os
import shutil
import stat
import sys
import time
from pathlib import Path

MIN_AGE_S = 86400  # Hermes' own default: generations younger than a day are kept

_ORIGINAL_RMTREE = shutil.rmtree


class Ctx:
    home: Path
    checkout: Path
    trash: Path


def hermes_imports(ctx: Ctx):
    os.environ.pop("HERMES_HOME", None)  # the default profile's install state
    if str(ctx.checkout) not in sys.path:
        sys.path.insert(0, str(ctx.checkout))
    from hermes_cli import runtime_state
    from pm import runtime
    from pm.environments import install_state_dir, selected_venv

    return runtime_state, runtime, install_state_dir, selected_venv


def unique_bytes(root: Path) -> int:
    """Bytes freed by deleting root: only files whose last link is inside it count."""
    total = 0
    for dirpath, _dirs, files in os.walk(root):
        for name in files:
            with contextlib.suppress(OSError):
                st = os.lstat(os.path.join(dirpath, name))
                if st.st_nlink <= 1:
                    total += st.st_size
    return total


def _call_rmtree(path, handle):
    """Run the real rmtree with an error handler (onexc on Python >= 3.12, else onerror)."""
    try:
        _ORIGINAL_RMTREE(path, onexc=handle)
    except TypeError:
        _ORIGINAL_RMTREE(path, onerror=lambda func, p, info: handle(func, p, info[1]))


def quarantine_rmtree(ctx: Ctx, report: list[str]):
    """An rmtree that moves undeletable (loaded) files aside instead of failing."""

    def rmtree(path, ignore_errors=False, onerror=None, *, onexc=None, dir_fd=None):
        root = Path(path)
        batch = ctx.trash / f"{root.name}-{int(time.time())}"
        moved = 0

        def handle(func, p, exc):
            nonlocal moved
            if isinstance(exc, FileNotFoundError):
                return
            if isinstance(exc, PermissionError) and func in (os.unlink, os.remove, os.rmdir):
                if func is not os.rmdir:
                    try:  # the read-only attribute is the other WinError 5 cause
                        os.chmod(p, stat.S_IWRITE)
                        os.unlink(p)
                        return
                    except OSError:
                        pass
                try:
                    rel = Path(p).relative_to(root)
                except ValueError:
                    rel = Path(Path(p).name)
                dest = batch / rel
                dest.parent.mkdir(parents=True, exist_ok=True)
                os.replace(p, dest)  # allowed for a loaded image; delete is not
                moved += 1
                return
            raise exc

        _call_rmtree(path, handle)
        report.append(f"removed {root.name}" + (f" ({moved} locked file(s) moved to trash)" if moved else ""))

    return rmtree


@contextlib.contextmanager
def patched_rmtree(fn):
    shutil.rmtree = fn
    try:
        yield
    finally:
        shutil.rmtree = _ORIGINAL_RMTREE


def purge_trash(ctx: Ctx) -> tuple[int, int]:
    """Delete trash batches; files still loaded stay for a later run. Returns (gone, left)."""
    if not ctx.trash.is_dir():
        return 0, 0
    gone = left = 0
    for batch in ctx.trash.iterdir():
        _call_rmtree(batch, lambda *a: None)
        if batch.exists():
            left += 1
        else:
            gone += 1
    with contextlib.suppress(OSError):
        ctx.trash.rmdir()  # only succeeds when empty
    return gone, left


def dry_run(ctx: Ctx, runtime_state, install_state_dir, selected_venv) -> int:
    """Read-only preview of what Hermes' collector would remove (no lock, no recovery)."""
    root = install_state_dir(ctx.checkout) / "environments"
    selected = selected_venv(ctx.checkout).parent.resolve()
    now = time.time()
    for gen in sorted(root.iterdir()) if root.is_dir() else []:
        if gen.is_symlink() or not gen.is_dir():
            continue
        marker = gen / ".lease-managed"
        if gen.resolve() == selected:
            verdict = "keep (selected)"
        elif not marker.is_file():
            verdict = "keep (not lease-managed)"
        elif now - marker.stat().st_mtime < MIN_AGE_S:
            verdict = f"keep (younger than 24 h: {int((now - marker.stat().st_mtime) / 3600)} h)"
        elif runtime_state.leases_held(gen):
            verdict = "keep (lease held by a running process)"
        else:
            verdict = f"REMOVE (~{unique_bytes(gen) / 1e6:.0f} MB unique)"
        print(f"  {gen.name}: {verdict}")
    waiting = len(list(ctx.trash.iterdir())) if ctx.trash.is_dir() else 0
    print(f"  trash batches waiting: {waiting}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--home", required=True, help="Hermes home (default profile)")
    ap.add_argument("--checkout", help="Hermes checkout (default <home>/hermes-agent)")
    ap.add_argument("--dry-run", action="store_true", help="show what would be removed; change nothing")
    a = ap.parse_args()
    ctx = Ctx()
    ctx.home = Path(a.home)
    ctx.checkout = Path(a.checkout) if a.checkout else ctx.home / "hermes-agent"
    ctx.trash = ctx.home / "cache" / "safe-update-trash"
    try:
        runtime_state, runtime, install_state_dir, selected_venv = hermes_imports(ctx)
    except Exception as exc:  # noqa: BLE001 - report, never crash the update
        print(f"generation cleanup: cannot load Hermes helpers ({exc}); skipped")
        return 1
    if a.dry_run:
        return dry_run(ctx, runtime_state, install_state_dir, selected_venv)

    gone, left = purge_trash(ctx)
    if gone or left:
        print(f"trash: purged {gone} batch(es), {left} still locked (kept for a later run)")
    free_before = shutil.disk_usage(str(ctx.home)).free
    report: list[str] = []
    try:
        with patched_rmtree(quarantine_rmtree(ctx, report)):
            removed = runtime_state.collect_generations(ctx.checkout)
            runtimes = runtime.collect_runtime_generations(install_state_dir(ctx.checkout) / "pm-runtime")
    except Exception as exc:  # noqa: BLE001
        for line in report:
            print(line)
        print(f"generation cleanup error: {type(exc).__name__}: {exc}")
        return 1
    for line in report:
        print(line)
    freed = (shutil.disk_usage(str(ctx.home)).free - free_before) / 1e6
    print(
        f"generation cleanup: {len(removed)} dependency generation(s), {len(runtimes)} PM runtime "
        f"generation(s) removed; disk free change {freed:+.0f} MB"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
