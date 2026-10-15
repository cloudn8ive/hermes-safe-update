package hermes

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// ---- StartAfterStale: the start after StopStale, other gateways running ----

// staleWorld is an install with a stale gateway (pid 10) and another
// profile's gateway (pid 11). `gateway stop` ends 10 only; `gateway start`
// (plain, see plainStartBringsUp12) brings pid 12 up. goos is "windows" or "linux".
func staleWorld(t *testing.T, goos string) (*CLIGateway, *execx.Fake, *platform.FakeProcs) {
	t.Helper()
	g, run, procs, _ := newGW(t)
	g.GOOS = goos
	procs.Add(gwOf(g, 10))
	procs.Add(gwOf(g, 11))
	run.OnFunc([]string{g.Launcher, "gateway", "stop"}, func(execx.Cmd) execx.Result { procs.Remove(10); return execx.Result{} })
	return g, run, procs
}

// plainStartBringsUp12 makes `gateway start` start gateway 12.
func plainStartBringsUp12(g *CLIGateway, run *execx.Fake, procs *platform.FakeProcs) {
	run.OnFunc([]string{g.Launcher, "gateway", "start"}, func(execx.Cmd) execx.Result { procs.Add(gwOf(g, 12)); return execx.Result{} })
}

func ran(calls []execx.Cmd, argv ...string) bool {
	for _, c := range calls {
		if strings.Join(c.Argv[1:], " ") == strings.Join(argv, " ") {
			return true
		}
	}
	return false
}

// With another profile's gateway still running, Start is a no-op (it returns
// at once because "a gateway exists"), so the stale gateway would stay down.
// StartAfterStale runs the plain `gateway start` anyway, never with `--all`
// (which would sweep the other gateway), on Windows and POSIX.
func TestStartAfterStaleStartsEvenWhenAnotherGatewayRuns(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			g, run, procs := staleWorld(t, goos)
			plainStartBringsUp12(g, run, procs)
			ctx := context.Background()
			if err := g.Start(ctx); err != nil || ran(run.Calls(), "gateway", "start") {
				t.Fatalf("precondition: Start must be the no-op the bug is about (err=%v calls=%v)", err, run.Calls())
			}
			if err := g.StopStale(ctx, 10); err != nil {
				t.Fatal(err)
			}
			if err := g.StartAfterStale(ctx, 10); err != nil {
				t.Fatalf("StartAfterStale = %v", err)
			}
			calls := run.Calls()
			if !ran(calls, "gateway", "start") {
				t.Errorf("`gateway start` was not run: %v", calls)
			}
			if sawArg(calls, "--all") {
				t.Errorf("an `--all` verb ran, it would sweep the other profile's gateway: %v", calls)
			}
			if k := procs.Killed(); len(k) != 0 {
				t.Errorf("killed %v", k)
			}
			if pids, _ := g.PIDs(ctx); len(pids) != 2 {
				t.Errorf("pids = %v, want the other gateway (11) and the new one (12)", pids)
			}
			if g.StartedVia() != "gateway start" {
				t.Errorf("via = %q", g.StartedVia())
			}
		})
	}
}

// `gateway start` can answer "already running" and do nothing (its own check
// found a gateway). The other profile's gateway must not make that look like
// a start.
func TestStartAfterStaleSaysSoWhenNothingNewAppears(t *testing.T) {
	g, run, _ := staleWorld(t, "windows")
	run.On([]string{g.Launcher, "gateway", "start"}, execx.Result{Output: "✓ Gateway already running (PID: 11)"})
	ctx := context.Background()
	if err := g.StopStale(ctx, 10); err != nil {
		t.Fatal(err)
	}
	err := g.StartAfterStale(ctx, 10)
	if err == nil || !strings.Contains(err.Error(), "no new gateway process appeared") {
		t.Fatalf("err = %v", err)
	}
	if g.StartedVia() != "" {
		t.Errorf("via = %q after a start that started nothing", g.StartedVia())
	}
}

// If the stale gateway is somehow still there, no second one is started.
func TestStartAfterStaleRefusesWhileTheStaleGatewayRuns(t *testing.T) {
	g, run, _ := staleWorld(t, "linux")
	err := g.StartAfterStale(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "still running") || len(run.Calls()) != 0 {
		t.Fatalf("err=%v calls=%v", err, run.Calls())
	}
}

// A failed `gateway start` falls back to the Scheduled Task; with another
// gateway already running, that gateway is not the proof the task worked.
func TestStartAfterStaleTaskFallbackNeedsANewGateway(t *testing.T) {
	failed := readFixture(t, "update-start-failed.txt")
	for _, c := range []struct {
		name    string
		onTask  func(g *CLIGateway, procs *platform.FakeProcs)
		wantErr bool
	}{
		{"the task brings a new gateway up", func(g *CLIGateway, procs *platform.FakeProcs) { procs.Add(gwOf(g, 12)) }, false},
		{"the task starts nothing, the other gateway still runs", func(*CLIGateway, *platform.FakeProcs) {}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, run, procs := staleWorld(t, "windows")
			run.On([]string{g.Launcher, "gateway", "start"}, execx.Result{Code: 1, Output: failed})
			auto := &platform.FakeAutostart{Tasks: map[string]string{"Hermes_Gateway_work": "wscript.exe"}}
			auto.OnRunTask = func(string) { c.onTask(g, procs) }
			g.Tasks = auto
			ctx := context.Background()
			if err := g.StopStale(ctx, 10); err != nil {
				t.Fatal(err)
			}
			err := g.StartAfterStale(ctx, 10)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error %v", err, c.wantErr)
			}
			if c.wantErr && !strings.Contains(err.Error(), "was run but no gateway appeared") {
				t.Errorf("err = %v; the fallback itself must not take the other profile's gateway for the one it started", err)
			}
			if !c.wantErr && !strings.HasPrefix(g.StartedVia(), "Task Scheduler") {
				t.Errorf("via = %q", g.StartedVia())
			}
		})
	}
}
