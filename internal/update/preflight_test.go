package update

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// newPreflightHarness is newHarness with prompts nobody answers giving up
// after two real seconds instead of ten minutes or one: a regression in these
// tests must fail, not hang.
func newPreflightHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.cfg.Tunables.ConfirmTimeoutS = 2
	h.cfg.Tunables.IdlePollS = 2
	return h
}

// stoppedBeforeAnythingChanged is what every pre-flight stop must leave: exit
// 4, no `hermes update`, no close request, no marker claim, no session read.
func stoppedBeforeAnythingChanged(t *testing.T, h *harness, err error) {
	t.Helper()
	if !errors.Is(err, apperr.ErrPreflightStop) || apperr.ExitCode(err) != apperr.PreflightStop {
		t.Fatalf("err = %v (exit %d), want a pre-flight stop\n%s", err, apperr.ExitCode(err), h.output())
	}
	if h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 || len(h.procs.Killed()) != 0 {
		t.Errorf("something changed: updates=%d app=%v marker=%+v killed=%v", h.updates, h.app.Calls(), h.marker, h.procs.Killed())
	}
	if h.sessions.calls != 0 || h.gw.stops != 0 {
		t.Errorf("the stop came too late: sessions read %d times, gateway stops %d", h.sessions.calls, h.gw.stops)
	}
}

// stopFlagSets are the ways to run that must not talk a pre-flight stop away:
// the stops are about what hermes update would do to a closed Hermes, so no
// "don't ask" flag changes them.
var stopFlagSets = [][]string{nil, {"unattended"}, {"yes"}, {"force"}, {"unattended", "yes", "force"}}

// ---- #126860: elevation guard ----

func TestElevatedRunStopsBeforeAnythingCloses(t *testing.T) {
	for _, flags := range stopFlagSets {
		h := newPreflightHarness(t)
		h.plat.Elevation = platform.FakeElevation{IsElevated: true}
		h.con.Keys = []platform.Key{key('y')}
		err := h.run(flags...)
		stoppedBeforeAnythingChanged(t, h, err)
		h.mustContain("running as administrator", "--allow-elevated", "Nothing was changed")
	}
}

func TestAllowElevatedGoesOn(t *testing.T) {
	h := newPreflightHarness(t)
	h.plat.Elevation = platform.FakeElevation{IsElevated: true}
	h.cfg.Run.AllowElevated = true
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("running as administrator (--allow-elevated)")
}

// check never closes anything, so it only says so.
func TestCheckModeOnlyWarnsWhenElevated(t *testing.T) {
	h := newPreflightHarness(t)
	h.mode = ModeCheck
	h.plat.Elevation = platform.FakeElevation{IsElevated: true}
	if err := h.run(); err != nil {
		t.Fatalf("check must not stop for elevation: %v\n%s", err, h.output())
	}
	h.mustContain("running as administrator", "A real run stops here unless you pass --allow-elevated")
}

func TestUnknownElevationIsNotAStop(t *testing.T) {
	h := newPreflightHarness(t)
	h.plat.Elevation = platform.FakeElevation{IsElevated: true, Err: errors.New("token query failed")}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d", err, h.updates)
	}
}

// ---- #137186: checkout lock ----

// The holder record has no "ct:" line on purpose. Run reads process creation
// times from the real platform.ProcessCreated, and a fake pid like 4242 must
// never reach it: Windows' OpenProcess ignores the two low bits of a pid, so a
// real process with pid 4240-4243 would make the fake owner look gone. Without
// "ct:" the owner is judged only by the fake process list. The creation-time
// comparison itself is tested in internal/hermes with an injected lookup.
const lockRecordFixture = "4242\n1760000000\n"

func holdLock(h *harness, record string) *platform.FakeLockProbe {
	p := h.plat.Locks.(*platform.FakeLockProbe)
	p.Info = platform.LockInfo{State: platform.LockHeld, Record: []byte(record)}
	return p
}

func TestCheckoutLockHeldByRunningUpdateStopsBeforeClosing(t *testing.T) {
	h := newPreflightHarness(t)
	probe := holdLock(h, lockRecordFixture)
	h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	stoppedBeforeAnythingChanged(t, h, err)
	h.mustContain("An update is running (pid 4242, python.exe", "checkout lock", "Nothing was closed")
	if got := probe.Asked(); len(got) != 1 {
		t.Errorf("probed %v", got)
	}
}

