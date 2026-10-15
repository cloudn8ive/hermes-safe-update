package hermes

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// ---- #123971: Hermes' own relaunch check exits 1 ----

func TestToleratedRelaunchExit(t *testing.T) {
	good := readFixture(t, "update-relaunch-unverified.txt")
	otherFailure := readFixture(t, "update-relaunch-unverified-other-failure.txt")
	fatal := readFixture(t, "update-relaunch-unverified-fatal.txt")
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"the relaunch family alone", good, true},
		{"CRLF output", crlf(good), true},
		{"another ✗ line next to it", otherFailure, false},
		{"another exception line next to it", good + "OSError: [WinError 5] Access is denied\n", false},
		{"a bare fatal: line next to it", fatal, false},
		{"a bare Error: line next to it", good + "Error: spawn npm ENOENT\n", false},
		{"ERROR:, error: and FATAL: lines", good + "ERROR: pip failed\n", false},
		{"error: line", good + "error: could not lock config file\n", false},
		{"FATAL: line", good + "FATAL: out of memory\n", false},
		{"the word in the middle of a line", good + "note: no fatal: here, error: neither\n", true},
		{"no 'Update complete!'", strings.ReplaceAll(good, "Update complete!", "Update stopped"), false},
		{"update complete, no marker", "✓ Update complete! (v1.0 → v1.1)\n", false},
		{"marker only as the warning line", "Update complete!\n  ⚠ Windows gateway restart could not be verified for: default\n", true},
		{"the follow-up wording", "Update complete!\nWindows gateway recovery failed: boom\n", true},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, ok := ToleratedRelaunchExit(c.out)
			if ok != c.want {
				t.Fatalf("ok = %v, want %v (line %q)", ok, c.want, line)
			}
			if ok && !hasRelaunchMarker(line) {
				t.Errorf("line %q carries no marker", line)
			}
			if ok && line != strings.TrimSpace(line) {
				t.Errorf("line %q is not trimmed", line)
			}
		})
	}
	// the tolerated line is the first marker line without its glyph, not a traceback frame
	line, _ := ToleratedRelaunchExit(good)
	if line != "Windows gateway restart could not be verified for: default" {
		t.Errorf("line = %q", line)
	}
	if line, _ := ToleratedRelaunchExit(strings.Replace(good, "⚠", "⚠\uFE0F", 1)); line != "Windows gateway restart could not be verified for: default" {
		t.Errorf("with a variation selector: line = %q", line)
	}
}

func TestFatalLineAfterTheRelaunchWarningStaysAProblem(t *testing.T) {
	fatal := readFixture(t, "update-relaunch-unverified-fatal.txt")
	onNew := GatewayState{CodeSHA: sha40, PID: 5}
	r := EvaluateUpdate(VerifyInput{RC: 1, Output: fatal, Head: sha40, Gateway: onNew, Alive: true})
	if r.OK || r.RelaunchTolerated != "" || !strings.HasPrefix(r.Line, "RESULT: PROBLEM") {
		t.Fatalf("a fatal: line was tolerated: %+v", r)
	}
}

