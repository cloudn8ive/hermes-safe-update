package update

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// The marker lines of one run in logs/safe-update.log. They are written by
// engine.go and stages.go with these constants and read back here, to find
// out whether the run before this one reached `hermes update` and never
// finished. The log is Safe Update's own, so these are not Hermes wording.
const (
	logStarted = "==== Hermes safe update started"
	logDone    = "==== done"
	// logRunningPrefix starts the "== Running: hermes update --yes ..." line.
	logRunningPrefix = "== Running: hermes update"
	// logFinishing is written when a run starts to finish an update that did
	// not finish (no new commits to bring it about); its keys attribute names
	// that update. A later unattended run reads it back: one attempt per
	// unfinished update, not one per day.
	logFinishing = "finishing the previous update"
)

// logTailBytes is how much of the end of the log is looked at (a run's whole
// output is some tens of KB). When the current file holds no earlier run (the
// log rotates when it is opened, so the run that stopped can sit at the end
// of the first rotated file), the end of that file is read as well.
const logTailBytes = 512 << 10

// logCreateSlack is how much later than a run's "started" line a process may
// have been created and still be that run (the log line and the process
// clock are read apart, as for the marker).
const logCreateSlack = 2 * time.Second

// readLogTail returns the last limit bytes of the file, from the start of a line.
func readLogTail(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(0, st.Size()-limit)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if off > 0 { // the first line is cut: drop it
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			b = nil
		}
	}
	return b, nil
}

// logRun is one run as the log shows it.
type logRun struct {
	pid     int
	started bool      // its "started" line is in the tail
	began   time.Time // the time of that line
	running bool      // it reached `hermes update`
	ranAt   time.Time // when it got there
	done    bool      // it wrote its "done" line
	step    string
	last    time.Time
	tried   []string // keys of the unfinished updates it set out to finish
}

// parseLogLine splits a slog text line into its time, message and the rest
// (the attributes).
func parseLogLine(line string) (t time.Time, msg, attrs string, ok bool) {
	rest, found := strings.CutPrefix(line, "time=")
	if !found {
		return
	}
	ts, rest, _ := strings.Cut(rest, " ")
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return
	}
	i := strings.Index(rest, "msg=")
	if i < 0 {
		return
	}
	rest = rest[i+len("msg="):]
	if strings.HasPrefix(rest, `"`) {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return
		}
		if msg, err = strconv.Unquote(q); err != nil {
			return
		}
		return t, msg, rest[len(q):], true
	}
	msg, attrs, _ = strings.Cut(rest, " ")
	return t, msg, attrs, true
}

// attrString is the value of key in the attributes (slog quotes a value that
// has a space in it).
func attrString(attrs, key string) string {
	i := strings.Index(" "+attrs, " "+key+"=")
	if i < 0 {
		return ""
	}
	v := attrs[i+len(key)+1:]
	if strings.HasPrefix(v, `"`) {
		if q, err := strconv.QuotedPrefix(v); err == nil {
			if u, err := strconv.Unquote(q); err == nil {
				return u
			}
		}
	}
	v, _, _ = strings.Cut(v, " ")
	return v
}