// The owner died but a child it started still holds the lock: the case
// `hermes update` itself refuses with "its owner exited but a process it
// started still holds the checkout". Before this, Safe Update treated the
// dead owner as stale, closed Hermes, and only then was refused.
func TestCheckoutLockHeldByAnOrphanStopsAndNamesIt(t *testing.T) {
	h := newPreflightHarness(t)
	holdLock(h, lockRecordFixture)
	h.procs.Add(platform.Process{PID: 5000, PPID: 4242, Name: "python.exe"})
	h.marker.stale = true // the dead owner's marker is still there
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	stoppedBeforeAnythingChanged(t, h, err)
	h.mustContain("the update that took it (pid 4242) has exited", "python.exe (pid 5000)",
		"taskkill /PID <pid> /T /F", "The update marker is stale too")
	if strings.Contains(h.output(), "stale update marker found (owner dead); it will be replaced") {
		t.Error("the run must not announce it will replace the marker and go on")
	}
}

func TestCheckoutLockStopIgnoresYesUnattendedAndForce(t *testing.T) {
	for _, flags := range stopFlagSets {
		h := newPreflightHarness(t)
		holdLock(h, lockRecordFixture)
		h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
		h.con.Keys = []platform.Key{key('y')}
		err := h.run(flags...)
		stoppedBeforeAnythingChanged(t, h, err)
		h.mustContain("An update is running (pid 4242")
	}
}

func TestCheckoutLockHeldWithNoReadableRecordStillStops(t *testing.T) {
	h := newPreflightHarness(t)
	holdLock(h, "")
	err := h.run()
	stoppedBeforeAnythingChanged(t, h, err)
	h.mustContain("holder record is unreadable")
}

func TestCheckoutLockFileThatCannotBeOpenedStops(t *testing.T) {
	h := newPreflightHarness(t)
	h.plat.Locks.(*platform.FakeLockProbe).Info = platform.LockInfo{State: platform.LockUnopenable, Detail: "Access is denied."}
	err := h.run()
	stoppedBeforeAnythingChanged(t, h, err)
	h.mustContain("checkout lock file cannot be opened (Access is denied)")
}

func TestCheckStopsOnAHeldLockToo(t *testing.T) {
	h := newPreflightHarness(t)
	h.mode = ModeCheck
	holdLock(h, lockRecordFixture)
	h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) {
		t.Fatalf("check: err = %v", err)
	}
	h.mustContain("An update is running (pid 4242")
}

func TestFreeOrUnaskableLockLetsTheRunGoOn(t *testing.T) {
	h := newPreflightHarness(t)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("free: err=%v updates=%d", err, h.updates)
	}
	if got := h.plat.Locks.(*platform.FakeLockProbe).Asked(); len(got) != 1 || !strings.HasSuffix(got[0], hermes.CheckoutLockNameNoGit) {
		t.Errorf("probed %v", got) // the fake checkout has no .git: the ZIP-install name
	}
	h2 := newPreflightHarness(t)
	p := h2.plat.Locks.(*platform.FakeLockProbe)
	p.Info, p.Err = platform.LockInfo{State: platform.LockHeld}, errors.New("lock probe: boom")
	h2.con.Keys = []platform.Key{key('y')}
	if err := h2.run(); err != nil || h2.updates != 1 {
		t.Fatalf("probe error must not stop: err=%v updates=%d", err, h2.updates)
	}
	h3 := newPreflightHarness(t)
	h3.plat.Locks = nil
	h3.con.Keys = []platform.Key{key('y')}
	if err := h3.run(); err != nil || h3.updates != 1 {
		t.Fatalf("no probe on this OS: err=%v updates=%d", err, h3.updates)
	}
}

// A live marker owner is the older, existing stop and still comes first.
func TestMarkerHolderStillStopsFirst(t *testing.T) {
	h := newPreflightHarness(t)
	holdLock(h, lockRecordFixture)
	h.marker.holder = 999
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) {
		t.Fatal(err)
	}
	h.mustContain("another update holds the marker (pid 999)")
	if got := h.plat.Locks.(*platform.FakeLockProbe).Asked(); len(got) != 0 {
		t.Errorf("lock probed although the marker already stopped the run: %v", got)
	}
}

// ---- #126860: ACL probe ----

func aclTools(parts ...string) string {
	return filepath.Join(append([]string{tHome, "tools"}, parts...)...)
}

// a tools folder with two entries; poisoned makes python-x unreadable.
func aclHarness(t *testing.T, poisoned bool) (*harness, *platform.FakeAccess) {
	t.Helper()
	h := newPreflightHarness(t)
	a := h.plat.Access.(*platform.FakeAccess)
	a.Dirs = map[string][]string{aclTools(): {"python-x", "node-y"}, aclTools("python-x"): {"python.exe"}}
	if poisoned {
		a.Deny(platform.AccessRead, aclTools("python-x"))
	}
	return h, a
}

