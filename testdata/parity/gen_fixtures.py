"""Regenerate fixtures.json: the Python updater's answers for scripted inputs.

Parity check (Go vs the Python updater): this script imports the Python
updater's modules (hermes_safe_update.py, hermes_update_ui.py,
hermes_update_mirror.py) from a Hermes install, feeds each pure decision
function a fixed set of inputs and records the answers. The Go tests
(internal/**/parity_test.go) feed the same inputs to the Go code and compare.

    python gen_fixtures.py <dir with the three python scripts> [out.json]

Everything is synthetic; nothing is read from a live install except the
script sources. Nothing is written outside the output file.
"""
from __future__ import annotations

import base64
import contextlib
import io
import json
import os
import sys
import types
from pathlib import Path

SCRIPTS = Path(sys.argv[1])
OUT = Path(sys.argv[2]) if len(sys.argv) > 2 else Path(__file__).with_name("fixtures.json")

FAKE_LOCAL = r"C:\Users\you\AppData\Local"
os.environ["LOCALAPPDATA"] = FAKE_LOCAL
sys.path.insert(0, str(SCRIPTS))
import hermes_safe_update as su  # noqa: E402
import hermes_update_ui as ui  # noqa: E402
import hermes_update_mirror as mir  # noqa: E402

HOME = r"C:\Users\you\AppData\Local\hermes"
assert str(su.HOME) == HOME, su.HOME
out: dict = {"home": HOME}


def quiet_log():
    logs: list[str] = []
    su.log = lambda m: logs.append(m)
    return logs


# --------------------------------------------------------------- classify
D = HOME + r"\hermes-agent\apps\desktop\release\win-unpacked\Hermes.exe"
cls_cases = [
    ("desktop main", "Hermes.exe", f'"{D}"'),
    ("desktop renderer", "Hermes.exe", f'"{D}" --type=renderer --lang=en'),
    ("desktop gpu", "Hermes.exe", f'"{D}" --type=gpu-process'),
    ("electron name not hermes.exe", "electron.exe", f'"{D}"'),
    ("desktop name upper", "HERMES.EXE", f'"{D}"'),
    ("desktop arm64 dir", "Hermes.exe", f'"{HOME}\\hermes-agent\\apps\\desktop\\release\\win-arm64-unpacked\\Hermes.exe"'),
    ("kernel", "python.exe", r'python.exe C:\somewhere\hermes_kernel_runner.py --port 5'),
    ("kernel upper", "python.exe", r'PYTHON.EXE C:\X\HERMES_KERNEL_RUNNER.PY'),
    ("gateway", "hermes.exe", f'"{HOME}\\bin\\hermes.exe" gateway run --replace'),
    ("gateway upper path", "hermes.exe", f'"{HOME.upper()}\\BIN\\HERMES.EXE" GATEWAY RUN'),
    ("gateway elsewhere", "hermes.exe", r'"D:\other\hermes.exe" gateway run'),
    ("backend exe", "hermes.exe", f'"{HOME}\\bin\\hermes.exe" serve --port 4'),
    ("backend module", "python.exe", f'"{HOME}\\tools\\python\\python.exe" -m hermes_cli.main serve'),
    ("serve but not hermes", "node.exe", f'"{HOME}\\tools\\node\\node.exe" serve'),
    ("backend elsewhere", "hermes.exe", r'"D:\other\hermes.exe" serve'),
    ("hermes chat", "hermes.exe", f'"{HOME}\\bin\\hermes.exe" chat'),
    ("preserve not serve", "hermes.exe", f'"{HOME}\\bin\\hermes.exe" preserve'),
    ("gateway wins over serve", "hermes.exe", f'"{HOME}\\bin\\hermes.exe" gateway run serve'),
    ("empty cmd", "hermes.exe", ""),
    ("none cmd", "hermes.exe", None),
    ("none name", None, f'"{HOME}\\bin\\hermes.exe" gateway run'),
]
out["classify"] = [
    {"name": n, "proc": pn, "cmd": c, "want": su.classify({"name": pn, "cmd": c}) or ""}
    for n, pn, c in cls_cases
]