func attrInt(attrs, key string) int {
	for _, f := range strings.Fields(attrs) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

// logRuns splits the tail into runs at the "started" lines. Lines before the
// first one belong to a run whose start was cut off (pid 0).
func logRuns(tail []byte) []*logRun {
	var runs []*logRun
	var cur *logRun
	for _, line := range strings.Split(string(tail), "\n") {
		t, msg, attrs, ok := parseLogLine(strings.TrimRight(line, "\r"))
		if !ok {
			continue
		}
		if msg == logStarted {
			cur = &logRun{pid: attrInt(attrs, "pid"), started: true, began: t}
			runs = append(runs, cur)
		} else if cur == nil {
			cur = &logRun{}
			runs = append(runs, cur)
		}
		cur.last = t
		switch {
		case strings.HasPrefix(msg, logRunningPrefix):
			cur.running, cur.step, cur.ranAt = true, "", t
		case msg == logDone:
			cur.done = true
		case msg == logFinishing:
			for _, k := range strings.Split(attrString(attrs, "keys"), ",") {
				if k != "" {
					cur.tried = append(cur.tried, k)
				}
			}
		case cur.running:
			if s := hermes.TriggerFor(msg); s != "" {
				cur.step = s
			}
		}
	}
	return runs
}

// stepGroups are the steps a receipt can name that are not one step of the
// update steps: it knows "the builds" but not which build, and what follows
// the restart.
var stepGroups = map[string]string{
	"start": "starting the update",
	"build": "building the apps",
	"tail":  "the last steps",
}

// stepPhrases word the update steps the log and the receipts can name, to
// follow "stopped during": the step's label lower-cased would read "pulling
// code, clearing caches".
var stepPhrases = map[string]string{
	"gateway_stop": "pausing the gateway",
	"fetch":        "fetching the new commits",
	"pull":         "pulling the new code",
	"pydeps":       "installing Python dependencies",
	"frontend":     "building the TUI and web UI",
	"desktop":      "building the desktop app",
	"package":      "packaging the desktop app",
	"finalize":     "syncing skills and config",
	"restart":      "restarting the gateway",
}

// stepWords is the plain name of an update step ("packaging the desktop
// app"): the log reads the step from the output of `hermes update`, the
// receipt from its last stage; both are worded here.
func stepWords(key string) string {
	if w, ok := stepGroups[key]; ok {
		return w
	}
	if w, ok := stepPhrases[key]; ok {
		return w
	}
	if key != "" {
		for _, s := range timings.UpdateSteps(nil) {
			if s.Key == key {
				r, n := utf8.DecodeRuneInString(s.Label)
				return string(unicode.ToLower(r)) + s.Label[n:]
			}
		}
	}
	return stepGroups["start"]
}

// logView is what the tail of the log says about the runs before this one.
type logView struct {
	// Prev: the newest earlier run that reached `hermes update` never wrote
	// its "done" line (nil: it finished, is still going, or there is none).
	Prev *hermes.PreviousUpdate
	// RanAt is when that newest earlier run got to `hermes update` (zero:
	// no earlier run did, or the tail does not say), finished or not. An
	// unfinished update older than this has been run over since.
	RanAt time.Time
	// Tried are the keys of the unfinished updates earlier runs set out to
	// finish (logFinishing).
	Tried map[string]bool
	// Unknown: the log could not be read, so none of the above is known.
	Unknown bool
}

// ownerAlive says whether pid is the process of the run that began at the
// given time (a live process created no later than that).
type ownerAlive func(pid int, began time.Time) bool

// runOwner builds the test for "this run is still going": the pid is alive
// AND that process was created no later than the run's "started" line. After
// a crash and a reboot the pid of the dead run is often taken by an unrelated
// process, created later. A creation time that cannot be read proves nothing,
// so the run then counts as gone and the unfinished update stays detected.
func runOwner(alive func(int) bool, created func(int) (time.Time, bool)) ownerAlive {
	if alive == nil {
		return nil
	}
	return func(pid int, began time.Time) bool {
		if !alive(pid) || created == nil {
			return false
		}
		ct, ok := created(pid)
		return ok && !ct.IsZero() && !ct.After(began.Add(logCreateSlack))
	}
}

// readLog reads the tail of the log: the runs before the one with pid self.
// An error = the tail cannot tell (this run is not in it, for example because
// the log is not written).
func readLog(tail []byte, self int, alive ownerAlive) (logView, error) {
	var v logView
	runs := logRuns(tail)
	cur := -1
	for i, r := range runs {
		if r.started && r.pid == self {
			cur = i
		}
	}
	if cur < 0 {
		return v, fmt.Errorf("this run (pid %d) is not in the log tail", self)
	}
	for i := cur - 1; i >= 0; i-- {
		for _, k := range runs[i].tried {
			if v.Tried == nil {
				v.Tried = map[string]bool{}
			}
			v.Tried[k] = true
		}
	}
	for i := cur - 1; i >= 0; i-- {
		r := runs[i]
		if !r.running {
			continue // a check, a stop or an "up to date" run: it never started the update
		}
		v.RanAt = r.ranAt
		// This process is never the run that stopped (a pid that comes round
		// again); another pid counts as that run only while it lives and is
		// the same process.
		if r.done || (r.pid > 0 && r.pid != self && alive != nil && alive(r.pid, r.began)) {
			return v, nil
		}
		v.Prev = &hermes.PreviousUpdate{Signals: []string{hermes.SignalOwnLog}, At: r.last, Stage: stepWords(r.step)}
		if r.started {
			v.Prev.Keys = []string{"log:" + r.began.UTC().Format(time.RFC3339Nano)}
		}
		return v, nil
	}
	return v, nil
}

// previousInLog is readLog's verdict: the newest run before the one with pid
// self that reached `hermes update` and has no "done" line after it is an
// update that did not finish. nil, nil = the previous run finished, or there
// is none.
func previousInLog(tail []byte, self int, alive ownerAlive) (*hermes.PreviousUpdate, error) {
	v, err := readLog(tail, self, alive)
	return v.Prev, err
}

// mergePrevious joins what the signals found: all signals are kept; when and
// what come from the log (it saw the stop itself), else from the first signal
// that knows.
func mergePrevious(parts []*hermes.PreviousUpdate) *hermes.PreviousUpdate {
	if len(parts) == 0 {
		return nil
	}
	out := &hermes.PreviousUpdate{}
	for _, p := range parts {
		out.Signals = append(out.Signals, p.Signals...)
		out.Keys = append(out.Keys, p.Keys...)
	}
	pick := func(use func(*hermes.PreviousUpdate) bool) *hermes.PreviousUpdate {
		for _, p := range parts {
			if p.Has(hermes.SignalOwnLog) && use(p) {
				return p
			}
		}
		for _, p := range parts {
			if use(p) {
				return p
			}
		}
		return nil
	}
	if p := pick(func(p *hermes.PreviousUpdate) bool { return !p.At.IsZero() }); p != nil {
		out.At = p.At
	}
	if p := pick(func(p *hermes.PreviousUpdate) bool { return p.Stage != "" }); p != nil {
		out.Stage = p.Stage
	}
	return out
}

// whenText words a time the way people say it, in fixed English (D12):
// "today 8:57 PM", "yesterday 8:57 PM", "Sun 8:57 PM" within the week,
// "Oct 12, 8:57 PM" before.
func whenText(t, now time.Time) string {
	t = t.In(now.Location())
	clock := t.Format("3:04 PM")
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	days := int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	switch {
	case days == 0:
		return "today " + clock
	case days == 1:
		return "yesterday " + clock
	case days > 1 && days < 7:
		return t.Format("Mon ") + clock
	case y1 != y2:
		return t.Format("Jan 2 2006, ") + clock
	}
	return t.Format("Jan 2, ") + clock
}

// stoppedText is what is known about the stop: "stopped during packaging the
// desktop app at Sun 8:57 PM". Empty when the signals say nothing about it
// (a stale update marker alone).
func stoppedText(p *hermes.PreviousUpdate, now time.Time) string {
	var s string
	switch {
	case p.Stage != "":
		s = "stopped during " + p.Stage
	case !p.At.IsZero():
		s = "stopped"
	default:
		return ""
	}
	if !p.At.IsZero() {
		s += " at " + whenText(p.At, now)
	}
	return s
}

// previousParen is " (stopped during ... at ...)" for a prompt, or "".
func (r *Run) previousParen() string {
	if p := r.Facts.Previous; p != nil {
		if s := stoppedText(p, r.Clock.Now()); s != "" {
			return " (" + s + ")"
		}
	}
	return ""
}

// previousWhat is what the rows say about the update that did not finish:
// "did not finish (stopped during ... at ...)".
func (r *Run) previousWhat() string {
	v := "did not finish"
	if s := stoppedText(r.Facts.Previous, r.Clock.Now()); s != "" {
		v += " (" + s + ")"
	} else {
		v += " (an update marker was left behind)"
	}
	return v
}

// previousRowValue is the pre-flight row's text.
func (r *Run) previousRowValue() string {
	v := r.previousWhat()
	if r.Facts.PreviousTried {
		v += "; an earlier attempt to finish it failed"
	}
	if r.Mode == ModeCheck {
		v += "; run Hermes Safe Update to finish it"
	}
	return v
}

// triedText is the clause a prompt adds when an earlier run already set out
// to finish this update (and it is still not finished).
func (r *Run) triedText() string {
	if r.Facts.PreviousTried {
		return ", and an earlier attempt to finish it failed"
	}
	return ""
}

// quiet: this run asks nobody (--unattended or --yes).
func (r *Run) quiet() bool { return r.Config.Run.Unattended || r.Config.Run.Yes }

// readOwnLog reads Safe Update's own log for the runs before this one. The
// log rotates when it is opened, so when the current file holds no earlier run
// the end of the first rotated file is read too. A log that cannot be read is
// logged and counts as unknown (an empty view).
func (r *Run) readOwnLog() logView {
	read := r.LogTail
	if read == nil {
		read = readLogTail
	}
	// the "started" line carries the real pid (engine.go), whatever SelfPID
	// says about the fake process table of a test
	self := os.Getpid()
	var alive func(int) bool
	if r.Platform != nil && r.Platform.Procs != nil {
		alive = r.Platform.Procs.Alive
	}
	owner := runOwner(alive, r.Created)
	tail, err := read(r.LogPath, logTailBytes)
	if err == nil && !hasEarlierRun(tail, self) {
		if older, rerr := read(r.LogPath+".1", logTailBytes); rerr == nil {
			tail = append(append([]byte(nil), older...), tail...)
		}
	}
	var v logView
	if err == nil {
		v, err = readLog(tail, self, owner)
	}
	if err != nil {
		r.Log.Info("previous update: own log not usable; unknown", slog.String("error", err.Error()))
		return logView{Unknown: true}
	}
	return v
}

// hasEarlierRun: the tail holds a run before the one with pid self.
func hasEarlierRun(tail []byte, self int) bool {
	runs := logRuns(tail)
	for i, r := range runs {
		if r.started && r.pid == self {
			return i > 0
		}
	}
	return false
}

// findUnfinished looks for an update that stopped before it finished, from
// three signals: Hermes' newest update receipt, Safe Update's own log and a
// stale update marker. Read-only and never fatal: a signal that cannot be read
// is logged and counts as unknown, which changes nothing.
//
// An unfinished update that a later run of Safe Update has run `hermes update`
// over is not offered again: the receipt is older than that run, and the run
// either finished the work or told its own story. A stale marker needs no such
// rule (every run that closes Hermes claims the marker and releases it again).
func (r *Run) findUnfinished(staleMarker bool) {
	var parts []*hermes.PreviousUpdate
	var rec *hermes.PreviousUpdate
	if r.Receipts != nil {
		p, err := r.Receipts.Unfinished()
		switch {
		case err != nil:
			r.Log.Info("previous update: receipt not readable; unknown", slog.String("error", err.Error()))
		case p != nil:
			rec = p
			if rec.Stage == "" && rec.StepKey != "" {
				rec.Stage = stepWords(rec.StepKey)
			}
		}
	}
	var view logView
	if r.LogPath != "" {
		view = r.readOwnLog()
	}
	if view.Prev != nil && r.Receipts != nil {
		// A newer finished update (the user ran `hermes update` by hand, as
		// Hermes tells them to) has finished what the stopped run left.
		fin, err := r.Receipts.NewestFinished()
		switch {
		case err != nil:
			r.Log.Info("previous update: receipts not readable for a newer finished update; unknown", slog.String("error", err.Error()))
		case fin.After(view.RanAt):
			r.Log.Info("previous update: an update finished after that run of Safe Update got to hermes update; not counted",
				slog.String("run_at", view.RanAt.Format(time.RFC3339)), slog.String("finished_update_started", fin.Format(time.RFC3339)))
			view.Prev = nil
		}
	}
	if rec != nil && !rec.At.IsZero() && view.RanAt.After(rec.At) {
		r.Log.Info("previous update: receipt is older than a later run of Safe Update; not counted",
			slog.String("receipt_at", rec.At.Format(time.RFC3339)), slog.String("later_run_at", view.RanAt.Format(time.RFC3339)))
		rec = nil
	}
	if rec != nil {
		parts = append(parts, rec)
	}
	if view.Prev != nil {
		parts = append(parts, view.Prev)
	}
	if staleMarker {
		parts = append(parts, &hermes.PreviousUpdate{Signals: []string{hermes.SignalStaleMarker}})
	}
	prev := mergePrevious(parts)
	if prev == nil {
		return
	}
	r.Facts.Previous = prev
	if view.Unknown {
		r.Facts.OnceUnknown = true
		r.Log.Info("previous update: the own log cannot say whether an earlier run already tried to finish it; a run that asks nobody will not try")
	}
	for _, k := range prev.Keys {
		if view.Tried[k] {
			r.Facts.PreviousTried = true
		}
	}
	at := ""
	if !prev.At.IsZero() {
		at = prev.At.Format(time.RFC3339)
	}
	attrs := []any{slog.String("signals", strings.Join(prev.Signals, ",")), slog.String("at", at), slog.String("stage", prev.Stage)}
	if r.Facts.PreviousTried {
		attrs = append(attrs, slog.Bool("tried_before", true))
	}
	r.Log.Info("previous update did not finish", attrs...)
	r.rows(ui.Row{Key: "last update", Value: r.previousRowValue(), Verdict: ptr(ui.VerdictAttention)})
}

// logFinishingAttempt writes the line a later run reads back (see logFinishing).
func (r *Run) logFinishingAttempt() {
	if r.Facts.Again != AgainFinish || r.Facts.Previous == nil {
		return
	}
	r.Log.Info(logFinishing, slog.String("keys", strings.Join(r.Facts.Previous.Keys, ",")))
}