func TestUnreadableToolsEntryStopsWithTheRepairCommands(t *testing.T) {
	h, _ := aclHarness(t, true)
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	stoppedBeforeAnythingChanged(t, h, err)
	entry := aclTools("python-x")
	h.mustContain("acl", "python-x", "cannot be opened", "WinError 5",
		`takeown /f "`+entry+`" /r /d y`, `icacls "`+entry+`" /reset /t /c /q`, "elevated terminal")
	// the way out is named in the message itself (a user without admin rights
	// cannot run the repair block)
	h.mustContain(`preflight.acl_check: "warn"`, "safe-update.yaml", "expected to fail on these entries")
}

// A refusal that clears by itself (delete-pending, a scanner holding the file)
// must not stop the run; the run waits once for it.
func TestTransientToolsEntryDenialDoesNotStop(t *testing.T) {
	h, a := aclHarness(t, false)
	a.DenyOnce(platform.AccessRead, aclTools("python-x"))
	seen := watchProbes(h, aclTools("python-x"))
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("a passing refusal stopped the run: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if strings.Contains(h.output(), "cannot be opened") {
		t.Errorf("reported a refusal that cleared:\n%s", h.output())
	}
	seen.wantPauseBetweenTwoLooks(t)
}

// The same refusal twice is real and still stops (the second look is not a
// pass-through).
func TestToolsEntryRefusedTwiceStillStopsAfterOnePause(t *testing.T) {
	h, _ := aclHarness(t, true)
	seen := watchProbes(h, aclTools("python-x"))
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	stoppedBeforeAnythingChanged(t, h, err)
	seen.wantPauseBetweenTwoLooks(t)
}

// probeWatch records the harness's slept total at each open of one path, so a
// test can say how long the run waited between two looks at it (not the sum
// of every sleep in the run).
type probeWatch struct {
	platform.Access
	h    *harness
	path string
	at   []time.Duration
}

func (p *probeWatch) Probe(path string, m platform.AccessMask) platform.AccessResult {
	if strings.EqualFold(path, p.path) && m == platform.AccessRead {
		p.at = append(p.at, p.h.slept)
	}
	return p.Access.Probe(path, m)
}

func watchProbes(h *harness, path string) *probeWatch {
	w := &probeWatch{Access: h.plat.Access, h: h, path: path}
	h.plat.Access = w
	return w
}

func (p *probeWatch) wantPauseBetweenTwoLooks(t *testing.T) {
	t.Helper()
	if len(p.at) != 2 {
		t.Fatalf("%q was opened %d times, want 2 (the look and the second look)", p.path, len(p.at))
	}
	if d := p.at[1] - p.at[0]; d < 400*time.Millisecond {
		t.Errorf("waited %v between the two looks, want >= 400ms", d)
	}
}

// slowAccess moves the virtual clock on every open, as a slow share would.
type slowAccess struct {
	platform.Access
	clock *testutil.Clock
	each  time.Duration
}

func (s slowAccess) Probe(path string, m platform.AccessMask) platform.AccessResult {
	s.clock.Advance(s.each)
	return s.Access.Probe(path, m)
}

// The 2 s budget ends the walk; the run says so instead of looking healthy.
func TestACLCheckThatStoppedEarlySaysSo(t *testing.T) {
	h, a := aclHarness(t, false)
	h.plat.Access = slowAccess{Access: a, clock: h.clock, each: time.Second}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("ACL check stopped early after", "deeper entries were not looked at")
}

func TestACLCheckThatFinishedSaysNothingAboutStoppingEarly(t *testing.T) {
	h, _ := aclHarness(t, false)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "stopped early") {
		t.Errorf("unexpected note:\n%s", h.output())
	}
}

// Ctrl-C while the ACL walk runs ends the run, cancelled, with nothing changed
// and no further opens.
func TestCtrlCDuringTheACLWalkEndsTheRun(t *testing.T) {
	h, a := aclHarness(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	n := 0
	h.plat.Access = cancelOnProbe{Access: a, cancel: cancel, after: 2, n: &n}
	err := h.run()
	if !errors.Is(err, context.Canceled) && !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("err = %v, want a cancellation\n%s", err, h.output())
	}
	if n != 2 || h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 {
		t.Errorf("opens after cancel: n=%d updates=%d app=%v", n, h.updates, h.app.Calls())
	}
}

type cancelOnProbe struct {
	platform.Access
	cancel func()
	after  int
	n      *int
}

func (c cancelOnProbe) Probe(path string, m platform.AccessMask) platform.AccessResult {
	*c.n++
	if *c.n == c.after {
		c.cancel()
	}
	return c.Access.Probe(path, m)
}