# --------------------------------------------------------------- busy sessions
# Python queries a real sqlite file; the rows are described here and built by
# the Go test. now/window fixed; expectation is the Python result computed
# from a temp DB built the same way.
import sqlite3  # noqa: E402
import tempfile  # noqa: E402
import time as _time  # noqa: E402

NOW = 1_790_000_000.0
rows_spec = [
    # id, title, last_activity_at (offset from now, None=NULL), ended (bool), source
    ("s-active", "Active session", -10, False, "cli"),
    ("s-edge", "Edge exactly at window", -180, False, "cli"),
    ("s-just-in", "Just inside", -179, False, "cli"),
    ("s-old", "Old", -4000, False, "cli"),
    ("s-ended", "Ended", -5, True, "cli"),
    ("s-cron", "Cron", -5, False, "cron"),
    ("s-nullsrc", "No source", -20, False, None),
    ("s-notitle", None, -30, False, "cli"),
    ("s-long", "T" * 80, -40, False, "cli"),
    ("s-null-last", "Null last activity", None, False, "cli"),
    ("s-future", "Future", 30, False, "cli"),
    ("s-unicode", "Café – naïve ✓ " + "é" * 60, -50, False, "cli"),
]
out["busy"] = {"now": NOW, "window_s": 180,
               "rows": [{"id": i, "title": t, "last": l, "ended": e, "source": s} for i, t, l, e, s in rows_spec]}
with tempfile.TemporaryDirectory(ignore_cleanup_errors=True) as td:
    homeA = Path(td) / "hermes"
    (homeA / "profiles" / "alpha").mkdir(parents=True)
    (homeA / "profiles" / "beta").mkdir(parents=True)
    (homeA / "profiles" / "empty").mkdir(parents=True)  # no state.db: ignored
    DDL = "create table sessions (id text primary key, title text, last_activity_at real, ended_at real, source text)"
    for h, subset in ((homeA, rows_spec), (homeA / "profiles" / "alpha", rows_spec[:3])):
        con = sqlite3.connect(h / "state.db")
        con.execute(DDL)
        for i, t, l, e, s in subset:
            con.execute("insert into sessions values (?,?,?,?,?)",
                        (i, t, None if l is None else NOW + l, NOW - 1 if e else None, s))
        con.commit()
        con.close()
    # beta: schema mismatch (no source column) -> unreadable = busy
    con = sqlite3.connect(homeA / "profiles" / "beta" / "state.db")
    con.execute("create table sessions (id text, title text)")
    con.commit()
    con.close()
    su.HOME = homeA
    real_time = _time.time
    su.time = types.SimpleNamespace(time=lambda: NOW, sleep=lambda s: None)
    res = su.busy_sessions(180)
    su.time = _time
    su.HOME = Path(HOME)
out["busy"]["want"] = [{"id": a, "title": b, "idle": c} for a, b, c in res]
# note: beta's error text is sqlite's and differs by driver; Go test checks the id only
for w in out["busy"]["want"]:
    if w["id"].endswith("state.db unreadable"):
        w["title"] = "*"

# --------------------------------------------------------------- update --check interpretation
def check_case(rc, text, drc=None, nrc=None):
    su.FACTS.clear()
    su.mirror = None
    logs = quiet_log()
    su.run = lambda cmd, timeout=300, env=None: (rc, text)
    calls = []

    def fake_git(*a, timeout=60):
        calls.append(a)
        return (drc if "uv.lock" in a else nrc), ""
    su.git = fake_git
    ret = su.check_for_updates()
    return {"rc": rc, "out": text, "drc": drc, "nrc": nrc,
            "want_return": ret, "want_behind": su.FACTS.get("behind"), "want_commits": su.FACTS.get("commits"),
            "want_npm": su.FACTS.get("npm"), "want_pydeps": su.FACTS.get("pydeps"),
            "want_source": su.FACTS.get("source"), "want_log0": logs[0], "want_logs": logs}

