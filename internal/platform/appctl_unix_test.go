package platform

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeSig struct {
	alive    map[int]bool
	termed   []int
	killed   []int
	termErr  error
	killFail map[int]bool
}

func (f *fakeSig) Term(pid int) error { f.termed = append(f.termed, pid); return f.termErr }
func (f *fakeSig) Kill(pid int) error {
	f.killed = append(f.killed, pid)
	if f.killFail[pid] {
		return errors.New("operation not permitted")
	}
	delete(f.alive, pid)
	return nil
}
func (f *fakeSig) Exists(pid int) bool { return f.alive[pid] }

func tree() []Process {
	return []Process{
		{PID: 1, PPID: 0}, {PID: 100, PPID: 1}, // 100 plays this tool
		{PID: 200, PPID: 1}, {PID: 201, PPID: 200}, {PID: 202, PPID: 200}, {PID: 300, PPID: 201},
		{PID: 400, PPID: 1},
	}
}

func TestDescendantsDeepestFirst(t *testing.T) {
	got := descendants(tree(), 200)
	if !reflect.DeepEqual(got, []int{300, 201, 202}) {
		t.Errorf("got %v", got)
	}
	if got := descendants(tree(), 400); len(got) != 0 {
		t.Errorf("leaf has descendants %v", got)
	}
	// A cycle in a corrupt table must not loop forever.
	cyc := []Process{{PID: 5, PPID: 6}, {PID: 6, PPID: 5}}
	if got := descendants(cyc, 5); !reflect.DeepEqual(got, []int{6}) {
		t.Errorf("cycle: %v", got)
	}
}

func TestAncestors(t *testing.T) {
	if got := ancestors(tree(), 300); !reflect.DeepEqual(got, []int{201, 200, 1}) {
		t.Errorf("got %v", got)
	}
}

func newProcs(sig *fakeSig, self int, procs []Process) *unixProcs {
	return &unixProcs{
		list: func(context.Context) ([]Process, error) { return procs, nil },
		sig:  sig, self: self,
	}
}

func TestKillTreeKillsChildrenBeforeParent(t *testing.T) {
	sig := &fakeSig{alive: map[int]bool{200: true, 201: true, 202: true, 300: true}}
	if err := newProcs(sig, 100, tree()).KillTree(context.Background(), Process{PID: 200}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sig.killed, []int{300, 201, 202, 200}) {
		t.Errorf("order %v", sig.killed)
	}
}

func TestKillTreeRefusesOwnTreeAndInit(t *testing.T) {
	sig := &fakeSig{alive: map[int]bool{}}
	p := newProcs(sig, 300, tree())
	for _, pid := range []int{200, 300, 1, 0} { // ancestor, self, init, invalid
		if err := p.KillTree(context.Background(), Process{PID: pid}); err == nil {
			t.Errorf("pid %d: want refusal", pid)
		}
	}
	if len(sig.killed) != 0 {
		t.Errorf("nothing may be signalled, got %v", sig.killed)
	}
}

func TestKillTreeToleratesExitedAndReportsSurvivors(t *testing.T) {
	// 300 fails to die and still exists -> error; 202 fails but is gone -> fine.
	sig := &fakeSig{alive: map[int]bool{300: true}, killFail: map[int]bool{300: true, 202: true}}
	err := newProcs(sig, 100, tree()).KillTree(context.Background(), Process{PID: 200})
	if err == nil {
		t.Fatal("a survivor must be reported")
	}
	if got := len(sig.killed); got != 4 {
		t.Errorf("every victim is still tried, killed %v", sig.killed)
	}
}

func TestAliveTreatsZombieAsDead(t *testing.T) {
	sig := &fakeSig{alive: map[int]bool{7: true}}
	p := newProcs(sig, 1, nil)
	p.isZombie = func(int) bool { return true }
	if p.Alive(7) {
		t.Error("zombie reported alive")
	}
	p.isZombie = nil
	if !p.Alive(7) || p.Alive(8) || p.Alive(0) {
		t.Error("Alive disagrees with Exists")
	}
}

type recRun struct {
	calls [][]string
	fail  map[string]error
}

func (r *recRun) run(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return "out", r.fail[name]
}

func newApp(goos string, sig *fakeSig, r *recRun, env map[string]string) *unixApp {
	return &unixApp{
		goos: goos, sig: sig, procs: newProcs(sig, 1, nil), run: r.run,
		getenv: func(k string) string { return env[k] },
		exists: func(string) bool { return true },
	}
}