func TestUnreadableToolsEntryStopIgnoresYesUnattendedAndForce(t *testing.T) {
	for _, flags := range stopFlagSets {
		h, _ := aclHarness(t, true)
		h.con.Keys = []platform.Key{key('y')}
		err := h.run(flags...)
		stoppedBeforeAnythingChanged(t, h, err)
		h.mustContain("WinError 5", `preflight.acl_check: "warn"`)
	}
}

func TestACLCheckWarnModeShowsTheSameAndGoesOn(t *testing.T) {
	h, _ := aclHarness(t, true)
	h.cfg.Preflight.ACLCheck = config.PreflightWarn
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("warn mode must not stop: err=%v updates=%d", err, h.updates)
	}
	h.mustContain(`takeown /f "`+aclTools("python-x")+`"`, "expected to fail")
}

func TestACLCheckOffDoesNotProbe(t *testing.T) {
	h, a := aclHarness(t, true)
	h.cfg.Preflight.ACLCheck = config.PreflightOff
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d", err, h.updates)
	}
	if len(a.Probes()) != 0 {
		t.Errorf("probed with acl_check off: %v", a.Probes())
	}
}

// Defender's folder guard also denies writes: a modify refusal is a warning,
// and no takeown/icacls advice (it would not help).
func TestACLModifyDenialWarnsWithoutStopping(t *testing.T) {
	h, a := aclHarness(t, false)
	a.Deny(platform.AccessModify, aclTools("node-y"))
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("acl: ", "node-y", "cannot be changed or deleted")
	if strings.Contains(h.output(), "takeown") {
		t.Error("takeown advice for a modify-only refusal")
	}
}

func TestHealthyToolsFolderSaysNothing(t *testing.T) {
	h, a := aclHarness(t, false)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "acl") || len(a.Probes()) == 0 {
		t.Errorf("probes=%d, output mentions acl=%v", len(a.Probes()), strings.Contains(h.output(), "acl"))
	}
}

func TestCheckStopsOnUnreadableToolsEntryToo(t *testing.T) {
	h, _ := aclHarness(t, true)
	h.mode = ModeCheck
	if err := h.run(); !errors.Is(err, apperr.ErrPreflightStop) {
		t.Fatalf("check: err = %v", err)
	}
}

// ---- #134121: ARM64 build tools ----

var arm64Vswhere = filepath.Join(append([]string{"pf86"}, hermes.ARM64VswhereRel...)...)

type armHarness struct {
	*harness
	files map[string]bool // tool files that exist
	vswOK execx.Result
}

// armTest builds a harness whose managed Python is the win32-arm64 one, a
// 3-commit update that changes Python dependencies, and a vswhere that finds
// a Visual Studio. No tool file exists yet.
func armTest(t *testing.T) *armHarness {
	t.Helper()
	h := newPreflightHarness(t)
	h.install.Python = filepath.Join(tHome, "tools", "python-3.14.7-win32-arm64", "python.exe")
	yes := true
	no := false
	h.repo.diffNPM, h.repo.diffPy = &no, &yes
	a := &armHarness{harness: h, files: map[string]bool{arm64Vswhere: true}, vswOK: execx.Result{Output: filepath.Join("vs", "BuildTools") + "\r\n"}}
	h.env = map[string]string{"ProgramFiles": "pf", "ProgramFiles(x86)": "pf86", "PATH": "path1"}
	h.exists = func(p string) bool {
		switch filepath.Base(p) {
		case "clang.exe", "clang-format.exe", "vswhere.exe":
			return a.files[p]
		}
		return true
	}
	h.runner.OnFunc(append([]string{arm64Vswhere}, hermes.ARM64VswhereArgs...), func(execx.Cmd) execx.Result { return a.vswOK })
	return a
}

func (a *armHarness) vswhereCalls() int {
	n := 0
	for _, c := range a.runner.Calls() {
		if c.Argv[0] == arm64Vswhere {
			n++
		}
	}
	return n
}

func TestARM64MissingClangWarnsBeforeAnythingCloses(t *testing.T) {
	a := armTest(t)
	a.files[filepath.Join("vs", "BuildTools", "VC", "Tools", "Llvm", "ARM64", "bin", "clang-format.exe")] = true
	a.mode = ModeCheck // check closes nothing at all; the warning comes from the remote stage
	if err := a.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, a.output())
	}
	a.mustContain("ARM64 build tools: clang.exe not found", "half-installed", "winget install LLVM.LLVM", "installs nothing")
}