chk = []
for rc, text in [
    (0, "3 commits behind origin/main."), (0, "1 commit behind origin/main"), (0, "Already up to date."),
    (0, "Update available: 12 commits behind"), (0, "behind but up to date?"), (0, "Hermes is up to date, nothing behind"),
    (0, "updates available"), (0, ""), (0, "something unrelated"),
    (1, "3 commits behind origin/main"), (124, "timed out after 300s"), (127, "not found"),
    (0, "BEHIND by 2"), (0, "  \n\n 5 commits behind  \n   x\n"),
]:
    for drc, nrc in ([(0, 0), (1, 0), (0, 1), (124, 2), (128, 1)] if (rc == 0 and "behind" in text.lower()
                                                                     and "up to date" not in text.lower()) else [(None, None)]):
        chk.append(check_case(rc, text, drc, nrc))
out["update_check"] = chk

# --------------------------------------------------------------- changed kinds
kinds = []
for files, complete in [
    ([], True), ([], False), (["README.md"], True), (["README.md"], False),
    (["package.json"], True), (["apps/desktop/package.json"], False), (["package-lock.json"], False),
    (["uv.lock"], False), (["pyproject.toml"], True), (["sub/pyproject.toml"], True), (["sub/uv.lock"], True),
    (["apps/desktop/package-lock.json", "uv.lock"], False), (["my-package.json"], True), (["package.json.bak"], True),
    (["xpackage.json"], True), (["a\\package.json"], True),
]:
    n, p = su._changed_kinds(files, complete)
    kinds.append({"files": files, "complete": complete, "npm": n, "pydeps": p})
out["kinds"] = kinds

# --------------------------------------------------------------- verify (RESULT decision)
ver = []
HEAD = "4e7403130ee278bd99c450fcd9f73c6b32135c95"
for rc in (0, 1, 2, 124):
    for text in ("", "Fleet version check returned no rows. verification incomplete", "boom"):
        for sha in (HEAD, HEAD[:10], "", "deadbeef" * 5):
            for alive in (True, False):
                for vrc in (0, 1):
                    su.FACTS.clear()
                    logs = quiet_log()
                    su.stage = lambda k: None
                    su.git = lambda *a, timeout=60: (0, HEAD + "\n")
                    su.gateway_pids = lambda: []
                    su.gateway_state = lambda sha=sha: {"code_sha": sha, "pid": 4321}
                    su.pid_alive = lambda pid, alive=alive: alive

                    def fake_run(cmd, timeout=300, env=None, vrc=vrc):
                        return (0, "") if "gateway" in cmd else (vrc, "")
                    su.run = fake_run
                    su.time = types.SimpleNamespace(time=real_time, sleep=lambda s: None)
                    ok = su.verify(rc, text)
                    su.time = _time
                    ver.append({"rc": rc, "out": text, "head": HEAD, "sha": sha, "alive": alive, "vrc": vrc,
                                "want_ok": ok, "want_line": logs[-1].split(" Full log:")[0]})
out["verify"] = ver

# --------------------------------------------------------------- gate probe parse
def gate_case(output, rc=0):
    su.run = lambda cmd, timeout=300, env=None: (rc, output)
    res, info = su.gateway_gate()
    return {"out": output, "rc": rc, "want_gate": res, "want_info": info}

