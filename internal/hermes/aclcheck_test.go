package hermes

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Synthetic layout; relative names keep these tests OS-independent.
var (
	aHome     = filepath.Join("home")
	aCheckout = filepath.Join("home", "hermes-agent")
	aTools    = filepath.Join(aHome, "tools")
)

func tp(parts ...string) string { return filepath.Join(append([]string{aTools}, parts...)...) }

// aclWorld: tools\{python-x, node-y, facts.json}; python-x\{python.exe, Lib};
// node-y\{node.exe}.
func aclWorld() *platform.FakeAccess {
	return &platform.FakeAccess{
		Dirs: map[string][]string{
			aTools:         {"python-x", "node-y", "facts.json"},
			tp("python-x"): {"python.exe", "Lib"},
			tp("node-y"):   {"node.exe"},
		},
		Absent: map[string]bool{
			filepath.Join(aHome, "runtime"):                             true,
			filepath.Join(aHome, "node"):                                true,
			filepath.Join(aCheckout, "node_modules"):                    true,
			filepath.Join(aCheckout, "apps", "desktop", "node_modules"): true,
			filepath.Join(aCheckout, "apps", "desktop", "release"):      true,
		},
	}
}

func runACL(a *platform.FakeAccess, mutate func(*ACLChecker)) ACLReport {
	// No real 400 ms pause in a test that is not about it; the tests of the
	// pause set their own Sleep.
	c := ACLChecker{Access: a, Home: aHome, Checkout: aCheckout,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	if mutate != nil {
		mutate(&c)
	}
	return c.Run(context.Background())
}

func TestACLHealthyStoreHasNoFinding(t *testing.T) {
	rep := runACL(aclWorld(), nil)
	if len(rep.Findings()) != 0 || rep.Incomplete {
		t.Fatalf("healthy store: %+v", rep)
	}
	if rep.Opens == 0 {
		t.Error("nothing was probed")
	}
}

func TestACLReadDeniedUnderToolsIsStopWithTheQuotedRepair(t *testing.T) {
	a := aclWorld()
	a.Deny(platform.AccessRead, tp("python-x"))
	rep := runACL(a, nil)
	if len(rep.Stop) != 1 || len(rep.Warn) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	f := rep.Stop[0]
	if f.Path != tp("python-x") || f.Mask != platform.AccessRead || !f.InTools {
		t.Errorf("finding = %+v", f)
	}
	cmds := RepairCommands(f.Path)
	want := []string{`takeown /f "` + tp("python-x") + `" /r /d y`, `icacls "` + tp("python-x") + `" /reset /t /c /q`}
	if len(cmds) != 2 || cmds[0] != want[0] || cmds[1] != want[1] {
		t.Errorf("repair = %q", cmds)
	}
	// a folder that cannot be read is not walked into
	for _, p := range a.ProbedPaths() {
		if strings.HasPrefix(p, tp("python-x")+string(filepath.Separator)) {
			t.Errorf("walked into an unreadable folder: %s", p)
		}
	}
}

// One level below a tools\ entry counts as "under tools\" too.
func TestACLReadDeniedOneLevelDeepIsStop(t *testing.T) {
	a := aclWorld()
	a.Deny(platform.AccessRead, tp("python-x", "Lib"))
	rep := runACL(a, nil)
	if len(rep.Stop) != 1 || rep.Stop[0].Path != tp("python-x", "Lib") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestACLModifyOnlyDenialIsWarn(t *testing.T) {
	a := aclWorld()
	a.Deny(platform.AccessModify, tp("node-y"))
	rep := runACL(a, nil)
	if len(rep.Stop) != 0 || len(rep.Warn) != 1 || rep.Warn[0].Mask != platform.AccessModify || rep.Warn[0].Path != tp("node-y") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestACLReadDeniedOutsideToolsIsWarn(t *testing.T) {
	a := aclWorld()
	a.Deny(platform.AccessRead, filepath.Join(aHome, "environments"))
	a.Deny(platform.AccessRead, filepath.Join(aCheckout, ".git"))
	rep := runACL(a, nil)
	if len(rep.Stop) != 0 || len(rep.Warn) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	for _, f := range rep.Warn {
		if f.InTools || f.Mask != platform.AccessRead {
			t.Errorf("finding = %+v", f)
		}
	}
}

// A sharing violation says nothing about rights; a missing path, a junction
// and an unexplained open failure are not findings either.
func TestACLNeverFlagsBusyMissingReparseOrUnknown(t *testing.T) {
	a := aclWorld()
	a.Set(platform.AccessRead, tp("python-x"), platform.AccessResult{Busy: true})
	a.Set(platform.AccessModify, tp("node-y"), platform.AccessResult{Busy: true})
	a.Set(platform.AccessRead, tp("facts.json"), platform.AccessResult{Reparse: true})
	a.Set(platform.AccessRead, filepath.Join(aHome, "environments"), platform.AccessResult{Err: platform.ErrNotSupported})
	a.Absent[tp("node-y")] = true
	rep := runACL(a, nil)
	if got := rep.Findings(); len(got) != 0 {
		t.Fatalf("findings = %+v", got)
	}
}

// A reparse point is neither judged nor followed.
func TestACLDoesNotFollowAJunction(t *testing.T) {
	a := aclWorld()
	a.Set(platform.AccessRead, tp("python-x"), platform.AccessResult{Reparse: true, Dir: true})
	rep := runACL(a, nil)
	if len(rep.Findings()) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, p := range a.ProbedPaths() {
		if strings.HasPrefix(p, tp("python-x")+string(filepath.Separator)) {
			t.Errorf("followed a junction into %s", p)
		}
	}
}

func TestACLProbeCapIsHonoured(t *testing.T) {
	a := aclWorld()
	rep := runACL(a, func(c *ACLChecker) { c.MaxOpens = 5 })
	if rep.Opens > 5 || !rep.Incomplete {
		t.Fatalf("opens = %d incomplete = %v", rep.Opens, rep.Incomplete)
	}
	if got := len(a.Probes()); got > 5 {
		t.Errorf("%d opens made", got)
	}
}

func TestACLTimeBudgetIsHonoured(t *testing.T) {
	a := aclWorld()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rep := runACL(a, func(c *ACLChecker) {
		c.Budget = time.Second
		c.Now = func() time.Time { now = now.Add(400 * time.Millisecond); return now }
	})
	if !rep.Incomplete || rep.Opens >= 8 {
		t.Fatalf("opens = %d incomplete = %v", rep.Opens, rep.Incomplete)
	}
}

// Grandchildren are read-only probes (the opens are for refusals that break
// the update); the entries themselves are read and modify.
func TestACLProbeMasksPerLevel(t *testing.T) {
	a := aclWorld()
	runACL(a, nil)
	probes := strings.Join(a.Probes(), "\n")
	if !strings.Contains(probes, "modify "+tp("python-x")+"\n") && !strings.HasSuffix(probes, "modify "+tp("python-x")) {
		t.Errorf("tools entry not probed for modify:\n%s", probes)
	}
	if strings.Contains(probes, "modify "+tp("python-x", "Lib")) {
		t.Errorf("a grandchild must be probed for read only:\n%s", probes)
	}
	if !strings.Contains(probes, "read "+tp("python-x", "Lib")) {
		t.Errorf("a grandchild must be probed for read:\n%s", probes)
	}
}

func TestACLNothingToProbe(t *testing.T) {
	if rep := (ACLChecker{}).Run(context.Background()); rep.Opens != 0 || len(rep.Findings()) != 0 {
		t.Errorf("%+v", rep)
	}
}

// A refusal that was only a passing state (a file that is delete-pending for
// a moment, a scanner holding it) is opened once more, after one pause, and
// not reported when the second open works.
func TestACLTransientDenialUnderToolsIsNotAFinding(t *testing.T) {
	a := aclWorld()
	a.DenyOnce(platform.AccessRead, tp("python-x"))
	var slept []time.Duration
	rep := runACL(a, func(c *ACLChecker) {
		c.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	})
	if len(rep.Findings()) != 0 {
		t.Fatalf("a denial that cleared was reported: %+v", rep)
	}
	if len(slept) != 1 || slept[0] != 400*time.Millisecond {
		t.Errorf("pauses = %v, want one of 400ms", slept)
	}
}

// A refusal that stays is a finding, and all refused entries share ONE pause.
func TestACLDenialThatStaysIsStillAStopAfterOnePause(t *testing.T) {
	a := aclWorld()
	a.Deny(platform.AccessRead, tp("python-x"))
	a.Deny(platform.AccessRead, tp("node-y"))
	a.DenyOnce(platform.AccessRead, tp("facts.json")) // this one clears
	pauses := 0
	rep := runACL(a, func(c *ACLChecker) {
		c.Sleep = func(context.Context, time.Duration) error { pauses++; return nil }
	})
	if len(rep.Stop) != 2 || rep.Stop[0].Path != tp("node-y") || rep.Stop[1].Path != tp("python-x") {
		t.Fatalf("stop = %+v", rep.Stop)
	}
	if pauses != 1 {
		t.Errorf("pauses = %d, want 1 for all refused entries", pauses)
	}
}

// Nothing refused: no pause at all (a healthy store stays fast).
func TestACLHealthyStoreDoesNotPause(t *testing.T) {
	pauses := 0
	runACL(aclWorld(), func(c *ACLChecker) {
		c.Sleep = func(context.Context, time.Duration) error { pauses++; return nil }
	})
	if pauses != 0 {
		t.Errorf("pauses = %d", pauses)
	}
}

// Ctrl-C ends the walk: no new open once the context is done.
func TestACLWalkStopsOpeningAfterCancel(t *testing.T) {
	a := aclWorld()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := ACLChecker{Access: a, Home: aHome, Checkout: aCheckout}.Run(ctx)
	if got := len(a.Probes()); got != 0 || rep.Opens != 0 || !rep.Incomplete {
		t.Fatalf("opens=%d probes=%d incomplete=%v after cancel", rep.Opens, got, rep.Incomplete)
	}
}

func TestACLWalkStopsMidwayWhenCancelled(t *testing.T) {
	a := aclWorld()
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	rep := ACLChecker{Access: &cancelAfter{FakeAccess: a, n: 3, cancel: cancel, count: &n}, Home: aHome, Checkout: aCheckout}.Run(ctx)
	if n != 3 || !rep.Incomplete {
		t.Fatalf("probes after cancel: n=%d incomplete=%v", n, rep.Incomplete)
	}
}

// cancelAfter cancels the context on the n-th probe.
type cancelAfter struct {
	*platform.FakeAccess
	n      int
	cancel func()
	count  *int
}

func (c *cancelAfter) Probe(path string, mask platform.AccessMask) platform.AccessResult {
	*c.count++
	if *c.count == c.n {
		c.cancel()
	}
	return c.FakeAccess.Probe(path, mask)
}
