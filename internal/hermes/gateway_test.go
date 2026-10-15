package hermes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

func TestGatewayState(t *testing.T) {
	cases := []struct {
		name, content string
		missing       bool
		want          GatewayState
	}{
		{"valid", `{"code_sha":"abc123","pid":4242,"extra":1}`, false, GatewayState{CodeSHA: "abc123", PID: 4242}},
		{"missing file", "", true, GatewayState{}},
		{"invalid json", "{nope", false, GatewayState{}},
		{"array", "[1,2]", false, GatewayState{}},
		{"wrong types", `{"code_sha":5,"pid":"x"}`, false, GatewayState{}},
		{"empty", "", false, GatewayState{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if !c.missing {
				if err := os.WriteFile(filepath.Join(home, GatewayStateFile), []byte(c.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			g := &CLIGateway{Home: home}
			if got := g.State(); got != c.want {
				t.Errorf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func probeLine(res string) string { return "noise\n" + GateProbeTag + res + "\ntrailer\n" }

func TestParseProbe(t *testing.T) {
	chain := func(c ...string) string {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": c[0], "chain": c})
		return string(b)
	}
	cases := []struct {
		name string
		out  string
		code int
		want GateResult
		info string // substring
	}{
		{"ok with gateways", probeLine(`{"ok": true, "gateways": [["default", 111], ["work", 222]], "running_pids": [111, 222]}`), 0, GateOK, "default (pid 111), work (pid 222)"},
		{"ok none running", probeLine(`{"ok": true, "gateways": [], "running_pids": []}`), 0, GateOK, "none running"},
		{"blocked: map pids", probeLine(chain("RuntimeError: Could not map Windows gateway PIDs to profiles: active gateway lock has no PID metadata", "RuntimeError: active gateway lock has no PID metadata")), 0, GateBlocked, "active gateway lock has no PID metadata"},
		{"blocked: service ownership", probeLine(chain("RuntimeError: Could not determine Windows gateway service ownership: x")), 0, GateBlocked, "x"},
		{"blocked: discover pids", probeLine(chain("RuntimeError: Could not discover Windows gateway PIDs before update: y")), 0, GateBlocked, "y"},
		{"blocked: prepare pause", probeLine(chain("Could not prepare Windows gateway pause for update: z")), 0, GateBlocked, "z"},
		{"other error is unknown", probeLine(chain("ValueError: boom", "OSError: disk")), 0, GateUnknown, "probe error: OSError: disk"},
		{"no tag line", "Traceback (most recent call last):\nImportError: no module", 1, GateUnknown, "probe did not run (rc=1)"},
		{"bad json", probeLine("{not json"), 0, GateUnknown, "probe output unreadable"},
		{"empty chain falls back to error", probeLine(`{"ok": false, "error": "RuntimeError: Could not map Windows gateway PIDs to profiles: q"}`), 0, GateBlocked, "q"},
		{"timeout", "timed out after 3m0s", 124, GateUnknown, "probe did not run (rc=124)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, info := ParseProbe(c.out, c.code)
			if got != c.want || !strings.Contains(info, c.info) {
				t.Errorf("got %v %q want %v containing %q", got, info, c.want, c.info)
			}
		})
	}
}

func TestProbeBlockedInfoTruncatedTo200(t *testing.T) {
	long := strings.Repeat("e", 500)
	line := probeLine(`{"ok": false, "chain": ["RuntimeError: Could not map Windows gateway PIDs to profiles: ` + long + `"]}`)
	_, info := ParseProbe(line, 0)
	if len(info) != 200 {
		t.Errorf("len = %d", len(info))
	}
}

func newGW(t *testing.T) (*CLIGateway, *execx.Fake, *platform.FakeProcs, string) {
	t.Helper()
	home := t.TempDir()
	run := &execx.Fake{}
	procs := platform.NewFakeProcs()
	clk := testutil.NewClock(time.Unix(1_790_000_000, 0))
	g := &CLIGateway{
		Home: home, Launcher: `L:\hermes.exe`, Python: `P:\python.exe`, Checkout: `C:\ck`,
		Run: run, Procs: procs, GOOS: "windows",
		Sleep: func(d time.Duration) { clk.Advance(d) },
	}
	return g, run, procs, home
}

func TestProbeRunsManagedPythonIsolated(t *testing.T) {
	g, run, _, _ := newGW(t)
	run.OnPrefix([]string{`P:\python.exe`}, execx.Result{Output: probeLine(`{"ok": true, "gateways": [], "running_pids": []}`)})
	res, _ := g.Probe(context.Background())
	if res != GateOK {
		t.Fatalf("res = %v", res)
	}
	c := run.Calls()[0]
	if len(c.Argv) != 5 || c.Argv[1] != "-I" || c.Argv[2] != "-c" || c.Argv[3] != GateProbeScript || c.Argv[4] != `C:\ck` {
		t.Errorf("argv = %q", c.Argv)
	}
	if c.Timeout != 180*time.Second {
		t.Errorf("timeout = %v", c.Timeout)
	}
}

func TestProbeWithoutManagedPythonIsUnknown(t *testing.T) {
	g, run, _, _ := newGW(t)
	g.Python = ""
	res, info := g.Probe(context.Background())
	if res != GateUnknown || !strings.Contains(info, "managed Python not found") {
		t.Errorf("got %v %q", res, info)
	}
	if len(run.Calls()) != 0 {
		t.Error("must not run anything (never fall back to system Python)")
	}
}

func TestProbeNotWindowsIsUnknownNotNeeded(t *testing.T) {
	g, run, _, _ := newGW(t)
	g.GOOS = "linux"
	res, info := g.Probe(context.Background())
	if res != GateNotApplicable || !strings.Contains(info, "not needed") || len(run.Calls()) != 0 {
		t.Errorf("got %v %q calls=%d", res, info, len(run.Calls()))
	}
}

func gw(pid int) platform.Process {
	return platform.Process{PID: pid, Name: "hermes.exe", Cmdline: `C:\H\bin\hermes.exe gateway run`}
}

func TestStopNoGatewayRunning(t *testing.T) {
	g, run, procs, home := newGW(t)
	g.Home = `C:\H`
	_ = home
	_ = procs
	if err := g.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(run.Calls()) != 0 {
		t.Errorf("calls = %v", run.Calls())
	}
}

func TestStopFirstCommandWorks(t *testing.T) {
	g, run, procs, _ := newGW(t)
	g.Home = `C:\H`
	procs.Add(gw(10))
	run.OnFunc([]string{`L:\hermes.exe`, "gateway", "stop"}, func(execx.Cmd) execx.Result {
		procs.Remove(10)
		return execx.Result{}
	})
	if err := g.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(run.Calls()) != 1 || len(procs.Killed()) != 0 {
		t.Errorf("calls=%v killed=%v", run.Calls(), procs.Killed())
	}
}

func TestStopFallsBackToAllThenForces(t *testing.T) {
	g, run, procs, _ := newGW(t)
	g.Home = `C:\H`
	procs.Add(gw(10))
	procs.Add(gw(11))
	run.OnPrefix([]string{`L:\hermes.exe`, "gateway", "stop"}, execx.Result{Code: 0})
	if err := g.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := run.Calls()
	if len(calls) != 2 || calls[1].Argv[len(calls[1].Argv)-1] != "--all" {
		t.Fatalf("calls = %v", calls)
	}
	if k := procs.Killed(); len(k) != 2 {
		t.Errorf("killed = %v", k)
	}
	if calls[0].Timeout != 180*time.Second {
		t.Errorf("timeout = %v", calls[0].Timeout)
	}
}

func TestStopFailsWhenProcessSurvivesKill(t *testing.T) {
	g, run, procs, _ := newGW(t)
	g.Home = `C:\H`
	procs.Add(gw(10))
	run.OnPrefix([]string{`L:\hermes.exe`}, execx.Result{})
	g.Procs = &stuckProcs{FakeProcs: procs}
	err := g.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not be stopped") {
		t.Fatalf("err = %v", err)
	}
}

// stuckProcs ignores KillTree.
type stuckProcs struct{ *platform.FakeProcs }

func (s *stuckProcs) KillTree(context.Context, platform.Process) error { return nil }

func TestStartWhenNoGateway(t *testing.T) {
	g, run, _, _ := newGW(t)
	g.Home = `C:\H`
	run.OnPrefix([]string{`L:\hermes.exe`, "gateway", "start", "--all"}, execx.Result{})
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run.Calls()[0].Timeout != 300*time.Second {
		t.Errorf("timeout = %v", run.Calls()[0].Timeout)
	}
}

func TestStartSkippedWhenAlreadyRunning(t *testing.T) {
	g, run, procs, _ := newGW(t)
	g.Home = `C:\H`
	procs.Add(gw(10))
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(run.Calls()) != 0 {
		t.Errorf("calls = %v", run.Calls())
	}
}

func TestStartReportsFailure(t *testing.T) {
	g, run, _, _ := newGW(t)
	g.Home = `C:\H`
	run.OnPrefix([]string{`L:\hermes.exe`}, execx.Result{Code: 1, Output: "no service"})
	if err := g.Start(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestGatewayPIDsUsesClassify(t *testing.T) {
	g, _, procs, _ := newGW(t)
	g.Home = `C:\H`
	procs.Add(gw(10))
	procs.Add(platform.Process{PID: 20, Name: "hermes.exe", Cmdline: `C:\H\bin\hermes.exe serve`})
	pids, err := g.PIDs(context.Background())
	if err != nil || len(pids) != 1 || pids[0] != 10 {
		t.Fatalf("pids = %v, %v", pids, err)
	}
}

// On macOS/Linux `--all` would kill every gateway of every profile first.
func TestStartNeverUsesAllOnPOSIX(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			g, run, _, _ := newGW(t)
			g.Home, g.GOOS = `/h`, goos
			run.OnPrefix([]string{`L:\hermes.exe`, "gateway", "start"}, execx.Result{})
			if err := g.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls := run.Calls()
			if len(calls) != 1 || strings.Join(calls[0].Argv, " ") != `L:\hermes.exe gateway start` {
				t.Errorf("calls = %v", calls)
			}
		})
	}
}

// An update can replace the managed Python; the probe after it runs the
// interpreter looked up now, not the one found before the update. A lookup
// that finds nothing keeps the earlier value.
func TestProbeLooksUpPythonAtProbeTime(t *testing.T) {
	g, run, _, _ := newGW(t)
	ok := execx.Result{Output: probeLine(`{"ok": true, "gateways": [], "running_pids": []}`)}
	run.OnPrefix([]string{`N:\python.exe`}, ok)
	run.OnPrefix([]string{`P:\python.exe`}, ok)
	g.PythonNow = func() string { return `N:\python.exe` }
	if res, _ := g.Probe(context.Background()); res != GateOK {
		t.Fatalf("res = %v", res)
	}
	g.PythonNow = func() string { return "" }
	if res, _ := g.Probe(context.Background()); res != GateOK {
		t.Fatalf("res = %v", res)
	}
	c := run.Calls()
	if len(c) != 2 || c[0].Argv[0] != `N:\python.exe` || c[1].Argv[0] != `P:\python.exe` {
		t.Errorf("interpreters used: %v", c)
	}
}

// ---- StopStale: the restart of one gateway that runs old code ----

// gwOf is a gateway process of this gateway's home (no fixed path literal, so
// the test is the same on every OS).
func gwOf(g *CLIGateway, pid int) platform.Process {
	return platform.Process{PID: pid, Name: "hermes.exe", Cmdline: filepath.Join(g.Home, "bin", "hermes.exe") + " gateway run"}
}

func sawArg(calls []execx.Cmd, arg string) bool {
	for _, c := range calls {
		for _, a := range c.Argv {
			if a == arg {
				return true
			}
		}
	}
	return false
}

func TestStopStaleStopsOnlyTheNamedGatewayAfterPlainStop(t *testing.T) {
	g, run, procs, _ := newGW(t)
	procs.Add(gwOf(g, 10))
	procs.Add(gwOf(g, 11))                                                // another profile's gateway
	run.OnPrefix([]string{g.Launcher, "gateway", "stop"}, execx.Result{}) // the plain stop does not end it
	if err := g.StopStale(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if sawArg(run.Calls(), "--all") {
		t.Errorf("ran `gateway stop --all`: %v", run.Calls())
	}
	if len(run.Calls()) != 1 || strings.Join(run.Calls()[0].Argv[1:], " ") != "gateway stop" {
		t.Errorf("calls = %v", run.Calls())
	}
	if k := procs.Killed(); len(k) != 1 || k[0] != 10 {
		t.Errorf("killed = %v, want only the stale pid 10", k)
	}
	if pids, _ := g.PIDs(context.Background()); len(pids) != 1 || pids[0] != 11 {
		t.Errorf("the other gateway was touched: %v", pids)
	}
}

func TestStopStalePlainStopThatWorksKillsNothing(t *testing.T) {
	g, run, procs, _ := newGW(t)
	procs.Add(gwOf(g, 10))
	run.OnFunc([]string{g.Launcher, "gateway", "stop"}, func(execx.Cmd) execx.Result { procs.Remove(10); return execx.Result{} })
	if err := g.StopStale(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(run.Calls()) != 1 || len(procs.Killed()) != 0 {
		t.Errorf("calls=%v killed=%v", run.Calls(), procs.Killed())
	}
}

// An unknown pid is never a licence to kill: the plain stop runs, and a
// gateway that stays is reported.
func TestStopStaleWithUnknownPIDKillsNothing(t *testing.T) {
	g, run, procs, _ := newGW(t)
	procs.Add(gwOf(g, 10))
	run.OnPrefix([]string{g.Launcher, "gateway", "stop"}, execx.Result{})
	err := g.StopStale(context.Background(), 0)
	if err == nil || len(procs.Killed()) != 0 || sawArg(run.Calls(), "--all") {
		t.Fatalf("err=%v killed=%v calls=%v", err, procs.Killed(), run.Calls())
	}
}

func TestStopStaleFailsWhenTheStaleProcessSurvivesTheKill(t *testing.T) {
	g, run, procs, _ := newGW(t)
	procs.Add(gwOf(g, 10))
	run.OnPrefix([]string{g.Launcher}, execx.Result{})
	g.Procs = &stuckProcs{FakeProcs: procs}
	if err := g.StopStale(context.Background(), 10); err == nil || !strings.Contains(err.Error(), "could not be stopped") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopStaleWhenTheGatewayIsAlreadyGone(t *testing.T) {
	g, run, _, _ := newGW(t)
	if err := g.StopStale(context.Background(), 10); err != nil || len(run.Calls()) != 0 {
		t.Fatalf("err=%v calls=%v", err, run.Calls())
	}
}