func TestRequestCloseDarwinSkipsOsascriptWhenPidDead(t *testing.T) {
	sig, r := &fakeSig{}, &recRun{} // pid 501 not alive: osascript would relaunch the app
	if err := newApp("darwin", sig, r, nil).RequestClose(context.Background(), 501); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 || len(sig.termed) != 0 {
		t.Errorf("dead pid must be a no-op: calls %v termed %v", r.calls, sig.termed)
	}
}

func TestRequestCloseDarwinQuitsByBundleID(t *testing.T) {
	sig, r := &fakeSig{alive: map[int]bool{501: true}}, &recRun{}
	if err := newApp("darwin", sig, r, nil).RequestClose(context.Background(), 501); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"osascript", "-e", `tell application id "com.nousresearch.hermes" to quit`}}
	if !reflect.DeepEqual(r.calls, want) || len(sig.termed) != 0 {
		t.Errorf("calls %v termed %v", r.calls, sig.termed)
	}
}

func TestRequestCloseDarwinFallsBackToSIGTERM(t *testing.T) {
	sig, r := &fakeSig{alive: map[int]bool{501: true}}, &recRun{fail: map[string]error{"osascript": errors.New("exit 1")}}
	if err := newApp("darwin", sig, r, nil).RequestClose(context.Background(), 501); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sig.termed, []int{501}) {
		t.Errorf("termed %v", sig.termed)
	}
	sig2 := &fakeSig{alive: map[int]bool{501: true}, termErr: errors.New("no such process")}
	if err := newApp("darwin", sig2, r, nil).RequestClose(context.Background(), 501); err == nil {
		t.Error("both paths failing must error")
	}
}

func TestRequestCloseLinuxIsSIGTERMToMainPidOnly(t *testing.T) {
	sig, r := &fakeSig{}, &recRun{}
	if err := newApp("linux", sig, r, nil).RequestClose(context.Background(), 4242); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sig.termed, []int{4242}) || len(r.calls) != 0 {
		t.Errorf("termed %v calls %v", sig.termed, r.calls)
	}
}

func TestForceCloseKillsTree(t *testing.T) {
	sig := &fakeSig{alive: map[int]bool{200: true, 201: true, 202: true, 300: true}}
	a := newApp("linux", sig, &recRun{}, nil)
	a.procs = newProcs(sig, 100, tree())
	if err := a.ForceClose(context.Background(), Process{PID: 200}); err != nil || len(sig.killed) != 4 {
		t.Errorf("err %v killed %v", err, sig.killed)
	}
}

