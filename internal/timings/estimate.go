package timings

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

// Owner: I2. Port of python-behaviour.md §5.10: each step's estimate is the
// mean of the last `estimate_window` matching runs (D13), taken from the
// pool that shares the step's condition (same / fast / unknown / all); class
// factors stretch pending steps after a slow network or machine; an
// overrunning step is expected to need k x its time so far again; the bar
// never moves backwards. Python's round() is math.RoundToEven here (D11).

const (
	classMinEst = 5.0 // steps usually shorter than this don't vote on the class factor
	fastN       = 3   // fastest runs used when a "fast" condition value has no runs of its own
)

// fastValues are condition values that make a step much faster than in any
// recorded run; without runs of their own the fastest ones stand in.
var fastValues = map[string][]string{"mirror": {"true"}, "source": {"mirror", "api"}}

// StepState is the lifecycle of one step.
type StepState int

const (
	Pending StepState = iota
	Active
	Done
	Skipped
)

// stepRT is the runtime state of one step.
type stepRT struct {
	state      StepState
	expectSkip bool
	t0, t1     time.Time
	est        float64 // seconds
}

// Estimator tracks a run's steps against history. Safe for use from one
// goroutine at a time; the UI serialises access.
type Estimator struct {
	Steps   []Step
	History []Record
	Kind    string
	Tun     config.Tunables
	Clock   Clock

	rt       []stepRT
	cond     Conditions
	waiting  bool
	finished bool
	result   *bool
	fracHi   float64
}

// NewEstimator builds an estimator for steps of the given kind.
func NewEstimator(kind string, steps []Step, history []Record, tun config.Tunables, clock Clock) *Estimator {
	if clock == nil {
		clock = SystemClock{}
	}
	e := &Estimator{Steps: steps, History: history, Kind: kind, Tun: tun, Clock: clock}
	e.rt = make([]stepRT, len(steps))
	e.recompute()
	return e
}

func (e *Estimator) window() int {
	if e.Tun.EstimateWindow > 0 {
		return e.Tun.EstimateWindow
	}
	return 5
}

func (e *Estimator) index(key string) int {
	for i := range e.Steps {
		if e.Steps[i].Key == key {
			return i
		}
	}
	return -1
}

func boolStr(p *bool) string {
	switch {
	case p == nil:
		return ""
	case *p:
		return "true"
	}
	return "false"
}

// condValue is the known value of condition name as a string ("" = unknown).
func condValue(c *Conditions, name string) string {
	if c == nil {
		return ""
	}
	switch name {
	case "source":
		return c.Source
	case "mirror":
		return boolStr(c.Mirror)
	case "npm":
		return boolStr(c.NPM)
	case "pydeps":
		return boolStr(c.PyDeps)
	}
	return ""
}

// recCond is the value condition name had in a past record ("" = the record
// does not say; records written by the Python tool have no cond).
func recCond(r *Record, name string) string {
	if v := condValue(r.Cond, name); v != "" {
		return v
	}
	switch name {
	case "source":
		return r.Source
	case "npm":
		return boolStr(r.NPM)
	case "pydeps":
		return boolStr(r.PyDeps)
	}
	return ""
}

func isFull(r *Record) bool {
	_, d := r.Steps.Get("desktop")
	_, v := r.Steps.Get("verify")
	return d || v
}

func isPreflight(key string) bool {
	for _, k := range PreflightKeys {
		if k == key {
			return true
		}
	}
	return false
}

// usable: the record has a number for key, is of the same kind, is a full
// update (or key is a pre-flight step), and key is not the step a failed
// run stopped in (that one was cut short).
func usable(r *Record, key, kind string) bool {
	if r.Kind != kind {
		return false
	}
	if _, ok := r.Steps.Get(key); !ok {
		return false
	}
	if kind == KindUpdate && !isFull(r) && !isPreflight(key) {
		return false
	}
	if (r.OK == nil || !*r.OK) && len(r.Steps) > 0 && r.Steps[len(r.Steps)-1].Key == key {
		return false
	}
	return true
}