TAG = su.PROBE_TAG
gate = [
    gate_case(f'noise\n{TAG}{{"ok": true, "gateways": [["default", 8756]], "running_pids": [8756]}}\ntrailer'),
    gate_case(f'{TAG}{{"ok": true, "gateways": [["default", 1], ["work", 2]], "running_pids": [1, 2]}}'),
    gate_case(f'{TAG}{{"ok": true, "gateways": [], "running_pids": []}}'),
    gate_case(f'{TAG}{{"ok": true}}'),
    gate_case(f'{TAG}{{"ok": false, "error": "RuntimeError: Could not map Windows gateway PIDs to profiles", "chain": ["RuntimeError: Could not map Windows gateway PIDs to profiles", "RuntimeError: active gateway lock has no PID metadata"]}}'),
    gate_case(f'{TAG}{{"ok": false, "error": "RuntimeError: Could not determine Windows gateway service ownership", "chain": ["RuntimeError: Could not determine Windows gateway service ownership"]}}'),
    gate_case(f'{TAG}{{"ok": false, "error": "RuntimeError: Could not prepare Windows gateway pause for update", "chain": ["RuntimeError: Could not prepare Windows gateway pause for update", "OSError: boom: with colon"]}}'),
    gate_case(f'{TAG}{{"ok": false, "error": "ValueError: odd", "chain": ["ValueError: odd"]}}'),
    gate_case(f'{TAG}{{"ok": false, "error": "ValueError: odd"}}'),
    gate_case(f'{TAG}{{"ok": false, "error": "RuntimeError: Could not discover Windows gateway PIDs before update"}}'),
    gate_case(f'{TAG}{{"ok": false, "chain": ["RuntimeError: Could not map Windows gateway PIDs to profiles", "' + "x" * 400 + '"]}}'),
    gate_case(f'{TAG}{{"ok": false, "chain": ["Other: ' + "y" * 400 + '"]}}'),
    gate_case(f"{TAG}not json"),
    gate_case("Traceback (most recent call last):\n  File x\nImportError: no module named psutil", 1),
    gate_case("", 3),
    gate_case("z" * 500, 1),
    gate_case(f'first\n{TAG}{{"ok": true, "gateways": [["a", 1]]}}\n{TAG}{{"ok": false, "error": "x"}}'),
]
out["gate"] = gate

# --------------------------------------------------------------- hermes update output: triggers, display, regexes
out["triggers_table"] = [[a, b] for a, b in su.UPDATE_TRIGGERS]
trig_lines = [
    "Stopping Windows gateway(s)...", "→ Fetching updates", "→ Pulling updates", "→ Fetching updates → Pulling updates",
    "Installing Python dependencies…", "Installing Python dependencies...", "Updating Python dependencies",
    "Preparing Node dependencies…", "Building the TUI…", "Building desktop packaged app…", "Packaging the desktop app…",
    "Code updated!", "Update complete! (v0.21.5 → v0.21.6)", "Code updated! Update complete!", "  nothing relevant",
    "", "fetching updates", "Stopping Windows gateway; Code updated!", "Fetching updates",
]
out["triggers"] = []
for line in trig_lines:
    hit = ""
    for needle, key in su.UPDATE_TRIGGERS:
        if needle in line:
            hit = key
            break
    out["triggers"].append({"line": line, "want": hit})

disp_lines = [
    "→ cua-driver refresh deferred: run hermes computer-use install --upgrade",
    "  Run: hermes computer-use install --upgrade", "plain line", "", "Tip: You can now select a provider and model",
    "hermes model              # Select provider and model", "cua-driver refresh deferred", "computer-use install --upgrade",
]
out["display"] = [{"line": l, "want": su.display_line(l), "hidden_tips": False} for l in disp_lines]
# Python's display_line has no tip filter (the UI module's _HIDE does); record that too
out["ui_hide"] = list(ui._HIDE)

m = su.PAUSE_GATE_RE
upd_out = [
    "RuntimeError: Could not map Windows gateway PIDs to profiles: lock has no PID",
    "x\nRuntimeError: Could not prepare Windows gateway pause for update\nmore",
    "RuntimeError: Something else", "Could not map Windows gateway PIDs to profiles",
    "RuntimeError: Could not discover Windows gateway PIDs before update: a: b",
]
out["pause_re"] = [{"out": o, "want": (m.search(o).group(1) if m.search(o) else None)} for o in upd_out]
ver_out = ["Update complete! (v0.21.5 → v0.21.6)", "Update complete! (a → b) Update complete! (c → d)", "Update complete!",
           "Update complete! (1.0.0+5.gabc (2026.9.24) → 1.0.1)"]
import re  # noqa: E402
out["versions_re"] = [{"out": o, "want": (list(mm.groups()) if (mm := re.search(r"Update complete! \((.+?) → (.+?)\)", o)) else None)}
                      for o in ver_out]
