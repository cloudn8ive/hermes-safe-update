package timings_test

import (
	"math"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)

func defaults() config.Tunables { return config.Defaults().Tunables }

func newUpdate(hist []timings.Record) (*timings.Estimator, *testutil.Clock) {
	clk := testutil.NewClock(t0)
	tun := defaults()
	return timings.NewEstimator(timings.KindUpdate, timings.UpdateSteps(tun.StepSeconds), hist, tun, clk), clk
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func near(t *testing.T, name string, got time.Duration, want float64) {
	t.Helper()
	if math.Abs(got.Seconds()-want) > 0.001 {
		t.Errorf("%s = %.4fs, want %.4fs", name, got.Seconds(), want)
	}
}

func bptr(b bool) *bool { return &b }

// rec builds a record; steps alternate key, seconds.
func rec(kind string, ok *bool, cond *timings.Conditions, kv ...any) timings.Record {
	r := timings.Record{Kind: kind, OK: ok, Cond: cond}
	for i := 0; i < len(kv); i += 2 {
		r.Steps = append(r.Steps, timings.StepSeconds{Key: kv[i].(string), Seconds: float64(kv[i+1].(int))})
	}
	return r
}

func TestDefaultsSumAndPlannedTotal(t *testing.T) {
	e, _ := newUpdate(nil)
	// Go step list: Python's 597 s - tile 8 + settings 10 + hooks 8.
	near(t, "planned total", e.PlannedTotal(""), 607)
	near(t, "from close", e.PlannedTotal("close"), 528)
	e.ExpectSkip("pydeps", true)
	near(t, "from close, pydeps skipped", e.PlannedTotal("close"), 464)
	near(t, "unknown key = whole list", e.PlannedTotal("nope"), 543)
}

func TestEstimateIsMeanOfLastFiveMatchingRuns(t *testing.T) {
	var hist []timings.Record
	for _, v := range []int{10, 100, 100, 100, 100, 100, 200} {
		hist = append(hist, rec("update", bptr(true), nil, "remote", v, "desktop", 1))
	}
	e, _ := newUpdate(hist)
	// last five = 100,100,100,100,200 -> mean 120 (not the median 100)
	near(t, "remote", e.Estimate("remote"), 120)
	near(t, "step without history = default", e.Estimate("pull"), 90)
}

func TestNothingToUpdateRunsOnlyTeachPreflightSteps(t *testing.T) {
	// not full: no desktop/verify step
	h := []timings.Record{rec("update", bptr(true), nil, "local", 2, "remote", 40, "pull", 5)}
	e, _ := newUpdate(h)
	near(t, "remote learns", e.Estimate("remote"), 40)
	near(t, "pull does not", e.Estimate("pull"), 90)
}

func TestFailedRunsLastStepWasCutShort(t *testing.T) {
	h := []timings.Record{rec("update", bptr(false), nil, "remote", 40, "desktop", 3, "package", 9)}
	e, _ := newUpdate(h)
	near(t, "earlier step counts", e.Estimate("desktop"), 3)
	near(t, "cut-short step ignored", e.Estimate("package"), 22)
	// ok=nil (unknown) also counts as not ok
	h = []timings.Record{rec("update", nil, nil, "desktop", 3, "package", 9)}
	e, _ = newUpdate(h)
	near(t, "ok=nil last step ignored", e.Estimate("package"), 22)
}

func TestKindMustMatch(t *testing.T) {
	h := []timings.Record{rec("check", bptr(true), nil, "remote", 5, "procs", 1)}
	e, _ := newUpdate(h)
	near(t, "check history ignored by update", e.Estimate("remote"), 75)
}

func TestConditionPools(t *testing.T) {
	mir := func(b bool) *timings.Conditions { return &timings.Conditions{Mirror: bptr(b)} }
	full := func(c *timings.Conditions, fetch int) timings.Record {
		return rec("update", bptr(true), c, "fetch", fetch, "desktop", 1)
	}
	t.Run("same condition wins", func(t *testing.T) {
		e, _ := newUpdate([]timings.Record{full(mir(false), 100), full(mir(true), 20)})
		e.SetConditions(timings.Conditions{Mirror: bptr(true)})
		near(t, "fetch", e.Estimate("fetch"), 20)
	})
	t.Run("fast value without own runs uses the 3 fastest", func(t *testing.T) {
		hist := []timings.Record{full(mir(false), 100), full(mir(false), 10), full(mir(false), 20), full(mir(false), 30), full(mir(false), 200)}
		e, _ := newUpdate(hist)
		e.SetConditions(timings.Conditions{Mirror: bptr(true)})
		near(t, "fetch", e.Estimate("fetch"), 20) // mean(10,20,30)
	})
	t.Run("non-fast value falls back to runs that never recorded it", func(t *testing.T) {
		hist := []timings.Record{full(mir(true), 5), full(nil, 60), full(&timings.Conditions{Source: "git"}, 80)}
		e, _ := newUpdate(hist)
		e.SetConditions(timings.Conditions{Mirror: bptr(false)})
		near(t, "fetch", e.Estimate("fetch"), 70) // unknown pool = records without mirror: 60, 80
	})
	t.Run("unknown value uses all runs", func(t *testing.T) {
		e, _ := newUpdate([]timings.Record{full(mir(true), 10), full(mir(false), 30)})
		near(t, "fetch", e.Estimate("fetch"), 20)
	})
	t.Run("source api is fast", func(t *testing.T) {
		mk := func(src string, v int) timings.Record {
			return rec("update", bptr(true), &timings.Conditions{Source: src}, "remote", v, "desktop", 1)
		}
		e, _ := newUpdate([]timings.Record{mk("git", 90), mk("git", 30), mk("git", 60), mk("git", 120)})
		e.SetConditions(timings.Conditions{Source: "api"})
		near(t, "remote", e.Estimate("remote"), 60) // mean(30,60,90)
	})
}

func TestStageAndComplete(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote")
	if e.State("local") != timings.Skipped || e.State("remote") != timings.Active || e.State("procs") != timings.Pending {
		t.Fatalf("states after Stage(remote): local=%v remote=%v procs=%v", e.State("local"), e.State("remote"), e.State("procs"))
	}
	clk.Advance(10 * time.Second)
	e.Stage("procs")
	if e.State("remote") != timings.Done {
		t.Errorf("remote should be done, is %v", e.State("remote"))
	}
	e.Stage("remote") // done: ignored
	e.Stage("bogus")  // unknown: ignored
	if e.State("procs") != timings.Active {
		t.Errorf("procs = %v", e.State("procs"))
	}
	e.Complete(bptr(true))
	if e.State("procs") != timings.Done || e.State("desktop") != timings.Skipped {
		t.Errorf("after Complete(true): procs=%v desktop=%v", e.State("procs"), e.State("desktop"))
	}
	if e.Fraction() != 1 {
		t.Errorf("Fraction after ok = %v", e.Fraction())
	}
	if _, ok := e.FinishETA(); ok {
		t.Error("no ETA after the run")
	}
	// failure leaves pending steps pending
	e, _ = newUpdate(nil)
	e.Stage("remote")
	e.Complete(bptr(false))
	if e.State("desktop") != timings.Pending || e.State("remote") != timings.Done {
		t.Errorf("after Complete(false): remote=%v desktop=%v", e.State("remote"), e.State("desktop"))
	}
	done, ok := e.Result()
	if !done || ok == nil || *ok {
		t.Errorf("Result = %v %v", done, ok)
	}
}

func TestRemainingOverrun(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote") // local skipped
	near(t, "start of remote", e.Remaining(), 606)
	clk.Advance(10 * time.Second)
	near(t, "10 s into remote", e.Remaining(), 596)
	clk.Advance(90 * time.Second) // 100 s: past the usual 75 s
	// overrun: max(7.5, 0.5*100) = 50; network factor 1+0.15*(100/75-1) = 1.05 on fetch+pull+finalize (112 s);
	// the other pending steps (419 s) are unscaled (machine class has no votes yet).
	near(t, "100 s into remote", e.Remaining(), 50+112*1.05+419)
}

func TestOverrunFloorBeforeUsualTime(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote")
	clk.Advance(74 * time.Second) // 1 s to go; floor = min(15, 7.5) = 7.5
	near(t, "floor", e.Remaining(), 606-75+7.5)
}

func TestFractionFillsAtExpectedRateHoldsAt95AndNeverDecreases(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote")
	if f := e.Fraction(); f != 0 {
		t.Errorf("fraction at start = %v", f)
	}
	clk.Advance(37500 * time.Millisecond)
	if f, want := e.Fraction(), 37.5/606; math.Abs(f-want) > 1e-9 {
		t.Errorf("fraction = %v, want %v", f, want)
	}
	clk.Advance(62500 * time.Millisecond) // 100 s: done holds at 0.95*75; pending network stretched by 1.05
	if f, want := e.Fraction(), 71.25/611.6; math.Abs(f-want) > 1e-9 {
		t.Errorf("held fraction = %v, want %v", f, want)
	}
}

func TestFractionNeverMovesBackwardsWhenEstimatesGrow(t *testing.T) {
	hist := []timings.Record{
		rec("update", bptr(true), &timings.Conditions{Source: "git"}, "remote", 75, "desktop", 1),
		rec("update", bptr(true), &timings.Conditions{Source: "api"}, "remote", 2000, "desktop", 1),
	}
	e, clk := newUpdate(hist)
	e.Stage("remote")
	clk.Advance(500 * time.Second)
	before := e.Fraction()
	e.SetConditions(timings.Conditions{Source: "api"}) // remote estimate jumps from 1037.5 to 2000
	if e.Estimate("remote") <= secs(1037.5) {
		t.Fatalf("scenario broken: estimate = %v", e.Estimate("remote"))
	}
	if after := e.Fraction(); after < before {
		t.Errorf("bar moved back: %v -> %v", before, after)
	}
}

func TestFractionCappedBelowOneUntilComplete(t *testing.T) {
	e, clk := newUpdate(nil)
	for _, k := range []string{"local", "remote", "procs", "close", "gateway_stop", "fetch", "pull", "pydeps", "frontend", "desktop", "package", "finalize", "restart", "verify", "settings", "relaunch", "cleanup", "cua", "hooks"} {
		e.Stage(k)
		clk.Advance(time.Second)
	}
	if f := e.Fraction(); f > 0.99 {
		t.Errorf("fraction = %v, want <= 0.99", f)
	}
}

func TestExpectSkipLeavesStepOutOfTheMath(t *testing.T) {
	e, _ := newUpdate(nil)
	e.Stage("remote")
	e.ExpectSkip("pydeps", true)
	near(t, "remaining", e.Remaining(), 606-64)
}

func TestFinishETA(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote")
	eta, ok := e.FinishETA()
	if !ok || !eta.Equal(clk.Now().Add(secs(606))) {
		t.Errorf("eta = %v %v", eta, ok)
	}
	e.SetWaiting(true)
	if _, ok := e.FinishETA(); ok {
		t.Error("no ETA while waiting for the user")
	}
}

func TestClassFactorFromFinishedSteps(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote")
	clk.Advance(150 * time.Second) // twice the usual 75 s
	e.Stage("procs")               // remote done: ratio 2 -> factor 1 + 0.15 = 1.15
	e.Stage("fetch")               // procs done
	// fetch active (el 0, est 10): 10; pending network: pull 90 + finalize 12 = 102 * 1.15; fetch itself is not pending
	// pending others: close 8, gateway_stop 23 (wait is 0) ... see the list in TestRemainingOverrun: 419 - procs 3 = 416,
	// minus nothing else; close 8 is skipped? close was earlier than fetch and never staged -> skipped.
	skipped := 8.0 + 23.0 // close, gateway_stop were skipped by Stage("fetch")
	near(t, "remaining", e.Remaining(), 10+102*1.15+(416-skipped))
}

func TestFactorIsClamped(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote")
	clk.Advance(100000 * time.Second) // ratio ~1333 -> factor clamped at 2.5
	e.Stage("procs")
	e.Stage("fetch")
	// fetch 10 + (pull 90 + finalize 12)*2.5 + other pending (close, gateway_stop skipped): 419-3-8-23 = 385
	near(t, "remaining", e.Remaining(), 10+102*2.5+385)
}

func TestRecordHoldsDoneEstimatedStepsInOrder(t *testing.T) {
	e, clk := newUpdate(nil)
	e.SetConditions(timings.Conditions{Source: "api", NPM: bptr(false)})
	e.Stage("local")
	clk.Advance(1200 * time.Millisecond)
	e.Stage("remote")
	clk.Advance(30 * time.Second)
	e.Stage("wait") // default 0: shown, never recorded
	clk.Advance(5 * time.Second)
	e.Stage("close")
	clk.Advance(2 * time.Second)
	e.Complete(bptr(true))
	r := e.Record(t0)
	if r == nil {
		t.Fatal("nil record")
	}
	if r.Kind != "update" || r.OK == nil || !*r.OK || r.Started != "2026-10-01T09:00:00" {
		t.Errorf("record header: %+v", r)
	}
	if math.Abs(r.Total-38.2) > 0.01 {
		t.Errorf("total = %v", r.Total)
	}
	want := timings.OrderedSeconds{{Key: "local", Seconds: 1.2}, {Key: "remote", Seconds: 30}, {Key: "close", Seconds: 2}}
	if len(r.Steps) != len(want) {
		t.Fatalf("steps = %+v", r.Steps)
	}
	for i := range want {
		if r.Steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, r.Steps[i], want[i])
		}
	}
	if r.Cond == nil || r.Cond.Source != "api" || r.Cond.NPM == nil || *r.Cond.NPM {
		t.Errorf("cond = %+v", r.Cond)
	}
}

func TestRecordNilWhenNothingFinished(t *testing.T) {
	e, _ := newUpdate(nil)
	if e.Record(t0) != nil {
		t.Error("want nil record")
	}
}

func TestCheckKindUsesCheckHistoryOnly(t *testing.T) {
	clk := testutil.NewClock(t0)
	tun := defaults()
	hist := []timings.Record{
		rec("check", bptr(true), nil, "local", 2, "remote", 11, "procs", 4, "sessions", 1, "gc_preview", 3, "cua_status", 1),
		rec("update", bptr(true), nil, "local", 9, "remote", 99, "desktop", 1),
	}
	e := timings.NewEstimator(timings.KindCheck, timings.CheckSteps(tun.StepSeconds), hist, tun, clk)
	near(t, "remote", e.Estimate("remote"), 11)
	near(t, "planned", e.PlannedTotal(""), 2+11+4+1+3+1)
}