// histValues returns the durations (oldest first) of key from matching runs.
func (e *Estimator) histValues(key, condName, condVal string) []float64 {
	var recs []*Record
	for i := range e.History {
		if usable(&e.History[i], key, e.Kind) {
			recs = append(recs, &e.History[i])
		}
	}
	if condName != "" && condVal != "" {
		var same, unknown []*Record
		for _, r := range recs {
			switch recCond(r, condName) {
			case condVal:
				same = append(same, r)
			case "":
				unknown = append(unknown, r)
			}
		}
		fast := false
		for _, f := range fastValues[condName] {
			fast = fast || f == condVal
		}
		switch {
		case len(same) > 0:
			recs = same
		case fast:
			sort.SliceStable(recs, func(a, b int) bool {
				x, _ := recs[a].Steps.Get(key)
				y, _ := recs[b].Steps.Get(key)
				return x < y
			})
			if len(recs) > fastN {
				recs = recs[:fastN]
			}
		case len(unknown) > 0:
			recs = unknown
		}
	}
	out := make([]float64, len(recs))
	for i, r := range recs {
		out[i], _ = r.Steps.Get(key)
	}
	return out
}

// recompute rebuilds every step's estimate from the matching history.
func (e *Estimator) recompute() {
	for i, s := range e.Steps {
		vals := e.histValues(s.Key, s.Cond, condValue(&e.cond, s.Cond))
		if n := e.window(); len(vals) > n {
			vals = vals[len(vals)-n:]
		}
		if len(vals) == 0 {
			e.rt[i].est = float64(s.DefaultS)
			continue
		}
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		e.rt[i].est = sum / float64(len(vals))
	}
}

func (e *Estimator) now() time.Time { return e.Clock.Now() }

// Stage makes key the active step (earlier active -> done, earlier pending -> skipped).
func (e *Estimator) Stage(key string) {
	idx := e.index(key)
	if idx < 0 || e.rt[idx].state == Done || e.rt[idx].state == Active {
		return
	}
	now := e.now()
	for i := 0; i < idx; i++ {
		switch e.rt[i].state {
		case Active:
			e.rt[i].state, e.rt[i].t1 = Done, now
		case Pending:
			e.rt[i].state = Skipped
		}
	}
	e.rt[idx].state, e.rt[idx].t0 = Active, now
	e.waiting = false
}

// ExpectSkip marks a step the run expects to skip (e.g. pydeps unchanged).
func (e *Estimator) ExpectSkip(key string, skip bool) {
	if i := e.index(key); i >= 0 {
		e.rt[i].expectSkip = skip
	}
}

// SetConditions updates the known conditions and re-estimates. Fields left
// nil/empty keep their earlier value.
func (e *Estimator) SetConditions(c Conditions) {
	if c.Source != "" {
		e.cond.Source = c.Source
	}
	if c.Mirror != nil {
		e.cond.Mirror = c.Mirror
	}
	if c.NPM != nil {
		e.cond.NPM = c.NPM
	}
	if c.PyDeps != nil {
		e.cond.PyDeps = c.PyDeps
	}
	if c.Commits != nil {
		e.cond.Commits = c.Commits
	}
	e.recompute()
}

// SetWaiting marks the run as waiting for the user (no ETA while true).
func (e *Estimator) SetWaiting(w bool) { e.waiting = w }

// Complete ends the run; ok=nil means unknown.
func (e *Estimator) Complete(ok *bool) {
	now := e.now()
	good := ok != nil && *ok
	for i := range e.rt {
		switch e.rt[i].state {
		case Active:
			e.rt[i].state, e.rt[i].t1 = Done, now
		case Pending:
			if good {
				e.rt[i].state = Skipped
			}
		}
	}
	e.finished, e.result, e.waiting = true, ok, false
}

// Result reports whether Complete was called and with which outcome.
func (e *Estimator) Result() (finished bool, ok *bool) { return e.finished, e.result }

// State returns a step's state (Pending for an unknown key).
func (e *Estimator) State(key string) StepState {
	if i := e.index(key); i >= 0 {
		return e.rt[i].state
	}
	return Pending
}

// ExpectedSkip reports whether a step was marked with ExpectSkip.
func (e *Estimator) ExpectedSkip(key string) bool {
	if i := e.index(key); i >= 0 {
		return e.rt[i].expectSkip
	}
	return false
}