cm = ["3 commits behind", "1 commit behind origin", "12 Commits behind", "no count", "x 7 commits behind y 9 commits behind"]
out["commits_re"] = [{"out": o, "want": (int(mm.group(1)) if (mm := re.search(r"(\d+) commits? behind", o)) else None)} for o in cm]
out["nothing_changed"] = []
for head_now, start, text in [(HEAD, HEAD, ""), (HEAD, HEAD, "→ Fetching updates"), ("other", HEAD, ""),
                              (HEAD, "", ""), (HEAD, HEAD[:39], ""), ("  " + HEAD + "\n", HEAD, "stopped")]:
    su.FACTS.clear()
    su.FACTS["head_full"] = start or None
    su.git = lambda *a, timeout=60, h=head_now: (0, h)
    out["nothing_changed"].append({"head_now": head_now, "start": start, "out": text, "want": bool(su.nothing_changed(text))})

# --------------------------------------------------------------- number formats
mins = [0, 1, 29, 30, 31, 59, 60, 89, 90, 91, 150, 151, 210, 300, 449, 450, 451, 629.9, 630]
out["est_minutes"] = [{"s": s, "want": su.est_minutes(s)} for s in mins]
durs = [0, 0.4, 0.5, 1.5, 2.5, 3.5, 59.4, 59.5, 60, 61, 125.5, 600, 3599.6, 3600, 3661, 7322]
out["fmt_duration"] = [{"s": s, "want": su.fmt_duration(s)} for s in durs]
out["fmt_dur"] = [{"s": s, "want": ui.fmt_dur(s)} for s in durs + [-5]]
lefts = [0, 10, 44.9, 45, 59, 60, 89.9, 90, 91, 150, 151, 449, 450, 3600]
out["fmt_left"] = [{"s": s, "want": ui.fmt_left(s)} for s in lefts]

# --------------------------------------------------------------- mirror age text
real_t = mir.time.time
mir.time.time = lambda: 1_790_000_000.0
ages = [None, 0, 1, 29, 30, 59, 60, 61, 3599, 7199, 7200, 7259, 7260, 86400, 90000]
out["age_text"] = [{"ago_s": a, "want": mir.age_text(None if a is None else 1_790_000_000.0 - a)} for a in ages]
mir.time.time = real_t

# --------------------------------------------------------------- cua parsing
cua_status = [
    "cua-driver: installed at C:\\hermes\\tools\\cua-driver-0.21.0-win32-x64\\cua-driver.exe (0.21.0)\r\n  second line\r\n",
    "cua-driver installed at C:\\hermes\\tools\\cua-driver-0.21.0-win32-x64\\cua-driver.exe\n",
    "CUA-DRIVER: INSTALLED AT D:\\X Y\\CUA-DRIVER.EXE (1.0)\n",
    "cua-driver: not installed\n", "", "installed at C:\\a\\cua-driver.exe and C:\\b\\cua-driver.exe\n",
    "line1\ninstalled at C:\\q\\cua-driver.exe  \n",
]
out["cua_status"] = []
for text in cua_status:
    su.run = lambda cmd, timeout=300, env=None, t=text: (0, t)
    out["cua_status"].append({"out": text, "want": su.cua_binary() or ""})

xml_new = ('<?xml version="1.0" encoding="UTF-16"?><Task><Actions><Exec><Command>powershell.exe</Command>'
           '<Arguments>-NoProfile -Command "Start-Process -FilePath \'C:\\hermes\\tools\\cua-driver-0.21.0-win32-x64\\cua-driver.exe\' -ArgumentList serve"</Arguments></Exec></Actions></Task>')
xml_direct = '<Task><Actions><Exec><Command>C:\\Tools\\CUA-Driver.EXE</Command></Exec></Actions></Task>'
xml_none = '<Task><Actions><Exec><Command>powershell.exe</Command><Arguments>-File x.ps1</Arguments></Exec></Actions></Task>'
xml_quoted = '<Task><Exec><Command>"C:\\Program Files\\cua-driver\\cua-driver.exe"</Command></Exec></Task>'
cases = [("utf16 bom", b"\xff\xfe" + xml_new.encode("utf-16-le")), ("utf16 nobom", xml_new.encode("utf-16-le")),
         ("utf8", xml_direct.encode()), ("none", xml_none.encode()), ("utf16 none", b"\xff\xfe" + xml_none.encode("utf-16-le")),
         ("quoted spaces", xml_quoted.encode()), ("empty", b"")]