func TestARM64WarnLetsTheRunGoOn(t *testing.T) {
	a := armTest(t)
	a.con.Keys = []platform.Key{key('y')}
	if err := a.run(); err != nil || a.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, a.updates, a.output())
	}
	a.mustContain("clang.exe not found")
	// the warning is in the pre-flight, before the close: the first prompt follows it
	out := a.output()
	if strings.Index(out, "clang.exe not found") > strings.Index(out, "Hermes will close for the update") {
		t.Error("the warning came after the confirmation")
	}
}

func TestARM64StopModeStopsBeforeAnythingChanges(t *testing.T) {
	a := armTest(t)
	a.cfg.Preflight.ARM64Toolchain = config.PreflightStop
	a.con.Keys = []platform.Key{key('y')}
	err := a.run()
	stoppedBeforeAnythingChanged(t, a.harness, err)
	a.mustContain("clang.exe not found", "preflight.arm64_toolchain: stop")
}

func TestARM64NoVisualStudioToolsIsReported(t *testing.T) {
	a := armTest(t)
	a.vswOK = execx.Result{Output: "\r\n"}
	a.files[filepath.Join("pf", "LLVM", "bin", "clang.exe")] = true
	a.mode = ModeCheck
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	a.mustContain("no Visual Studio with the ARM64 C++ tools found")
}

func TestARM64HealthyToolsSayNothing(t *testing.T) {
	a := armTest(t)
	a.files[filepath.Join("pf", "LLVM", "bin", "clang.exe")] = true
	a.mode = ModeCheck
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.output(), "ARM64") || a.vswhereCalls() != 1 {
		t.Errorf("vswhere calls %d, output:\n%s", a.vswhereCalls(), a.output())
	}
}

func TestARM64ProbeRunsOnlyWhenItCanMatter(t *testing.T) {
	cases := map[string]func(*armHarness){
		"python dependencies unchanged": func(a *armHarness) { no := false; a.repo.diffPy = &no },
		"not an ARM64 host": func(a *armHarness) {
			a.install.Python = filepath.Join(tHome, "tools", "python-3.14.7-win32-x64", "python.exe")
		},
		"switched off": func(a *armHarness) { a.cfg.Preflight.ARM64Toolchain = config.PreflightOff },
		"nothing to update": func(a *armHarness) {
			a.mainCheck = "Already up to date"
		},
	}
	for name, mutate := range cases {
		a := armTest(t)
		mutate(a)
		a.mode = ModeCheck
		if err := a.run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if a.vswhereCalls() != 0 || strings.Contains(a.output(), "ARM64") {
			t.Errorf("%s: vswhere calls %d, output:\n%s", name, a.vswhereCalls(), a.output())
		}
	}
}

// Unknown dependency change counts as "may change" (the spec: PyDeps != false).
func TestARM64UnknownDependencyChangeStillProbes(t *testing.T) {
	a := armTest(t)
	no := false
	a.repo.diffNPM, a.repo.diffPy = &no, nil
	a.mode = ModeCheck
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	if a.vswhereCalls() != 1 || !strings.Contains(a.output(), "clang.exe not found") {
		t.Errorf("vswhere calls %d, output:\n%s", a.vswhereCalls(), a.output())
	}
}

// A vswhere that cannot answer is unknown, never "missing".
func TestARM64VswhereFailureIsSilent(t *testing.T) {
	a := armTest(t)
	a.vswOK = execx.Result{Code: 1, Output: "boom"}
	a.cfg.Preflight.ARM64Toolchain = config.PreflightStop
	a.mode = ModeCheck
	if err := a.run(); err != nil {
		t.Fatalf("an undecided probe must not stop: %v", err)
	}
	if strings.Contains(a.output(), "ARM64") {
		t.Errorf("output mentions ARM64:\n%s", a.output())
	}
}

// ---- confirmation: ten minutes, and a second look after a long wait ----

// scriptConsole delivers scripted keys and moves the virtual clock the way a
// slow user would. A step with timeout=true makes the prompt time out.
type scriptStep struct {
	key     rune
	after   time.Duration // the clock moves this much before the key arrives
	timeout bool
	act     func() // runs when the step is delivered (the world changes while the user is away)
}

type scriptConsole struct {
	*platform.FakeConsole
	t     *testing.T
	clock *testutil.Clock
	steps []scriptStep
}

func (c *scriptConsole) ReadKey(ctx context.Context) (platform.Key, error) {
	if len(c.steps) == 0 {
		// a prompt nobody scripted: fail now instead of waiting for a timeout
		c.t.Errorf("the flow asked for a key that the test did not script")
		return platform.Key{}, errors.New("no scripted key left")
	}
	s := c.steps[0]
	c.steps = c.steps[1:]
	c.clock.Advance(s.after)
	if s.act != nil {
		s.act()
	}
	if s.timeout {
		return platform.Key{}, context.DeadlineExceeded
	}
	return platform.Key{Rune: s.key}, nil
}