// Current returns the active step.
func (e *Estimator) Current() (Step, bool) {
	for i := range e.rt {
		if e.rt[i].state == Active {
			return e.Steps[i], true
		}
	}
	return Step{}, false
}

// Elapsed is how long step key has been (or was) active.
func (e *Estimator) Elapsed(key string) time.Duration {
	i := e.index(key)
	if i < 0 || e.rt[i].t0.IsZero() {
		return 0
	}
	switch e.rt[i].state {
	case Active:
		return e.now().Sub(e.rt[i].t0)
	case Done:
		return e.rt[i].t1.Sub(e.rt[i].t0)
	}
	return 0
}

func dur(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// Estimate returns the current estimate for key.
func (e *Estimator) Estimate(key string) time.Duration {
	if i := e.index(key); i >= 0 {
		return dur(e.rt[i].est)
	}
	return 0
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// classFactors: per class, actual/estimated time of finished steps (and of
// an active step already past its estimate) damped into a pending-step factor.
func (e *Estimator) classFactors(now time.Time) map[string]float64 {
	num, den := map[string]float64{}, map[string]float64{}
	for i, s := range e.Steps {
		r := &e.rt[i]
		if s.Class == "" || s.DefaultS <= 0 || r.est < classMinEst {
			continue
		}
		var actual float64
		switch {
		case r.state == Done && !r.t0.IsZero() && !r.t1.IsZero():
			actual = r.t1.Sub(r.t0).Seconds()
		case r.state == Active && now.Sub(r.t0).Seconds() > r.est:
			actual = now.Sub(r.t0).Seconds()
		default:
			continue
		}
		num[s.Class] += actual
		den[s.Class] += r.est
	}
	out := map[string]float64{}
	for c := range num {
		out[c] = clamp(1+e.Tun.EstimateClassDamp*(num[c]/den[c]-1), e.Tun.EstimateClassMin, e.Tun.EstimateClassMax)
	}
	return out
}

func (e *Estimator) pendingEst(i int, f map[string]float64) float64 {
	if k, ok := f[e.Steps[i].Class]; ok && e.Steps[i].Class != "" {
		return e.rt[i].est * k
	}
	return e.rt[i].est
}

// overrunLeft: seconds still to go for a step that ran el against est.
func (e *Estimator) overrunLeft(el, est float64) float64 {
	floor := math.Min(15, 0.1*est)
	if el < est {
		return math.Max(est-el, floor)
	}
	return math.Max(floor, e.Tun.EstimateOverrunK*el)
}

// Remaining is the estimated time left.
func (e *Estimator) Remaining() time.Duration {
	now := e.now()
	f := e.classFactors(now)
	left := 0.0
	for i, s := range e.Steps {
		r := &e.rt[i]
		if s.DefaultS <= 0 || r.expectSkip || r.state == Done || r.state == Skipped {
			continue
		}
		if r.state == Active {
			left += e.overrunLeft(now.Sub(r.t0).Seconds(), r.est)
		} else {
			left += e.pendingEst(i, f)
		}
	}
	return dur(left)
}

// Fraction is the bar position 0..1 (never decreases).
func (e *Estimator) Fraction() float64 {
	if e.finished {
		e.fracHi = 1
		return 1
	}
	now := e.now()
	f := e.classFactors(now)
	var total, done float64
	for i, s := range e.Steps {
		r := &e.rt[i]
		if s.DefaultS <= 0 || (r.expectSkip && r.state == Pending) || r.state == Skipped {
			continue
		}
		switch r.state {
		case Done:
			total += r.est
			done += r.est
		case Active:
			total += r.est
			done += math.Min(now.Sub(r.t0).Seconds(), 0.95*r.est)
		default:
			total += e.pendingEst(i, f)
		}
	}
	frac := 0.0
	if total > 0 {
		frac = clamp(done/total, 0, 0.99)
	}
	e.fracHi = math.Max(e.fracHi, frac)
	return e.fracHi
}

// planned lists the indexes of the steps that count towards the estimate,
// from fromKey on (same selection PlannedTotal sums).
func (e *Estimator) planned(fromKey string) []int {
	var idx []int
	for i, s := range e.Steps {
		if s.DefaultS > 0 && !e.rt[i].expectSkip {
			idx = append(idx, i)
		}
	}
	if fromKey != "" {
		for n, i := range idx {
			if e.Steps[i].Key == fromKey {
				return idx[n:]
			}
		}
	}
	return idx
}

// PlannedTotal sums estimates from fromKey to the end ("close" for the confirm prompt).
func (e *Estimator) PlannedTotal(fromKey string) time.Duration {
	sum := 0.0
	for _, i := range e.planned(fromKey) {
		sum += e.rt[i].est
	}
	return dur(sum)
}

// RoughMinRuns is how many earlier runs under the same conditions an
// estimate needs before it stops being called rough.
const RoughMinRuns = 3

// likeThis reports whether a past record ran under the same value of every
// condition in conds (name -> current value).
func likeThis(r *Record, conds map[string]string) bool {
	for name, want := range conds {
		got := recCond(r, name)
		if got == "" && name == "mirror" && r.Cond == nil && r.Source != "" {
			got = "false" // Python-era record: mirror follows the source
			if r.Source == "mirror" {
				got = "true"
			}
		}
		if got != want {
			return false
		}
	}
	return true
}

// RoughNote says why the estimate from fromKey on is shaky, or "" when it
// rests on RoughMinRuns or more finished full updates run under the same
// conditions as this one (source, mirror, Node packages, Python deps; only
// those of steps that will really run count). Steps with no recorded run
// fall back to defaults, so a user with little history sees it too.
func (e *Estimator) RoughNote(fromKey string) string {
	if e.Kind != KindUpdate {
		return "" // a check run estimates nothing the user plans around
	}
	conds := map[string]string{}
	for _, i := range e.planned(fromKey) {
		if c := e.Steps[i].Cond; c != "" {
			if v := condValue(&e.cond, c); v != "" {
				conds[c] = v
			}
		}
	}
	n := 0
	for i := range e.History {
		r := &e.History[i]
		if r.Kind == e.Kind && r.OK != nil && *r.OK && isFull(r) && likeThis(r, conds) {
			n++
		}
	}
	switch {
	case n >= RoughMinRuns:
		return ""
	case n == 0 && len(e.History) == 0:
		return "rough: no earlier runs like this; uses defaults"
	case n == 0:
		return "rough: no earlier runs like this"
	case n == 1:
		return "rough: 1 earlier run like this"
	}
	return fmt.Sprintf("rough: %d earlier runs like this", n)
}

// FinishETA is the expected end time; ok=false when unknown.
func (e *Estimator) FinishETA() (time.Time, bool) {
	if e.finished || e.waiting {
		return time.Time{}, false
	}
	for i, s := range e.Steps {
		if s.DefaultS > 0 && !e.rt[i].expectSkip && (e.rt[i].state == Pending || e.rt[i].state == Active) {
			return e.now().Add(e.Remaining()), true
		}
	}
	return time.Time{}, false
}

func round1(x float64) float64 { return math.RoundToEven(x*10) / 10 }

// Record builds the history record of this run (nil when no estimated step
// finished). Steps keep step-list order; waits (default 0) are not recorded.
func (e *Estimator) Record(started time.Time) *Record {
	var steps OrderedSeconds
	for i, s := range e.Steps {
		r := &e.rt[i]
		if r.state == Done && s.DefaultS > 0 && !r.t0.IsZero() && !r.t1.IsZero() {
			steps = append(steps, StepSeconds{Key: s.Key, Seconds: round1(r.t1.Sub(r.t0).Seconds())})
		}
	}
	if len(steps) == 0 {
		return nil
	}
	rec := &Record{
		Started: started.Format(StartedLayout),
		Kind:    e.Kind,
		OK:      e.result,
		Total:   round1(e.now().Sub(started).Seconds()),
		Steps:   steps,
		Commits: e.cond.Commits,
		PyDeps:  e.cond.PyDeps,
		NPM:     e.cond.NPM,
		Source:  e.cond.Source,
	}
	if e.cond != (Conditions{}) {
		c := e.cond
		rec.Cond = &c
	}
	return rec
}