out["cua_task"] = []
for name, data in cases:
    su.subprocess = types.SimpleNamespace(run=lambda *a, d=data, **k: types.SimpleNamespace(returncode=0, stdout=d),
                                          SubprocessError=Exception, DEVNULL=-3)
    out["cua_task"].append({"name": name, "b64": base64.b64encode(data).decode(), "want": su.cua_task_binary()})
import subprocess as _sp  # noqa: E402
su.subprocess = _sp
sp = [("C:\\a\\b.exe", "c:\\A\\B.exe"), ("C:\\a\\b.exe", "C:/a/b.exe"), ("C:\\a\\.\\b.exe", "C:\\a\\b.exe"),
      ("", "C:\\a"), ("C:\\a", ""), (None, "C:\\a"), ("C:\\a\\", "C:\\a"), ("C:\\a\\x\\..\\b.exe", "C:\\a\\b.exe")]
out["same_path"] = [{"a": a or "", "b": b or "", "want": su.same_path(a, b)} for a, b in sp]

# --------------------------------------------------------------- estimator
def hist():
    ok = True

    def rec(started, kind, steps, cond=None, ok=True, **extra):
        r = {"started": started, "kind": kind, "ok": ok, "total": sum(steps.values()), "steps": steps, **extra}
        if cond:
            r["cond"] = cond
        return r
    full = {"local": 1, "remote": 70, "procs": 3, "close": 9, "gateway_stop": 20, "fetch": 12, "pull": 95, "pydeps": 60,
            "frontend": 24, "desktop": 200, "package": 20, "finalize": 14, "restart": 28, "verify": 5, "relaunch": 1,
            "cleanup": 3, "cua": 6}
    h = []
    for i in range(8):
        s = {k: v + i for k, v in full.items()}
        h.append(rec(f"2026-09-{20+i:02d}T10:00:00", "update", s))
    h.append(rec("2026-09-29T10:00:00", "update", {k: v * 0.4 for k, v in full.items()},
                 cond={"source": "mirror", "mirror": True, "npm": False, "pydeps": False, "commits": 5}))
    h.append(rec("2026-09-29T11:00:00", "update", {k: v * 0.35 for k, v in full.items()},
                 cond={"source": "mirror", "mirror": True, "npm": False, "pydeps": False, "commits": 6}))
    h.append(rec("2026-09-29T12:00:00", "update", {"local": 1, "remote": 3, "procs": 1}, ok=True))  # nothing to update
    h.append(rec("2026-09-29T13:00:00", "update", {"local": 1, "remote": 5, "procs": 1, "close": 7, "gateway_stop": 20, "fetch": 400},
                 ok=False))
    for i in range(6):
        h.append(rec(f"2026-09-3{i%10}T09:00:00" if i < 1 else f"2026-10-0{i}T09:00:00", "check",
                     {"local": 1, "remote": 4 + i, "procs": 0, "sessions": 0.1, "gc_preview": 2.5, "cua_status": 4.5},
                     cond={"source": "mirror", "mirror": True}))
    return h


H = hist()
Hp = Path(tempfile.mkdtemp()) / "t.json"
Hp.write_text(json.dumps(H), encoding="utf-8")
scen = [
    ("update, no conditions", "update", {}),
    ("update, mirror, pydeps unchanged", "update", {"source": "mirror", "mirror": True, "npm": False, "pydeps": False, "commits": 81}),
    ("update, git, npm changed", "update", {"source": "git", "mirror": False, "npm": True, "pydeps": True, "commits": 9}),
    ("update, api", "update", {"source": "api", "mirror": False, "npm": False, "pydeps": False}),
    ("check, mirror", "check", {"source": "mirror", "mirror": True}),
]
est = []
for name, kind, cond in scen:
    steps = su.UPDATE_STEPS if kind == "update" else su.CHECK_STEPS
    u = ui.UpdateUI("", steps, Hp, plain=True, conditions=su.STEP_CONDITIONS)
    u.set_conditions(**cond)
    if "pydeps" in cond:
        u.expect_skip("pydeps", cond["pydeps"] is False)
    est.append({"name": name, "kind": kind, "cond": cond,
                "est": {s["key"]: round(s["est"], 6) for s in u.steps},
                "planned_close": round(u.planned_total("close"), 6), "planned_all": round(u.planned_total(), 6),
                "minutes_text": su.est_minutes(u.planned_total("close"))})