func TestBundlePath(t *testing.T) {
	for in, want := range map[string]string{
		"/Applications/Hermes.app/Contents/MacOS/Hermes":                "/Applications/Hermes.app",
		"/c/apps/desktop/release/mac-arm64/Hermes.app/Contents/MacOS/H": "/c/apps/desktop/release/mac-arm64/Hermes.app",
		"/usr/bin/hermes": "",
	} {
		if got := bundlePath(in); got != want {
			t.Errorf("bundlePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLaunchDarwinUsesOpenOnBundle(t *testing.T) {
	r := &recRun{}
	a := newApp("darwin", &fakeSig{}, r, nil)
	if err := a.Launch(context.Background(), "/Applications/Hermes.app/Contents/MacOS/Hermes", []string{"--x"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"/usr/bin/xattr", "-dr", "com.apple.quarantine", "/Applications/Hermes.app"},
		{"/usr/bin/open", "/Applications/Hermes.app", "--args", "--x"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls %v", r.calls)
	}
	if err := a.Launch(context.Background(), "/usr/bin/hermes", nil); err == nil {
		t.Error("an exe outside a bundle cannot be opened on macOS")
	}
	r.fail = map[string]error{"/usr/bin/open": errors.New("exit 1")}
	if err := a.Launch(context.Background(), "/Applications/Hermes.app/Contents/MacOS/Hermes", nil); err == nil {
		t.Error("open failing must surface")
	}
}

func TestLaunchLinuxDelegatesToDetachedLauncher(t *testing.T) {
	var gotExe string
	a := newApp("linux", &fakeSig{}, &recRun{}, nil)
	a.launcher = func(_ context.Context, exe string, _ []string) error { gotExe = exe; return nil }
	if err := a.Launch(context.Background(), "/x/hermes", nil); err != nil || gotExe != "/x/hermes" {
		t.Errorf("err %v exe %q", err, gotExe)
	}
}

func TestCanLaunch(t *testing.T) {
	setuid := func(string) bool { return true }
	noSetuid := func(string) bool { return false }
	userns := func() bool { return true }
	noUserns := func() bool { return false }
	cases := []struct {
		name    string
		goos    string
		env     map[string]string
		missing bool
		setuid  func(string) bool
		userns  func() bool
		wantOK  bool
	}{
		{"mac desktop", "darwin", nil, false, nil, nil, true},
		{"mac over ssh", "darwin", map[string]string{"SSH_CONNECTION": "a b c d"}, false, nil, nil, false},
		{"mac exe missing", "darwin", nil, true, nil, nil, false},
		{"linux no display", "linux", nil, false, setuid, nil, false},
		{"linux x11 setuid sandbox", "linux", map[string]string{"DISPLAY": ":0"}, false, setuid, noUserns, true},
		{"linux wayland userns", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, false, noSetuid, userns, true},
		{"linux sandbox opt-out", "linux", map[string]string{"DISPLAY": ":0", "ELECTRON_DISABLE_SANDBOX": "1"}, false, nil, nil, true},
		{"linux no usable sandbox", "linux", map[string]string{"DISPLAY": ":0"}, false, noSetuid, noUserns, false},
		{"linux ssh with display", "linux", map[string]string{"DISPLAY": ":0", "SSH_TTY": "/dev/pts/1"}, false, setuid, userns, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(c.goos, &fakeSig{}, &recRun{}, c.env)
			a.exists = func(string) bool { return !c.missing }
			a.chromeSandboxSetuid, a.userNamespacesWork = c.setuid, c.userns
			ok, why := a.CanLaunch("/x/hermes")
			if ok != c.wantOK {
				t.Fatalf("ok = %v (%s)", ok, why)
			}
			if !ok && why == "" {
				t.Error("a refusal needs a reason")
			}
		})
	}
}

func createdTree(created map[int]time.Time) []Process {
	ps := tree()
	for i := range ps {
		ps[i].Created = created[ps[i].PID]
	}
	return ps
}

func TestKillTreeRefusesReusedPid(t *testing.T) {
	t0 := time.Unix(1790000000, 0)
	now := map[int]time.Time{200: t0.Add(time.Hour), 201: t0.Add(time.Hour), 202: t0.Add(time.Hour), 300: t0.Add(time.Hour)}
	for name, tc := range map[string]struct {
		classified Process
		snapshot   []Process
	}{
		"created changed": {Process{PID: 200, Created: t0}, createdTree(now)},
		"pid gone":        {Process{PID: 777, Created: t0}, createdTree(now)},
	} {
		t.Run(name, func(t *testing.T) {
			sig := &fakeSig{alive: map[int]bool{200: true, 201: true, 202: true, 300: true}}
			err := newProcs(sig, 100, tc.snapshot).KillTree(context.Background(), tc.classified)
			if !errors.Is(err, ErrProcessChanged) {
				t.Fatalf("err = %v, want ErrProcessChanged", err)
			}
			if len(sig.killed) != 0 {
				t.Errorf("nothing may be signalled, got %v", sig.killed)
			}
		})
	}
}

func TestKillTreeKillsWhenCreatedUnchanged(t *testing.T) {
	t0 := time.Unix(1790000000, 0)
	now := map[int]time.Time{200: t0, 201: t0.Add(time.Second), 202: t0.Add(time.Second), 300: t0.Add(2 * time.Second)}
	sig := &fakeSig{alive: map[int]bool{200: true, 201: true, 202: true, 300: true}}
	// a sub-second difference is tick rounding, not a new process
	err := newProcs(sig, 100, createdTree(now)).KillTree(context.Background(), Process{PID: 200, Created: t0.Add(300 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sig.killed, []int{300, 201, 202, 200}) {
		t.Errorf("order %v", sig.killed)
	}
}

func TestForceCloseRefusesReusedPid(t *testing.T) {
	t0 := time.Unix(1790000000, 0)
	sig := &fakeSig{alive: map[int]bool{200: true}}
	a := newApp("linux", sig, &recRun{}, nil)
	a.procs = newProcs(sig, 100, createdTree(map[int]time.Time{200: t0.Add(time.Minute)}))
	if err := a.ForceClose(context.Background(), Process{PID: 200, Created: t0}); !errors.Is(err, ErrProcessChanged) || len(sig.killed) != 0 {
		t.Errorf("err %v killed %v", err, sig.killed)
	}
}

func TestDescendantsSkipsStalePpid(t *testing.T) {
	t0 := time.Unix(1790000000, 0)
	ps := []Process{
		{PID: 200, PPID: 1, Created: t0},
		{PID: 201, PPID: 200, Created: t0.Add(time.Second)},
		{PID: 500, PPID: 200, Created: t0.Add(-time.Hour)}, // older than "parent": ppid is stale
		{PID: 501, PPID: 500, Created: t0.Add(-time.Minute)},
	}
	if got := descendants(ps, 200); !reflect.DeepEqual(got, []int{201}) {
		t.Errorf("got %v, want [201]", got)
	}
}
