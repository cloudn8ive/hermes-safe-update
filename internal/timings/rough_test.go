package timings_test

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// npmRun is a full, finished update whose Node packages did (or did not) change.
func npmRun(npm bool) timings.Record {
	return rec("update", bptr(true), &timings.Conditions{Source: "mirror", Mirror: bptr(true), NPM: bptr(npm), PyDeps: bptr(false)},
		"fetch", 10, "frontend", 100, "desktop", 20, "verify", 5)
}

func TestRoughNoteCountsRunsWithTheSameConditions(t *testing.T) {
	cur := timings.Conditions{Source: "mirror", Mirror: bptr(true), NPM: bptr(true), PyDeps: bptr(false)}
	other := []timings.Record{npmRun(false), npmRun(false), npmRun(false), npmRun(false)} // plenty of history, wrong kind of run
	cases := []struct {
		name string
		hist []timings.Record
		want string
	}{
		{"no history at all", nil, "rough: no earlier runs like this; uses defaults"},
		{"history but none like this", other, "rough: no earlier runs like this"},
		{"one like this", append(append([]timings.Record{}, other...), npmRun(true)), "rough: 1 earlier run like this"},
		{"two like this", append(append([]timings.Record{}, other...), npmRun(true), npmRun(true)), "rough: 2 earlier runs like this"},
		{"three like this", append(append([]timings.Record{}, other...), npmRun(true), npmRun(true), npmRun(true)), ""},
		{"more than three", []timings.Record{npmRun(true), npmRun(true), npmRun(true), npmRun(true), npmRun(true)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := newUpdate(c.hist)
			e.SetConditions(cur)
			e.ExpectSkip("pydeps", true)
			if got := e.RoughNote("close"); got != c.want {
				t.Errorf("RoughNote(close) = %q, want %q", got, c.want)
			}
			if got := e.RoughNote(""); got != c.want {
				t.Errorf("RoughNote() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRoughNoteIgnoresRunsThatCannotTeachTheEstimate(t *testing.T) {
	cur := timings.Conditions{Source: "mirror", Mirror: bptr(true), NPM: bptr(true), PyDeps: bptr(false)}
	h := []timings.Record{
		rec("check", bptr(true), &cur, "remote", 5, "procs", 1),                     // other kind
		rec("update", bptr(true), &cur, "local", 2, "remote", 40),                   // nothing-to-update run: not full
		rec("update", bptr(true), nil, "fetch", 10, "frontend", 100, "desktop", 20), // no conditions recorded
	}
	e, _ := newUpdate(h)
	e.SetConditions(cur)
	e.ExpectSkip("pydeps", true)
	if got, want := e.RoughNote("close"), "rough: no earlier runs like this"; got != want {
		t.Errorf("RoughNote = %q, want %q", got, want)
	}
}

func TestRoughNoteIgnoresConditionsOfSkippedSteps(t *testing.T) {
	// pydeps is unchanged, so its step is skipped and a run with other pydeps still counts
	cur := timings.Conditions{Source: "mirror", Mirror: bptr(true), NPM: bptr(true), PyDeps: bptr(false)}
	mk := func() timings.Record {
		return rec("update", bptr(true), &timings.Conditions{Source: "mirror", Mirror: bptr(true), NPM: bptr(true), PyDeps: bptr(true)},
			"fetch", 10, "frontend", 100, "desktop", 20)
	}
	e, _ := newUpdate([]timings.Record{mk(), mk(), mk()})
	e.SetConditions(cur)
	e.ExpectSkip("pydeps", true)
	if got := e.RoughNote("close"); got != "" {
		t.Errorf("RoughNote = %q, want none", got)
	}
}

func TestRoughNoteOnlyForUpdateRuns(t *testing.T) {
	tun := defaults()
	e := timings.NewEstimator(timings.KindCheck, timings.CheckSteps(tun.StepSeconds), nil, tun, nil)
	if got := e.RoughNote(""); got != "" {
		t.Errorf("RoughNote for a check = %q, want none", got)
	}
}
