package timings_test

import (
	"math"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// Parity with the Python updater (task C3): the estimator is fed the same
// history and the same conditions as hermes_update_ui.UpdateUI and must give
// the same per-step estimates, planned totals, remaining time and bar
// fraction. The Go step lists differ on purpose from the Python ones
// (D-T1-7: "tile" removed, "settings" and "hooks" added), so the comparison
// runs on the Python step list (rebuilt from the fixture, with the Cond/Class
// tags of the Go step of the same key), and the shared keys of the real Go
// lists are compared separately.

type pyScenario struct {
	Name         string
	Kind         string
	Cond         pyCond
	Est          map[string]float64
	PlannedClose float64 `json:"planned_close"`
	PlannedAll   float64 `json:"planned_all"`
	MinutesText  string  `json:"minutes_text"`
}

type pyCond struct {
	Source  string `json:"source"`
	Mirror  *bool  `json:"mirror"`
	NPM     *bool  `json:"npm"`
	PyDeps  *bool  `json:"pydeps"`
	Commits *int   `json:"commits"`
}

func (c pyCond) cond() timings.Conditions {
	return timings.Conditions{Source: c.Source, Mirror: c.Mirror, NPM: c.NPM, PyDeps: c.PyDeps, Commits: c.Commits}
}

type pyEstimator struct {
	History     []timings.Record
	Scenarios   []pyScenario
	PythonSteps struct {
		Update [][]any `json:"update"`
		Check  [][]any `json:"check"`
	} `json:"python_steps"`
}

// pySteps rebuilds the Python step list; Cond/Class come from the Go list.
func pySteps(raw [][]any, goSteps []timings.Step) []timings.Step {
	tags := map[string]timings.Step{}
	for _, s := range goSteps {
		tags[s.Key] = s
	}
	var out []timings.Step
	for _, r := range raw {
		key := r[0].(string)
		g := tags[key]
		out = append(out, timings.Step{Key: key, Group: r[1].(string), Label: r[2].(string), DefaultS: int(r[3].(float64)), Cond: g.Cond, Class: g.Class})
	}
	return out
}

func loadEstimator(t *testing.T) pyEstimator {
	var f pyEstimator
	key := "estimator"
	if testutil.ParityFixtureHas("estimator_real") {
		key = "estimator_real"
	}
	testutil.ParityFixture(t, key, &f)
	return f
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 0.0015 }

func TestParityEstimates(t *testing.T) {
	f := loadEstimator(t)
	tun := config.Defaults().Tunables
	goUpdate := timings.UpdateSteps(tun.StepSeconds)
	goCheck := timings.CheckSteps(tun.StepSeconds)
	for _, sc := range f.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			kind, raw, goSteps := timings.KindUpdate, f.PythonSteps.Update, goUpdate
			if sc.Kind == "check" {
				kind, raw, goSteps = timings.KindCheck, f.PythonSteps.Check, goCheck
			}
			clk := testutil.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local))
			steps := pySteps(raw, goSteps)
			e := timings.NewEstimator(kind, steps, f.History, tun, clk)
			e.SetConditions(sc.Cond.cond())
			if sc.Cond.PyDeps != nil {
				e.ExpectSkip("pydeps", !*sc.Cond.PyDeps)
			}
			for _, s := range steps {
				if got := e.Estimate(s.Key).Seconds(); !closeTo(got, sc.Est[s.Key]) {
					t.Errorf("estimate %-12s Go %.4f, Python %.4f", s.Key, got, sc.Est[s.Key])
				}
			}
			if sc.Kind == "update" {
				if got := e.PlannedTotal("close").Seconds(); !closeTo(got, sc.PlannedClose) {
					t.Errorf("planned_total(close) Go %.4f, Python %.4f", got, sc.PlannedClose)
				}
				if got := e.PlannedTotal("").Seconds(); !closeTo(got, sc.PlannedAll) {
					t.Errorf("planned_total() Go %.4f, Python %.4f", got, sc.PlannedAll)
				}
			}
			// The shared keys of the real Go list must agree too.
			ge := timings.NewEstimator(kind, goSteps, f.History, tun, clk)
			ge.SetConditions(sc.Cond.cond())
			for _, s := range goSteps {
				want, ok := sc.Est[s.Key]
				if !ok {
					continue // settings / hooks: new in Go
				}
				if got := ge.Estimate(s.Key).Seconds(); !closeTo(got, want) {
					t.Errorf("Go step list: estimate %-12s Go %.4f, Python %.4f", s.Key, got, want)
				}
			}
		})
	}
}

func TestParityTimeline(t *testing.T) {
	var est pyEstimator
	testutil.ParityFixture(t, "estimator", &est)
	var raw struct {
		Timeline struct {
			Events []map[string]any `json:"events"`
			Snaps  []struct {
				Label        string  `json:"label"`
				Remaining    float64 `json:"remaining"`
				Fraction     float64 `json:"fraction"`
				PlannedClose float64 `json:"planned_close"`
			} `json:"snaps"`
			StartUnix float64 `json:"start_unix"`
		} `json:"timeline"`
	}
	testutil.ParityFixture(t, "estimator", &raw)
	tun := config.Defaults().Tunables
	steps := pySteps(est.PythonSteps.Update, timings.UpdateSteps(tun.StepSeconds))
	start := time.Unix(int64(raw.Timeline.StartUnix), 0)
	clk := testutil.NewClock(start)
	e := timings.NewEstimator(timings.KindUpdate, steps, est.History, tun, clk)
	e.SetConditions(timings.Conditions{Source: "mirror", Mirror: boolp(true), NPM: boolp(false), PyDeps: boolp(false), Commits: intp(81)})
	e.ExpectSkip("pydeps", true)
	// Python: snap("start"), then per script step: stage -> snap, advance -> snap.
	i := 0
	check := func(label string) {
		t.Helper()
		w := raw.Timeline.Snaps[i]
		i++
		if w.Label != label {
			t.Fatalf("snapshot %d: label %q, want %q (fixture order changed)", i, label, w.Label)
		}
		if got := e.Remaining().Seconds(); !closeTo(got, w.Remaining) {
			t.Errorf("%-22s remaining Go %.4f, Python %.4f", w.Label, got, w.Remaining)
		}
		if got := e.Fraction(); math.Abs(got-w.Fraction) > 0.0001 {
			t.Errorf("%-22s fraction  Go %.6f, Python %.6f", w.Label, got, w.Fraction)
		}
	}
	check("start")
	var key string
	var dur float64
	for _, ev := range raw.Timeline.Events {
		if k, ok := ev["stage"].(string); ok {
			key = k
			e.Stage(k)
			check("after stage " + k)
			continue
		}
		dur = ev["advance"].(float64)
		clk.Advance(time.Duration(dur * float64(time.Second)))
		check(labelAdvance(key, dur))
	}
	e.Complete(boolp(true))
	if i != len(raw.Timeline.Snaps)-1 {
		t.Fatalf("compared %d of %d snapshots", i, len(raw.Timeline.Snaps))
	}
	if got := e.Fraction(); got != 1 {
		t.Errorf("fraction after complete = %v, Python 1", got)
	}
}

func labelAdvance(key string, dur float64) string {
	return key + " +" + trimFloat(dur) + "s"
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) {
		return itoa(int(f))
	}
	return "x"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func boolp(b bool) *bool { return &b }
func intp(n int) *int    { return &n }
