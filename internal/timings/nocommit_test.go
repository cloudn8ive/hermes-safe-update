package timings_test

import (
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// A repair, or an unfinished update finished, finds nothing to pull: its fetch
// and pull never ran and the rest of its steps ran under other conditions.
// Such a record must not teach the estimate of an update with new commits,
// but the pre-flight steps it measured are as good as any.
func TestRunWithoutNewCommitsDoesNotTeachTheUpdateSteps(t *testing.T) {
	noCommits := rec("update", bptr(true), &timings.Conditions{Commits: intp(0)}, "remote", 50, "gateway_stop", 300, "verify", 4)
	noCommits.Commits = intp(0)
	normal := rec("update", bptr(true), &timings.Conditions{Commits: intp(3)}, "remote", 30, "gateway_stop", 20, "verify", 6)
	normal.Commits = intp(3)
	e, _ := newUpdate([]timings.Record{normal, noCommits})
	near(t, "gateway_stop learns only from the run with commits", e.Estimate("gateway_stop"), 20)
	near(t, "verify learns only from the run with commits", e.Estimate("verify"), 6)
	near(t, "the pre-flight step still learns from both", e.Estimate("remote"), 40)
}

// The commit count may only be in the condition block (older writers).
func TestNoCommitsIsReadFromTheConditionToo(t *testing.T) {
	r := rec("update", bptr(true), &timings.Conditions{Commits: intp(0)}, "gateway_stop", 300, "verify", 4)
	e, _ := newUpdate([]timings.Record{r})
	near(t, "gateway_stop", e.Estimate("gateway_stop"), 23) // the default: nothing learned
}

// An unknown count (nil) is an ordinary update, as before.
func TestUnknownCommitCountStillTeaches(t *testing.T) {
	r := rec("update", bptr(true), nil, "gateway_stop", 300, "verify", 4)
	e, _ := newUpdate([]timings.Record{r})
	near(t, "gateway_stop", e.Estimate("gateway_stop"), 300)
}

func TestRoughNoteDoesNotCountRunsWithoutNewCommits(t *testing.T) {
	cur := timings.Conditions{Source: "mirror", Mirror: bptr(true), NPM: bptr(true), PyDeps: bptr(false)}
	mk := func(commits int) timings.Record {
		r := rec("update", bptr(true), &timings.Conditions{Source: "mirror", Mirror: bptr(true), NPM: bptr(true), PyDeps: bptr(false), Commits: intp(commits)},
			"fetch", 10, "frontend", 100, "desktop", 20, "verify", 5)
		r.Commits = intp(commits)
		return r
	}
	e, _ := newUpdate([]timings.Record{mk(0), mk(0), mk(0)})
	e.SetConditions(cur)
	e.ExpectSkip("pydeps", true)
	if got, want := e.RoughNote("close"), "rough: no earlier runs like this"; got != want {
		t.Errorf("three repairs taught the estimate of an update: %q, want %q", got, want)
	}
	e, _ = newUpdate([]timings.Record{mk(3), mk(3), mk(3)})
	e.SetConditions(cur)
	e.ExpectSkip("pydeps", true)
	if got := e.RoughNote("close"); got != "" {
		t.Errorf("three real updates are enough: %q", got)
	}
}

// The seconds a person spent answering a question are not the step's time.
func TestExcuseTakesAnsweringTimeOutOfTheActiveStep(t *testing.T) {
	e, clk := newUpdate(nil)
	start := clk.Now()
	e.Stage("remote")
	clk.Advance(10 * time.Minute)
	e.Excuse(7 * time.Minute)
	near(t, "elapsed", e.Elapsed("remote"), 180)
	e.Stage("procs")
	clk.Advance(2 * time.Second)
	e.Complete(bptr(true))
	r := e.Record(start)
	if v, _ := r.Steps.Get("remote"); v != 180 {
		t.Errorf("recorded remote = %v s, want 180", v)
	}
}

func TestExcuseNeverGoesPastNowAndIgnoresNothingToExcuse(t *testing.T) {
	e, clk := newUpdate(nil)
	e.Stage("remote")
	clk.Advance(5 * time.Second)
	e.Excuse(time.Hour) // more than the step has run
	near(t, "clamped to now", e.Elapsed("remote"), 0)
	clk.Advance(3 * time.Second)
	e.Excuse(0)
	e.Excuse(-time.Minute)
	near(t, "no-op", e.Elapsed("remote"), 3)
	e.Stage("procs")
	e.Excuse(time.Minute) // only the active step is touched
	near(t, "a finished step keeps its time", e.Elapsed("remote"), 3)
}

// The renderers tell the estimator when a question is on screen
// (SetWaiting); with ExcuseWaits on, that time is taken out of the step.
func TestWaitsAreExcusedOnlyWhenAsked(t *testing.T) {
	run := func(excuse bool) time.Duration {
		e, clk := newUpdate(nil)
		e.Stage("remote")
		clk.Advance(3 * time.Second)
		e.ExcuseWaits(excuse)
		e.SetWaiting(true)
		clk.Advance(5 * time.Minute)
		e.SetWaiting(true) // the prompt announces itself again: the wait began once
		clk.Advance(5 * time.Minute)
		e.SetWaiting(false)
		e.SetWaiting(false) // ... and ends twice: excused once
		return e.Elapsed("remote")
	}
	near(t, "excused", run(true), 3)
	near(t, "not excused (the close stage's own waits)", run(false), 603)
}
