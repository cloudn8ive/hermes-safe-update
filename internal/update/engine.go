package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// errUpToDate ends a run early with exit 0 and the "up to date" card.
var errUpToDate = errors.New("hermes is up to date")

// finallyTimeout bounds the clean-up work that runs after a cancel.
const finallyTimeout = 5 * time.Minute

// NewRun prepares a run: defaults for optional deps, the step list of the
// mode and an estimator seeded from the timings history.
func NewRun(d Deps) *Run {
	r := &Run{Deps: d}
	r.defaults()
	steps := timings.UpdateSteps(r.Config.Tunables.StepSeconds)
	kind := timings.KindUpdate
	if r.Mode == ModeCheck {
		steps = timings.CheckSteps(r.Config.Tunables.StepSeconds)
		kind = timings.KindCheck
	}
	var hist []timings.Record
	if r.TimingsPath != "" {
		h, err := timings.LoadHistory(r.TimingsPath)
		if err != nil {
			r.histErr = err
		}
		hist = h
	}
	r.Est = timings.NewEstimator(kind, steps, hist, r.Config.Tunables, r.Clock)
	r.steps = steps
	return r
}

func (r *Run) defaults() {
	if r.Config == nil {
		r.Config = &config.Config{File: config.Defaults()}
	}
	if r.Log == nil {
		r.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if r.UI == nil {
		r.UI = ui.NewPlain(io.Discard, platform.Stub{})
	}
	if r.Clock == nil {
		r.Clock = timings.SystemClock{}
	}
	if r.Sleep == nil {
		r.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if r.FileExists == nil {
		r.FileExists = func(p string) bool { _, err := os.Stat(p); return p != "" && err == nil }
	}
	if r.Platform == nil {
		r.Platform = platform.NewFake()
	}
	if r.Title == "" {
		r.Title = r.Config.Theme.TitleUpdate
		if r.Mode == ModeCheck {
			r.Title = r.Config.Theme.TitleCheck
		}
	}
	if r.Facts.Procs == nil {
		r.Facts.Procs = map[string]int{}
	}
}

// Execute runs the stages in order and returns the first error; the
// finally-steps (stop the marker refresher, release the marker, relaunch
// if due, restart a gateway the tool stopped, save timings) always run.
func Execute(ctx context.Context, r *Run, stages []Stage) (err error) {
	r.defaults()
	r.started = r.Clock.Now()
	if r.Est != nil {
		r.UI.Start(r.Title, r.steps, r.Est, r.Mode == ModeUpdate)
		r.uiStarted = true
	}
	r.Log.Info("==== Hermes safe update started", slog.Int("pid", os.Getpid()),
		slog.Bool("check", r.Mode == ModeCheck), slog.Bool("unattended", r.Config.Run.Unattended))
	if r.histErr != nil {
		r.note("timing history unreadable (%v); estimates use defaults", r.histErr)
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("internal error: %v", p)
		}
		err = r.finally(ctx, err)
	}()
	for _, s := range stages {
		if s.Skip != nil && s.Skip(r) {
			continue
		}
		r.stage(s.Key)
		if err := s.Run(ctx, r); err != nil {
			if errors.Is(err, apperr.ErrNotImplemented) {
				return fmt.Errorf("stage %s: %w", s.Key, err)
			}
			return err
		}
	}
	return nil
}

// finally runs once, whatever happened.
func (r *Run) finally(ctx context.Context, err error) error {
	if r.finalized {
		return err
	}
	r.finalized = true
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finallyTimeout)
	defer cancel()

	if r.stopRefresh != nil {
		r.stopRefresh()
	}
	if r.claimed && r.Marker != nil {
		if ok, rerr := r.Marker.Release(); rerr != nil {
			r.warn("could not release marker: %v", rerr)
		} else if ok {
			r.Log.Info("released update marker")
		}
	}
	if r.relaunchDue() {
		r.relaunch(fctx)
	}
	if r.Facts.GateStopped {
		r.ensureGatewayRunning(fctx)
	}

	switch {
	case errors.Is(err, errUpToDate):
		err = nil
		r.complete(true)
		r.summary(ui.Card{OK: ptr(true), Headline: "Hermes is up to date", Rows: []ui.Row{{Key: "Log", Value: r.LogPath}}})
		if r.Mode == ModeCheck {
			r.offerMirror(fctx)
		}
	case (errors.Is(err, apperr.ErrCancelled) || errors.Is(err, context.Canceled)) && !r.Facts.Closed:
		r.complete(false)
		r.summary(ui.Card{OK: ptr(false), Headline: "Update cancelled. Nothing was changed; Hermes was left running.",
			Rows: []ui.Row{{Key: "Log", Value: r.LogPath}}})
	case errors.Is(err, apperr.ErrUpdateProblem) && !r.summaryShown:
		// interrupted during the update: still show what happened
		r.complete(false)
		r.summary(ui.Card{OK: ptr(false), Headline: "The update needs attention: it was interrupted.",
			Rows: []ui.Row{{Key: "Log", Value: r.LogPath}}})
	case err != nil:
		r.complete(false)
	}
	r.saveTimings()
	r.Log.Info("==== done", slog.Int("exit", apperr.ExitCode(err)))
	return err
}