out["estimator"] = {"history": H, "scenarios": est,
                    "python_steps": {"update": [list(s) for s in su.UPDATE_STEPS], "check": [list(s) for s in su.CHECK_STEPS]}}

# overrun / class factor / fraction: drive a UI with a fake clock
class Clk:
    t = 1_790_000_000.0

ui.time = types.SimpleNamespace(time=lambda: Clk.t, sleep=lambda s: None, strftime=_time.strftime, localtime=_time.localtime)
u = ui.UpdateUI("", su.UPDATE_STEPS, Hp, plain=True, conditions=su.STEP_CONDITIONS)
u.set_conditions(source="mirror", mirror=True, npm=False, pydeps=False, commits=81)
u.expect_skip("pydeps", True)
tl = []
def snap(label):
    tl.append({"label": label, "remaining": round(u.remaining(), 6), "fraction": round(u.fraction(), 6),
               "planned_close": round(u.planned_total("close"), 6)})
snap("start")
script = [("local", 1), ("remote", 12), ("procs", 2), ("wait", 5), ("close", 30), ("gateway_stop", 40), ("fetch", 30),
          ("pull", 400), ("frontend", 90)]
events = []
for key, dur in script:
    Clk.t += 0.0
    u.stage(key)
    events.append({"stage": key})
    snap(f"after stage {key}")
    for frac in (0.5, 1.5, 3.0):
        pass
    Clk.t += dur
    events.append({"advance": dur})
    snap(f"{key} +{dur}s")
u.complete(True)
snap("complete")
ui.time = _time
out["estimator"]["timeline"] = {"events": [
    e for key, dur in script for e in ({"stage": key}, {"advance": dur})], "snaps": tl,
    "start_unix": 1_790_000_000.0}

# --------------------------------------------------------------- optional: a real machine's history
# HSU_REAL_HISTORY=<path to safe-update-timings.json> adds "estimator_real": the same scenarios over that
# history. Write such output ONLY to a scratch file (never commit it) and run the Go tests with
# HSU_PARITY_FIXTURES=<that file>.
real = os.environ.get("HSU_REAL_HISTORY")
if real:
    if os.environ.get("HSU_NO_LEGACY"):
        # Python back-fills conditions of old records (KNOWN_COND, LEGACY_COND); the Go port
        # deliberately does not (python-behaviour 5.10). Switching them off shows that this is the
        # only source of difference on a real history.
        ui.KNOWN_COND.clear()
        ui.LEGACY_COND.clear()
    RH = json.loads(Path(real).read_text(encoding="utf-8"))
    est2 = []
    for name, kind, cond in scen:
        steps = su.UPDATE_STEPS if kind == "update" else su.CHECK_STEPS
        u = ui.UpdateUI("", steps, Path(real), plain=True, conditions=su.STEP_CONDITIONS)
        u.set_conditions(**cond)
        if "pydeps" in cond:
            u.expect_skip("pydeps", cond["pydeps"] is False)
        est2.append({"name": name, "kind": kind, "cond": cond,
                     "est": {s["key"]: round(s["est"], 6) for s in u.steps},
                     "planned_close": round(u.planned_total("close"), 6), "planned_all": round(u.planned_total(), 6),
                     "minutes_text": su.est_minutes(u.planned_total("close"))})
    out["estimator_real"] = {"history": RH, "scenarios": est2,
                             "python_steps": {"update": [list(s) for s in su.UPDATE_STEPS],
                                              "check": [list(s) for s in su.CHECK_STEPS]}}

OUT.write_text(json.dumps(out, indent=1, ensure_ascii=False), encoding="utf-8")
print("wrote", OUT, {k: (len(v) if hasattr(v, "__len__") else v) for k, v in out.items()})