func scripted(h *harness, steps ...scriptStep) {
	con := &scriptConsole{FakeConsole: h.con, t: h.t, clock: h.clock, steps: steps}
	h.onRun = func(r *Run) { r.UI = ui.NewPlain(&h.out, con) }
}

func prompts(h *harness) int { return strings.Count(h.output(), "Hermes will close for the update.") }

func TestConfirmPromptSaysTenMinutesByDefault(t *testing.T) {
	h := newHarness(t)
	scripted(h, scriptStep{key: 'y'})
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("auto-abort in 10 min")
}

// Under two minutes the prompt counts in seconds, as before.
func TestConfirmPromptSaysSecondsForAShortTimeout(t *testing.T) {
	h := newHarness(t)
	h.cfg.Tunables.ConfirmTimeoutS = 90
	scripted(h, scriptStep{key: 'y'})
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("auto-abort in 90s")
}

// idle before the prompt, busy again after a long wait: back to waiting, no
// close until idle, then asked again.
func TestLongConfirmWaitRechecksAndWaitsAgainIfBusy(t *testing.T) {
	h := newPreflightHarness(t)
	h.sessions.script = [][]hermes.Session{nil, busy("s1"), busy("s1"), nil}
	scripted(h,
		scriptStep{key: 'y', after: 5 * time.Minute}, // answered after five minutes
		scriptStep{timeout: true, after: 30 * time.Second},
		scriptStep{key: 'y'})
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	out := h.output()
	if prompts(h) != 2 || h.updates != 1 || h.sessions.calls != 4 {
		t.Errorf("prompts=%d updates=%d session reads=%d", prompts(h), h.updates, h.sessions.calls)
	}
	became := strings.Index(out, "became active while the confirmation waited")
	closing := strings.Index(out, "closing desktop app")
	second := strings.LastIndex(out, "Hermes will close for the update.")
	if became < 0 || closing < 0 || !(became < second && second < closing) {
		t.Errorf("order wrong (became=%d second prompt=%d closing=%d):\n%s", became, second, closing, out)
	}
	if calls := h.app.Calls(); len(calls) == 0 || calls[0] != "close 100" {
		t.Errorf("app calls = %v", calls)
	}
}

// An update that starts while the prompt waits takes Hermes' checkout lock:
// the answer after a long wait must stop (exit 4) before anything closes.
func TestLockTakenDuringALongConfirmStopsBeforeClosing(t *testing.T) {
	h := newPreflightHarness(t)
	probe := h.plat.Locks.(*platform.FakeLockProbe)
	h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
	scripted(h, scriptStep{key: 'y', after: 5 * time.Minute, act: func() { holdLock(h, lockRecordFixture) }})
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) || apperr.ExitCode(err) != apperr.PreflightStop {
		t.Fatalf("err = %v (exit %d), want a pre-flight stop\n%s", err, apperr.ExitCode(err), h.output())
	}
	if h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 || len(h.procs.Killed()) != 0 || h.gw.stops != 0 {
		t.Errorf("something changed: updates=%d app=%v marker=%+v killed=%v gw stops=%d", h.updates, h.app.Calls(), h.marker, h.procs.Killed(), h.gw.stops)
	}
	h.mustContain("An update is running (pid 4242", "checkout lock", "Nothing was closed")
	if got := probe.Asked(); len(got) != 2 {
		t.Errorf("lock probed %d times, want 2 (at the start and after the long wait)", len(got))
	}
}

// A free lock after the long wait costs one more probe and changes nothing.
func TestLockStillFreeAfterALongConfirmGoesOn(t *testing.T) {
	h := newPreflightHarness(t)
	scripted(h, scriptStep{key: 'y', after: 5 * time.Minute})
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if got := h.plat.Locks.(*platform.FakeLockProbe).Asked(); len(got) != 2 {
		t.Errorf("lock probed %d times, want 2", len(got))
	}
}

// A quick answer is not re-checked (same rule as the busy sessions).
func TestLockIsNotRecheckedAfterAShortConfirm(t *testing.T) {
	h := newPreflightHarness(t)
	scripted(h, scriptStep{key: 'y', after: 30 * time.Second, act: func() { holdLock(h, lockRecordFixture) }})
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
}