// withEst runs fn on the estimator under the renderer's lock: the renderer
// owns the estimator once Start ran (its ticker reads it), so the flow never
// touches it directly.
func (r *Run) withEst(fn func(*timings.Estimator)) {
	if r.Est == nil {
		return
	}
	if w, ok := r.UI.(interface {
		WithEstimator(func(*timings.Estimator))
	}); ok && r.uiStarted {
		w.WithEstimator(fn)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.Est)
}

// complete freezes the estimator and the footer once (the renderer
// completes the estimator).
func (r *Run) complete(ok bool) {
	if r.completed {
		return
	}
	r.completed = true
	r.UI.Complete(&ok)
	r.withEst(func(e *timings.Estimator) {
		if done, _ := e.Result(); !done {
			e.Complete(&ok) // a renderer that does not drive the estimator
		}
	})
}

// saveTimings writes the history record (plain mode too, D5).
func (r *Run) saveTimings() {
	if r.timingsSaved || r.Est == nil || r.TimingsPath == "" {
		return
	}
	r.timingsSaved = true
	var rec *timings.Record
	r.withEst(func(e *timings.Estimator) { rec = e.Record(r.started) })
	if rec == nil {
		return
	}
	keep := r.Config.Tunables.HistoryKeep
	if err := timings.SaveHistory(r.TimingsPath, *rec, keep); err != nil {
		r.Log.Warn("timings not saved", slog.String("error", err.Error()))
	}
}

// stage advances the footer (the renderer stages the estimator).
func (r *Run) stage(key string) {
	if r.Est == nil {
		return
	}
	r.UI.Stage(key)
}

// plannedTotal reads the estimate from key to the end.
func (r *Run) plannedTotal(key string) time.Duration {
	var d time.Duration
	r.withEst(func(e *timings.Estimator) { d = e.PlannedTotal(key) })
	return d
}

// roughNote is the "rough: ..." qualifier for the estimate from key on, or "".
func (r *Run) roughNote(key string) string {
	var n string
	r.withEst(func(e *timings.Estimator) { n = e.RoughNote(key) })
	return n
}

// withRough appends the rough qualifier, if any, to an estimate text.
func withRough(text, note string) string {
	if note == "" {
		return text
	}
	return text + " (" + note + ")"
}

// ---- messages: every line goes to the screen and the log ----

func (r *Run) say(level ui.Level, prefix, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.UI.Line(level, msg)
	switch level {
	case ui.LevelWarn:
		r.Facts.Warnings++
		r.Log.Warn(prefix + msg)
	case ui.LevelStop:
		r.Log.Warn(prefix + msg)
	default:
		r.Log.Info(prefix + msg)
	}
}

func (r *Run) info(format string, a ...any) { r.say(ui.LevelInfo, "", format, a...) }
func (r *Run) note(format string, a ...any) { r.say(ui.LevelNote, "note: ", format, a...) }
func (r *Run) warn(format string, a ...any) { r.say(ui.LevelWarn, "WARN ", format, a...) }
func (r *Run) stop(format string, a ...any) { r.say(ui.LevelStop, "STOP ", format, a...) }

func (r *Run) rows(rows ...ui.Row) {
	r.UI.Rows(rows...)
	for _, row := range rows {
		r.Log.Info(row.Key + ": " + row.Value)
	}
}

// rowsLog shows row on the screen but logs logValue instead: for rows whose
// screen text is personal (busy-session ids and titles) and must stay out
// of the log file, which people paste into bug reports.
func (r *Run) rowsLog(row ui.Row, logValue string) {
	r.UI.Rows(row)
	r.Log.Info(row.Key + ": " + logValue)
}

func (r *Run) section(title string) {
	r.UI.Section(title)
	r.Log.Info("== " + title)
}

// ask shows a single-key prompt (paused footer while it waits).
func (r *Run) ask(ctx context.Context, text string, keys []rune, timeout time.Duration, countdown string) (ui.Answer, error) {
	return r.askLog(ctx, text, text, keys, timeout, countdown)
}

// askLog is ask with a separate text for the log (no personal paths).
func (r *Run) askLog(ctx context.Context, text, logText string, keys []rune, timeout time.Duration, countdown string) (ui.Answer, error) {
	r.Log.Info("prompt: " + logText)
	r.UI.Waiting("Waiting for your answer", r.Clock.Now().Add(timeout), countdown)
	defer r.UI.Waiting("", time.Time{}, "")
	a, err := r.UI.Prompt(ctx, ui.PromptSpec{Text: text, Keys: keys, Timeout: timeout, CountdownLabel: countdown, AbortOnCtrlC: true})
	r.Log.Info("prompt answer", slog.String("key", string(a.Key)), slog.Bool("timeout", a.TimedOut), slog.Bool("abort", a.Abort))
	return a, err
}

// minutes renders a planned duration as "about N min" (D11 RoundToEven).
func minutes(d time.Duration) int {
	return max(1, int(math.RoundToEven(d.Seconds()/60)))
}

// fmtDuration is m:ss (minutes not wrapped at 60), python fmt_duration.
func fmtDuration(d time.Duration) string {
	s := int(math.RoundToEven(d.Seconds()))
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func ptr[T any](v T) *T { return &v }

// runState is the unexported part of Run (kept here to keep update.go's
// public types readable).
type runState struct {
	mu            sync.Mutex
	steps         []timings.Step
	histErr       error
	relaunchTried bool
	summaryShown  bool
	logRows       map[string]string // summary row key -> text for the log (screen keeps the real value)
	uiStarted     bool
	completed     bool
}