func TestEvaluateUpdateRelaunchUnverified(t *testing.T) {
	good := readFixture(t, "update-relaunch-unverified.txt")
	other := readFixture(t, "update-relaunch-unverified-other-failure.txt")
	onNew := GatewayState{CodeSHA: sha40, PID: 5}
	cases := []struct {
		name   string
		in     VerifyInput
		wantOK bool
	}{
		{"rc 1, family only, gateway on the new code", VerifyInput{RC: 1, Output: good, Head: sha40, Gateway: onNew, Alive: true}, true},
		{"rc 2 is another matter", VerifyInput{RC: 2, Output: good, Head: sha40, Gateway: onNew, Alive: true}, false},
		{"rc 3", VerifyInput{RC: 3, Output: good, Head: sha40, Gateway: onNew, Alive: true}, false},
		{"another ✗ line", VerifyInput{RC: 1, Output: other, Head: sha40, Gateway: onNew, Alive: true}, false},
		{"gateway on the old code", VerifyInput{RC: 1, Output: good, Head: sha40, Gateway: GatewayState{CodeSHA: "old", PID: 5}, Alive: true}, false},
		{"gateway dead", VerifyInput{RC: 1, Output: good, Head: sha40, Gateway: onNew, Alive: false}, false},
		{"no gateway ran before", VerifyInput{RC: 1, Output: good, Head: sha40, NoGateway: true}, false},
		{"desktop build not verified", VerifyInput{RC: 1, Output: good, Head: sha40, Gateway: onNew, Alive: true, VerifyRC: 1}, false},
		{"unknown head", VerifyInput{RC: 1, Output: good, Head: "", Gateway: GatewayState{PID: 5}, Alive: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := EvaluateUpdate(c.in)
			if r.OK != c.wantOK {
				t.Fatalf("OK = %v, want %v: %s", r.OK, c.wantOK, r.Line)
			}
			if c.wantOK {
				if r.RelaunchTolerated == "" || r.KnownFalseFleet || !strings.Contains(r.Line, "#123971") {
					t.Errorf("result = %+v", r)
				}
			} else if r.RelaunchTolerated != "" || !strings.HasPrefix(r.Line, "RESULT: PROBLEM") {
				t.Errorf("result = %+v", r)
			}
		})
	}
}

// ---- #129171: the gateway Hermes left stopped ----

func TestParseGatewayHeld(t *testing.T) {
	why, ok := ParseGatewayHeld(readFixture(t, "update-gateway-held.txt"))
	if !ok || why != "the checkout is not whole (pull interrupted)" {
		t.Errorf("held = %q, %v", why, ok)
	}
	if _, ok := ParseGatewayHeld(crlf(readFixture(t, "update-gateway-held.txt"))); !ok {
		t.Error("CRLF output not recognised")
	}
	for _, out := range []string{"", readFixture(t, "update-relaunch-unverified.txt"), "Paused gateway(s) resumed"} {
		if why, ok := ParseGatewayHeld(out); ok {
			t.Errorf("%q read as held: %q", out, why)
		}
	}
}

func TestGatewayStartFailureWording(t *testing.T) {
	out := readFixture(t, "update-start-failed.txt")
	if !GatewayStartFailedRe.MatchString(out) || !GatewayStartFailed(0, out) {
		t.Error("the FAILED line is not recognised")
	}
	if got := ParseGatewayTask(out); got != "Hermes_Gateway_work" {
		t.Errorf("task = %q (the name is Hermes' own, profile suffix included)", got)
	}
	if got := ParseGatewayTask(crlf(out)); got != "Hermes_Gateway_work" {
		t.Errorf("CRLF task = %q", got)
	}
	if !GatewayStartFailed(1, "") || GatewayStartFailed(0, "✓ Gateway started via Scheduled Task (PID: 4)") {
		t.Error("GatewayStartFailed on rc / success text")
	}
	if got := ParseGatewayTask("✓ Gateway started"); got != "" {
		t.Errorf("task from nothing = %q", got)
	}
}

// A token that begins with / or - is a schtasks switch, not a task name.
func TestParseGatewayTaskRefusesSwitches(t *testing.T) {
	for _, tok := range []string{"/S", "/U", "/RU", "-x", "--help", "/?"} {
		if got := ParseGatewayTask("  Recovery: schtasks /Run /TN " + tok + " remote"); got != "" {
			t.Errorf("%q read as a task name: %q", tok, got)
		}
	}
	if got := ParseGatewayTask("Recovery: schtasks /Run /TN Hermes_Gateway"); got != "Hermes_Gateway" {
		t.Errorf("default profile task = %q", got)
	}
}

// ---- #126821: two relaunches of one profile ----