// The lock is re-probed right before the close stage whenever the last probe
// is more than a minute old, in every mode. F says "close despite active
// sessions", nothing about the lock.
func TestLockIsRecheckedAfterForceAndALongWait(t *testing.T) {
	h := newPreflightHarness(t)
	probe := h.plat.Locks.(*platform.FakeLockProbe)
	h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
	h.sessions.script = [][]hermes.Session{busy("s1")}
	scripted(h, scriptStep{key: 'f'}, scriptStep{key: 'y', after: 8 * time.Minute, act: func() { holdLock(h, lockRecordFixture) }})
	err := h.run()
	lockStoppedBeforeClosing(t, h, err)
	if got := probe.Asked(); len(got) != 2 {
		t.Errorf("lock probed %d times, want 2 (at the start and before closing)", len(got))
	}
}

// An unattended run (the scheduled-task case) can wait up to idle_wait_max_s
// for idle sessions; an update that starts meanwhile holds the lock.
func TestLockIsRecheckedAfterALongIdleWaitWhenUnattended(t *testing.T) {
	for _, flags := range [][]string{{"unattended"}, {"unattended", "yes"}} {
		t.Run(strings.Join(flags, "+"), func(t *testing.T) {
			h := newPreflightHarness(t)
			h.cfg.Tunables.IdlePollS = 150
			h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
			h.sessions.script = [][]hermes.Session{busy("s1"), busy("s1"), nil}
			h.sessions.onCall = func(n int) {
				if n == 2 {
					holdLock(h, lockRecordFixture)
				}
			}
			err := h.run(flags...)
			lockStoppedBeforeClosing(t, h, err)
			if got := h.plat.Locks.(*platform.FakeLockProbe).Asked(); len(got) != 2 {
				t.Errorf("lock probed %d times, want 2 (at the start and before closing)", len(got))
			}
		})
	}
}

// The age of the last probe counts, not how long the prompt waited: a quick Y
// right after a long idle wait is re-checked too.
func TestLockIsRecheckedAfterAQuickYFollowingALongIdleWait(t *testing.T) {
	h := newPreflightHarness(t)
	h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
	h.sessions.script = [][]hermes.Session{busy("s1"), nil}
	scripted(h,
		scriptStep{timeout: true, after: 5 * time.Minute, act: func() { holdLock(h, lockRecordFixture) }}, // the idle wait
		scriptStep{key: 'y'}) // answered at once
	err := h.run()
	lockStoppedBeforeClosing(t, h, err)
}

// With no answer from the probe (macOS/Linux, or a real failure) the run goes
// on, and "not probed" is logged once however many looks the run takes.
func TestLockNotProbedIsLoggedOncePerRun(t *testing.T) {
	h := newPreflightHarness(t)
	var logs strings.Builder
	h.logSink = &logs
	p := h.plat.Locks.(*platform.FakeLockProbe)
	p.Err = errors.New("lock probe: no answer")
	h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
	h.sessions.script = [][]hermes.Session{busy("s1"), nil}
	scripted(h,
		scriptStep{timeout: true, after: 5 * time.Minute}, // the idle wait
		scriptStep{key: 'y'})
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if n := len(p.Asked()); n < 2 {
		t.Fatalf("probed %d times: the test does not cover the re-check", n)
	}
	if n := strings.Count(logs.String(), "checkout lock: not probed"); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, logs.String())
	}
}

