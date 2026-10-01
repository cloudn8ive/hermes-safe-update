package hermes

// GateProbeScript is run as `<managed python> -I -c <script> <checkout>`
// (python-behaviour §2.5). It is Python because it asks Hermes' own
// Windows update code which gateways it can map; there is no Go
// equivalent short of re-implementing Hermes. It prints one line:
// GateProbeTag + JSON. Dependencies are imported BEFORE the try block so a
// missing module can never read as a blocked gate. Read-only.
const GateProbeScript = `import json, os, sys
from pathlib import Path
ck = Path(sys.argv[1]); os.environ.pop("HERMES_HOME", None); sys.path.insert(0, str(ck))
try:
    import psutil, ruamel.yaml
except ImportError:
    from pm.environments import activate_dependencies
    activate_dependencies(ck)
    import psutil, ruamel.yaml
import hermes_cli.gateway as g
import hermes_cli.update_cmd_windows as w
res = {"ok": True}
try:
    procs, _svc, _svc_pids, running = w._discover_windows_gateways()
    res["gateways"] = sorted([str(p.profile), int(pid)] for pid, p in procs.items())
    res["running_pids"] = sorted(int(p) for p in running)
except Exception as exc:
    chain, e = [], exc
    while e is not None and len(chain) < 6:
        chain.append(f"{type(e).__name__}: {e}"); e = e.__cause__
    res = {"ok": False, "error": chain[0], "chain": chain}
print("` + GateProbeTag + `" + json.dumps(res), flush=True)
`