func TestDoubleRelaunchProfiles(t *testing.T) {
	if got := DoubleRelaunchProfiles(readFixture(t, "update-double-relaunch.txt")); !reflect.DeepEqual(got, []string{"work"}) {
		t.Errorf("both lines name work and only work: %v", got)
	}
	if got := DoubleRelaunchProfiles(crlf(readFixture(t, "update-double-relaunch.txt"))); !reflect.DeepEqual(got, []string{"work"}) {
		t.Errorf("CRLF: %v", got)
	}
	if got := DoubleRelaunchProfiles(readFixture(t, "update-relaunch-different-profiles.txt")); got != nil {
		t.Errorf("different profiles are legitimate: %v", got)
	}
	if got := DoubleRelaunchProfiles("✓ Restarting manual gateway profile(s): work\n"); got != nil {
		t.Errorf("one line only: %v", got)
	}
}

// ---- gateway start falls back to the task Hermes names ----

func startFailure(t *testing.T, g *CLIGateway, run *execx.Fake, out string) {
	t.Helper()
	g.GOOS = "windows"
	run.On([]string{g.Launcher, "gateway", "start", "--all"}, execx.Result{Code: 1, Output: out})
}

func TestStartFallsBackToTaskWhenDirectSpawnFails(t *testing.T) {
	g, run, procs, home := newGW(t)
	g.Home = home
	auto := &platform.FakeAutostart{Tasks: map[string]string{"Hermes_Gateway_work": "wscript.exe"}}
	g.Tasks = auto
	startFailure(t, g, run, readFixture(t, "update-start-failed.txt"))
	// Task Scheduler brings the gateway up
	auto.OnRunTask = func(string) { procs.Add(gatewayAt(home, 700)) }
	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("Start = %v", err)
	}
	if !reflect.DeepEqual(auto.TasksRun, []string{"Hermes_Gateway_work"}) {
		t.Errorf("tasks run = %v, want the task Hermes named", auto.TasksRun)
	}
	if got := g.StartedVia(); got != "Task Scheduler (Hermes_Gateway_work)" {
		t.Errorf("via = %q", got)
	}
}

// gatewayAt is a gateway process of the install at home.
func gatewayAt(home string, pid int) platform.Process {
	py := filepath.Join(home, "python.exe")
	return platform.Process{PID: pid, Name: "python.exe", Exe: py, Cmdline: py + " -m hermes_cli.main gateway run"}
}