// A probe that fails at the re-check lets the run go on: nothing closed
// early, no stop, and the update runs.
func TestLockProbeErrorAtTheRecheckLetsTheRunGoOn(t *testing.T) {
	h := newPreflightHarness(t)
	p := h.plat.Locks.(*platform.FakeLockProbe)
	h.sessions.script = [][]hermes.Session{busy("s1"), nil}
	scripted(h,
		scriptStep{timeout: true, after: 5 * time.Minute, act: func() { // the probe fails from here on
			p.Info, p.Err = platform.LockInfo{State: platform.LockHeld, Record: []byte(lockRecordFixture)}, errors.New("lock probe: boom")
		}},
		scriptStep{key: 'y'})
	err := h.run()
	if err != nil || h.updates != 1 {
		t.Fatalf("a failed probe at the re-check must not stop: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if n := len(p.Asked()); n < 2 {
		t.Errorf("probed %d times: the re-check did not run", n)
	}
	if len(h.app.Calls()) == 0 {
		t.Errorf("the close stage did not run after the failed probe")
	}
}

// A short run probes the lock once, at the start, in every mode.
func TestLockIsProbedOnceOnAShortRun(t *testing.T) {
	for _, flags := range stopFlagSets {
		t.Run(strings.Join(flags, "+"), func(t *testing.T) {
			h := newPreflightHarness(t)
			if !slices.Contains(flags, "unattended") && !slices.Contains(flags, "yes") {
				h.con.Keys = []platform.Key{key('y')} // interactive: the prompt wants its Y
			}
			if err := h.run(flags...); err != nil || h.updates != 1 {
				t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
			}
			if got := h.plat.Locks.(*platform.FakeLockProbe).Asked(); len(got) != 1 {
				t.Errorf("lock probed %d times, want 1", len(got))
			}
		})
	}
}

// lockStoppedBeforeClosing: exit 4 for a lock taken meanwhile, with nothing
// closed or changed (the sessions were read, so this is not the pre-flight
// helper).
func lockStoppedBeforeClosing(t *testing.T, h *harness, err error) {
	t.Helper()
	if !errors.Is(err, apperr.ErrPreflightStop) || apperr.ExitCode(err) != apperr.PreflightStop {
		t.Fatalf("err = %v (exit %d), want a pre-flight stop\n%s", err, apperr.ExitCode(err), h.output())
	}
	if h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 || len(h.procs.Killed()) != 0 || h.gw.stops != 0 {
		t.Errorf("something changed: updates=%d app=%v marker=%+v killed=%v gw stops=%d", h.updates, h.app.Calls(), h.marker, h.procs.Killed(), h.gw.stops)
	}
	h.mustContain("An update is running (pid 4242", "checkout lock", "Nothing was closed")
}

func TestLongConfirmWaitBusyThenAbortClosesNothing(t *testing.T) {
	h := newPreflightHarness(t)
	h.sessions.script = [][]hermes.Session{nil, busy("s1")}
	scripted(h, scriptStep{key: 'y', after: 5 * time.Minute}, scriptStep{key: 'a'})
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) || h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 {
		t.Fatalf("err=%v updates=%d app=%v marker=%+v", err, h.updates, h.app.Calls(), h.marker)
	}
	h.mustContain("aborted by user")
}

func TestLongConfirmWaitStillIdleProceedsWithoutAskingTwice(t *testing.T) {
	h := newPreflightHarness(t)
	scripted(h, scriptStep{key: 'y', after: 5 * time.Minute})
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d", err, h.updates)
	}
	if prompts(h) != 1 || h.sessions.calls != 2 {
		t.Errorf("prompts=%d session reads=%d (want 1 prompt, the check before it and one after the long wait)", prompts(h), h.sessions.calls)
	}
}

// A quick answer needs no second look: no extra read.
func TestShortConfirmWaitDoesNotRecheck(t *testing.T) {
	h := newPreflightHarness(t)
	h.sessions.script = [][]hermes.Session{nil, busy("s1")}
	scripted(h, scriptStep{key: 'y', after: 45 * time.Second})
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if h.sessions.calls != 1 {
		t.Errorf("session reads = %d, want 1 (a 45 s answer is not rechecked)", h.sessions.calls)
	}
}

// F means "close anyway, I know": a long wait afterwards does not undo it.
func TestForcedCloseIsNotRecheckedAfterALongWait(t *testing.T) {
	h := newPreflightHarness(t)
	h.sessions.script = [][]hermes.Session{busy("s1")}
	scripted(h, scriptStep{key: 'f'}, scriptStep{key: 'y', after: 8 * time.Minute})
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if h.sessions.calls != 1 {
		t.Errorf("session reads = %d, want 1", h.sessions.calls)
	}
}

func TestYesNeverRechecks(t *testing.T) {
	h := newPreflightHarness(t)
	h.sessions.script = [][]hermes.Session{nil, busy("s1")}
	if err := h.run("yes"); err != nil || h.updates != 1 || h.sessions.calls != 1 {
		t.Fatalf("err=%v updates=%d reads=%d", err, h.updates, h.sessions.calls)
	}
}

// The second waiting episode gets its own idle budget: the first one and the
// ten-minute prompt must not use it up.
func TestSecondWaitAfterALongConfirmGetsAFreshIdleBudget(t *testing.T) {
	h := newPreflightHarness(t)
	h.cfg.Tunables.IdleWaitMaxS = 120
	h.sessions.script = [][]hermes.Session{busy("s1"), busy("s1"), nil, busy("s1"), busy("s1"), nil}
	scripted(h,
		scriptStep{timeout: true, after: 60 * time.Second}, // busy at 0 s and at 60 s
		scriptStep{timeout: true, after: 30 * time.Second}, // idle at 90 s
		scriptStep{key: 'y', after: 400 * time.Second},     // answer at 490 s: busy again
		scriptStep{timeout: true, after: 30 * time.Second}, // a fresh episode: 0 s into it
		scriptStep{key: 'y'})
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 1 || strings.Contains(h.output(), "sessions still active after") {
		t.Errorf("updates=%d\n%s", h.updates, h.output())
	}
}