func TestStartTaskFallbackNeedsHermesHintAndARegisteredTask(t *testing.T) {
	failed := readFixture(t, "update-start-failed.txt")
	cases := []struct {
		name    string
		rc      int
		out     string
		tasks   map[string]string
		wantErr bool
	}{
		{"no recovery hint in the output", 1, "✗ Gateway start via Scheduled Task FAILED — nothing", map[string]string{"Hermes_Gateway": "x"}, true},
		{"the hint names a task that is not registered", 1, failed, map[string]string{"Hermes_Gateway": "x"}, true},
		{"logon tasks are not supported here", 1, failed, nil, true},
		{"the hint names a switch, even if a task of that name exists", 1, "✗ Gateway start via Scheduled Task FAILED\n  Recovery: schtasks /Run /TN /S host", map[string]string{"/S": "x"}, true},
		{"a hint that starts with a dash", 1, "✗ Gateway start via Scheduled Task FAILED\n  Recovery: schtasks /Run /TN -x", map[string]string{"-x": "x"}, true},
		{"a start that worked is left alone", 0, "✓ Gateway started (PID: 4)\n  If it dies, start it with: schtasks /Run /TN Hermes_Gateway_work", map[string]string{"Hermes_Gateway_work": "x"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, run, _, home := newGW(t)
			g.Home = home
			auto := &platform.FakeAutostart{Tasks: c.tasks}
			g.Tasks = auto
			g.GOOS = "windows"
			run.On([]string{g.Launcher, "gateway", "start", "--all"}, execx.Result{Code: c.rc, Output: c.out})
			if err := g.Start(context.Background()); (err != nil) != c.wantErr {
				t.Errorf("err = %v, want error %v", err, c.wantErr)
			}
			if len(auto.TasksRun) != 0 {
				t.Errorf("a task was run although the conditions were not met: %v", auto.TasksRun)
			}
		})
	}
	t.Run("the task could not be run", func(t *testing.T) {
		g, run, _, home := newGW(t)
		g.Home = home
		auto := &platform.FakeAutostart{Tasks: map[string]string{"Hermes_Gateway_work": "x"}, RunTaskErr: os.ErrPermission}
		g.Tasks = auto
		startFailure(t, g, run, failed)
		err := g.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "could not be run") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestStartTaskFallbackReportsAGatewayThatNeverAppears(t *testing.T) {
	g, run, _, home := newGW(t)
	g.Home = home
	auto := &platform.FakeAutostart{Tasks: map[string]string{"Hermes_Gateway_work": "x"}}
	g.Tasks = auto
	startFailure(t, g, run, readFixture(t, "update-start-failed.txt"))
	err := g.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no gateway appeared") {
		t.Fatalf("err = %v", err)
	}
	if g.StartedVia() != "" {
		t.Errorf("via = %q after a failed start", g.StartedVia())
	}
}

func TestStartExitZeroWithFailedLineIsAFailure(t *testing.T) {
	g, run, _, home := newGW(t)
	g.Home = home
	g.GOOS = "windows"
	run.On([]string{g.Launcher, "gateway", "start", "--all"}, execx.Result{Code: 0, Output: "✗ Gateway start via Scheduled Task FAILED — no stable gateway"})
	if err := g.Start(context.Background()); err == nil {
		t.Fatal("Hermes said FAILED and Start reported success")
	}
}

// ---- RuntimeArgv: Hermes' launcher-free command ----

type runtimeHome struct {
	home, checkout, python string
}

func newRuntimeHome(t *testing.T) runtimeHome {
	t.Helper()
	home := t.TempDir()
	h := runtimeHome{home: home, checkout: filepath.Join(home, "hermes-agent"), python: filepath.Join(home, "tools", "python-3.12", "python.exe")}
	for _, p := range []string{h.python, filepath.Join(h.checkout, "hermes_cli", "_launchers.py")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h runtimeHome) fixture(t *testing.T) string {
	return strings.ReplaceAll(strings.ReplaceAll(readFixture(t, "runtime-command-ok.txt"), "@PYTHON@", jsonPath(h.python)), "\r", "")
}

// jsonPath escapes a path for the JSON fixture (Windows backslashes).
func jsonPath(p string) string { return strings.ReplaceAll(p, `\`, `\\`) }

func TestRuntimeArgvAcceptsHermesCommand(t *testing.T) {
	h := newRuntimeHome(t)
	run := &execx.Fake{}
	run.OnPrefix([]string{h.python}, execx.Result{Output: h.fixture(t)})
	got, err := RuntimeArgv(context.Background(), run, h.python, h.home, h.checkout, []string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0] != h.python || got[1] != "-I" || got[2] != "-c" || got[4] != "--version" {
		t.Errorf("argv = %q", got)
	}
	c := run.Calls()[0]
	want := []string{h.python, "-I", "-B", filepath.Join(h.checkout, "hermes_cli", "_launchers.py"), "--print-runtime-command", "--", "--version"}
	if !reflect.DeepEqual(c.Argv, want) {
		t.Errorf("asked with %q, want %q", c.Argv, want)
	}
	pre, err := DirectPrefix(context.Background(), run, h.python, h.home, h.checkout)
	if err != nil || len(pre) != 4 || pre[2] != "-c" {
		t.Errorf("prefix = %q, %v", pre, err)
	}
}

func TestRuntimeArgvRejectsAnythingElse(t *testing.T) {
	h := newRuntimeHome(t)
	ok := strings.TrimSpace(strings.Split(h.fixture(t), "\n")[1])
	outside := filepath.Join(t.TempDir(), "python.exe")
	if err := os.WriteFile(outside, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		res    execx.Result
		python string
	}{
		"not JSON":              {execx.Result{Output: "no command here"}, h.python},
		"empty output":          {execx.Result{}, h.python},
		"non-zero exit":         {execx.Result{Code: 1, Output: ok}, h.python},
		"missing -I":            {execx.Result{Output: strings.Replace(ok, `"-I"`, `"-S"`, 1)}, h.python},
		"missing -c":            {execx.Result{Output: strings.Replace(ok, `"-c"`, `"-m"`, 1)}, h.python},
		"tail is not the args":  {execx.Result{Output: strings.Replace(ok, `"--version"]`, `"--help"]`, 1)}, h.python},
		"extra argument":        {execx.Result{Output: strings.Replace(ok, `"--version"]`, `"--version", "x"]`, 1)}, h.python},
		"not a python":          {execx.Result{Output: strings.Replace(ok, "python.exe", "evil.exe", 1)}, h.python},
		"interpreter not there": {execx.Result{Output: strings.Replace(ok, "python.exe", "python2.exe", 1)}, h.python},
		"python outside home":   {execx.Result{Output: strings.Replace(ok, jsonPath(h.python), jsonPath(outside), 1)}, h.python},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			run := &execx.Fake{}
			run.OnPrefix([]string{c.python}, c.res)
			if got, err := RuntimeArgv(context.Background(), run, c.python, h.home, h.checkout, []string{"--version"}); err == nil {
				t.Errorf("accepted %q", got)
			}
		})
	}
	t.Run("no managed python", func(t *testing.T) {
		if _, err := RuntimeArgv(context.Background(), &execx.Fake{}, "", h.home, h.checkout, nil); err == nil {
			t.Error("accepted without a Python")
		}
	})
	t.Run("no launchers script", func(t *testing.T) {
		if _, err := RuntimeArgv(context.Background(), &execx.Fake{}, h.python, h.home, t.TempDir(), nil); err == nil {
			t.Error("accepted without _launchers.py")
		}
	})
}

// ---- LauncherRoute ----

func TestLauncherRouteRewritesOnlyTheLauncher(t *testing.T) {
	fake := &execx.Fake{}
	fake.OnPrefix([]string{"new"}, execx.Result{Output: "new ran"})
	fake.OnPrefix([]string{"py"}, execx.Result{Output: "py ran"})
	fake.OnPrefix([]string{"other"}, execx.Result{Output: "other ran"})
	route := &LauncherRoute{}
	run := route.Wrap(fake)
	ctx := context.Background()

	// no route yet: untouched
	if _, err := run.Run(ctx, execx.Cmd{Argv: []string{"old", "x"}}); err != nil {
		t.Fatal(err)
	}
	route.Set("old", []string{"new"})
	if _, err := run.Run(ctx, execx.Cmd{Argv: []string{"old", "gateway", "start"}, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Stream(ctx, execx.Cmd{Argv: []string{"old", "update"}}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Run(ctx, execx.Cmd{Argv: []string{"other", "old"}}); err != nil {
		t.Fatal(err)
	}
	route.Set("old", []string{"py", "-I", "-c", "boot"})
	if _, err := run.Run(ctx, execx.Cmd{Argv: []string{"old", "--version"}}); err != nil {
		t.Fatal(err)
	}
	route.Clear()
	if _, err := run.Run(ctx, execx.Cmd{Argv: []string{"old", "y"}}); err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, c := range fake.Calls() {
		got = append(got, c.Argv)
	}
	want := [][]string{
		{"old", "x"},
		{"new", "gateway", "start"},
		{"new", "update"},
		{"other", "old"},
		{"py", "-I", "-c", "boot", "--version"},
		{"old", "y"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q\nwant %q", got, want)
	}
	if fake.Calls()[1].Timeout != time.Second {
		t.Errorf("timeout lost: %v", fake.Calls()[1].Timeout)
	}
	// a nil route is no route
	if (*LauncherRoute)(nil).Wrap(fake) != execx.Runner(fake) {
		t.Error("a nil route must return the runner itself")
	}
}
