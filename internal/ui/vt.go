package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// Options tune a renderer. The zero value is the production behaviour with
// colour off; New sets Color from the configuration.
type Options struct {
	Clock     timings.Clock // default: the system clock
	NoTicker  bool          // tests: no background ticker, call Tick by hand
	TickEvery time.Duration // default 1 s
	Color     bool          // false = NO_COLOR: same layout, no colour (bold stays)
	Restore   func()        // console-mode restore from EnableVT, called by Close
}

func (o Options) clock() timings.Clock {
	if o.Clock == nil {
		return timings.SystemClock{}
	}
	return o.Clock
}

func (o Options) tickEvery() time.Duration {
	if o.TickEvery <= 0 {
		return time.Second
	}
	return o.TickEvery
}

const waitingLabel = "Waiting for your answer"

// VT is the renderer for a real VT console: pinned five-line footer, numbered
// stage banners, aligned key/value rows (python-behaviour §5). All output goes
// through one mutex; the ticker goroutine and the update pump may both call it.
type VT struct {
	mu   sync.Mutex
	w    io.Writer
	plat *platform.Platform
	th   config.Theme
	st   *style
	clk  timings.Clock
	opts Options
	pad  string

	title   string
	steps   []timings.Step
	groups  []string
	est     *timings.Estimator
	banners bool
	t0      time.Time

	started, closed bool
	waitLabel       string
	waitDeadline    time.Time
	countdown       string
	warnings        int

	drawn     int
	drawnLens []int
	lastKey   string

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

var _ Renderer = (*VT)(nil)

// NewVT builds the VT renderer writing to w.
func NewVT(w io.Writer, p *platform.Platform, th config.Theme, o Options) *VT {
	v := &VT{w: w, plat: p, th: th, opts: o, clk: o.clock()}
	v.st = newStyle(&v.th, o.Color)
	v.pad = strings.Repeat(" ", th.Margin)
	return v
}

// WithEstimator runs fn on the estimator while holding the renderer's lock,
// so the update flow can call SetConditions/ExpectSkip safely.
func (v *VT) WithEstimator(fn func(*timings.Estimator)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.est != nil {
		fn(v.est)
	}
}

func (v *VT) write(s string) {
	if s != "" {
		_, _ = io.WriteString(v.w, s)
	}
}

func (v *VT) con() platform.Console { return v.plat.Console }

func (v *VT) cols() int {
	if c, _, ok := v.con().Size(); ok && c > 0 {
		return c
	}
	return 100
}

// width is the usable text width: the window minus the margins and one cell
// so a line never reaches the last column (no auto-wrap).
func (v *VT) width() int {
	w := v.cols() - 1 - 2*v.th.Margin
	if w > v.th.MaxWidth {
		w = v.th.MaxWidth
	}
	if w < 40 {
		w = 40
	}
	return w
}

func (v *VT) col() config.Colors { return v.th.Colors }

// group is the stage group used for colouring: the current step's, else the
// last group that has a done or active step.
func (v *VT) group() string {
	if v.est == nil {
		return ""
	}
	if c, ok := v.est.Current(); ok {
		return c.Group
	}
	last := ""
	for _, s := range v.steps {
		if st := v.est.State(s.Key); st == timings.Done || st == timings.Active {
			last = s.Group
		}
	}
	return last
}

func (v *VT) result() (finished bool, ok *bool) {
	if v.est == nil {
		return false, nil
	}
	return v.est.Result()
}

func okTrue(p *bool) bool { return p != nil && *p }

// ---- Start / header

func (v *VT) Start(title string, steps []timings.Step, est *timings.Estimator, banners bool) {
	v.mu.Lock()
	if v.started {
		v.mu.Unlock()
		return
	}
	if est == nil {
		kind := timings.KindUpdate
		est = timings.NewEstimator(kind, steps, nil, config.Defaults().Tunables, v.clk)
	}
	v.title, v.steps, v.est, v.banners = title, steps, est, banners
	for _, s := range steps {
		seen := false
		for _, g := range v.groups {
			seen = seen || g == s.Group
		}
		if !seen {
			v.groups = append(v.groups, s.Group)
		}
	}
	v.t0 = v.clk.Now()
	v.started = true

	date := []seg{{t: fmtDate(v.t0), codes: []string{v.col().Dim}}}
	left := []seg{{t: "  " + v.th.Glyphs.Title + " ", codes: []string{v.col().Accent}}, {t: title, codes: []string{bold, v.col().Accent}}}
	if !v.th.DateTopRight {
		left = append(left, seg{t: "  " + date[0].t, codes: date[0].codes})
		date = nil
	}
	hdr := v.st.row(left, date, v.width(), " ", nil)
	v.write("\n" + v.pad + hdr + "\n" + esc + "?25l")
	v.write(v.refreshSeq())
	v.syncExternalLocked()
	if !v.opts.NoTicker {
		v.stop, v.done = make(chan struct{}), make(chan struct{})
		go v.tickLoop(v.stop, v.done)
	}
	v.mu.Unlock()
}

func (v *VT) tickLoop(stop, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(v.opts.tickEvery())
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			v.Tick()
		}
	}
}

// ---- footer

func (v *VT) footer() []string {
	st, th, c := v.st, v.th, v.col()
	w := v.width()
	now := v.clk.Now()
	elapsed := now.Sub(v.t0).Seconds()
	frac := v.est.Fraction()
	finished, okp := v.result()
	waiting := v.waitLabel != ""

	// 1. rule
	rule := st.row(
		[]seg{{t: " "}, {t: th.Glyphs.RuleThin + th.Glyphs.RuleThin + " ", codes: []string{c.Faint}}, {t: v.title, codes: []string{c.Dim}}, {t: " "}},
		[]seg{{t: " started " + fmtClock(v.t0), codes: []string{c.Dim}}},
		w, th.Glyphs.RuleThin, []string{c.Faint})

	// 2. bar; the status sits in a fixed-width field so the bar keeps one length
	var status, state string
	switch {
	case finished && okTrue(okp):
		status, state = "done in "+fmtDur(elapsed), "ok"
	case finished && okp != nil:
		status, state = "stopped after "+fmtDur(elapsed), "stopped"
	case waiting:
		status, state = fmtDur(elapsed)+" elapsed · paused", "paused"
	default:
		state = "ok"
		status = fmtDur(elapsed) + " elapsed · " + fmtLeft(v.est.Remaining().Seconds())
		if v.est.RoughNote("") != "" {
			status += " (rough)"
		}
		if eta, ok := v.est.FinishETA(); ok {
			for _, tail := range []string{" · done by " + fmtClock(eta), " · " + fmtClock(eta)} {
				if runeLen(status+tail) <= th.StatusWidth {
					status += tail
					break
				}
			}
		}
	}
	statusCol := c.Soft
	switch state {
	case "stopped":
		statusCol = c.Soft
	}
	status = padLeft(status, th.StatusWidth)
	pct := fmt.Sprintf("%3d%%", int(frac*100))
	barW := w - runeLen(status) - runeLen(pct) - 5
	if barW < 10 {
		barW = 10
	}
	bar := []seg{{t: " "}}
	bar = append(bar, v.barSegs(frac, barW, state)...)
	bar = append(bar, seg{t: "  " + pct, codes: []string{bold}}, seg{t: "  " + status, codes: []string{statusCol}})
	barLine := st.fit(bar, w)

	// 3. current step
	var step string
	cur, hasCur := v.est.Current()
	switch {
	case waiting:
		extra := ""
		if !v.waitDeadline.IsZero() {
			left := int(math.Max(0, v.waitDeadline.Sub(now).Seconds()))
			extra = fmt.Sprintf("  %s %ds", v.countdown, left)
		}
		step = st.fit([]seg{{t: " " + th.Glyphs.Active + " ", codes: []string{c.Warn, bold}}, {t: v.waitLabel, codes: []string{c.Warn}}, {t: extra, codes: []string{c.Soft}}}, w)
	case hasCur:
		step = v.stepLine(cur, now, w)
	case finished:
		g, label, col := th.Glyphs.OK, "Finished", c.OK
		if !okTrue(okp) {
			g, label, col = th.Glyphs.Err, "Stopped: see the messages above", c.Err
		}
		step = st.fit([]seg{{t: " " + g + " ", codes: []string{col, bold}}, {t: label}}, w)
	default:
		step = st.fit([]seg{{t: " " + th.Glyphs.Pending + " ", codes: []string{c.Dim}}, {t: "Starting…", codes: []string{c.Dim}}}, w)
	}

	// 4. stages
	segs := []seg{{t: " "}}
	current := 0
	for i, g := range v.groups {
		var active, done, skipped, pending int
		for _, s := range v.steps {
			if s.Group != g {
				continue
			}
			switch v.est.State(s.Key) {
			case timings.Active:
				active++
			case timings.Done:
				done++
			case timings.Skipped:
				skipped++
			default:
				pending++
			}
		}
		var mark string
		var codes []string
		switch {
		case active > 0:
			mark, codes, current = th.Glyphs.Active+" ", []string{v.st.hue(g, false), bold}, i+1
		case pending == 0 && done > 0:
			mark, codes = th.Glyphs.OK+" ", []string{c.OK}
		case pending == 0 && done == 0:
			mark, codes = th.Glyphs.Sep+" ", []string{c.Faint}
		default:
			mark, codes = th.Glyphs.Pending+" ", []string{c.Dim}
			if !v.banners { // the check has no banners: show the group icons here
				mark = st.icon(g) + " "
			}
		}
		if i > 0 {
			segs = append(segs, seg{t: " " + th.Glyphs.RuleThin + th.Glyphs.RuleThin + " ", codes: []string{c.Faint}})
		}
		segs = append(segs, seg{t: mark + g, codes: codes})
	}
	where := ""
	switch {
	case current > 0:
		where = fmt.Sprintf("stage %d of %d", current, len(v.groups))
	case finished && okTrue(okp):
		where = "all stages done"
	}
	var groups string
	if where != "" {
		groups = st.row(segs, []seg{{t: " " + where, codes: []string{c.Soft}}}, w, " ", nil)
	} else {
		groups = st.fit(segs, w)
	}
	return []string{"", rule, barLine, step, groups}
}

func (v *VT) stepLine(cur timings.Step, now time.Time, w int) string {
	c := v.col()
	est := v.est.Estimate(cur.Key).Seconds()
	el := v.est.Elapsed(cur.Key).Seconds()
	n, idx := 0, 0
	for _, s := range v.steps {
		if (s.DefaultS > 0 && !v.est.ExpectedSkip(s.Key) && v.est.State(s.Key) != timings.Skipped) || s.Key == cur.Key {
			n++
			if s.Key == cur.Key {
				idx = n
			}
		}
	}
	pos := ""
	if idx > 0 {
		pos = fmt.Sprintf("step %d of %d", idx, n)
	}
	usual, usualCol := "", c.Dim
	if cur.DefaultS > 0 && est >= 5 {
		usual = "  (usually " + fmtDur(est) + ")"
		if el > est*1.3+10 {
			usual, usualCol = "  (usually "+fmtDur(est)+", taking longer this time)", c.Warn
		}
	}
	left := []seg{
		{t: " " + v.th.Glyphs.Active + " ", codes: []string{v.st.hue(cur.Group, false), bold}},
		{t: cur.Label, codes: []string{bold}},
		{t: "  " + fmtDur(el)},
		{t: usual, codes: []string{usualCol}},
		{t: " "},
	}
	if pos == "" {
		return v.st.fit(left, w)
	}
	return v.st.row(left, []seg{{t: " " + pos, codes: []string{c.Soft}}}, w, " ", nil)
}

// barSegs returns the filled cells and the track. state "ok" paints the
// gradient in bands of theme.bar_band cells (few colour runs: conhost draws a
// hairline between runs); "paused" and "stopped" are one solid run.
func (v *VT) barSegs(frac float64, barW int, state string) []seg {
	filled := int(math.RoundToEven(frac * float64(barW)))
	if filled > barW {
		filled = barW
	}
	bar := v.th.Glyphs.Bar
	var out []seg
	switch state {
	case "ok":
		band := v.th.BarBand
		if band < 1 {
			band = 1
		}
		for i := 0; i < filled; i += band {
			n := band
			if filled-i < n {
				n = filled - i
			}
			out = append(out, seg{t: strings.Repeat(bar, n), codes: []string{v.st.barColour(i, barW)}})
		}
	default:
		col := v.col().Warn
		if state == "stopped" {
			col = v.col().Err
		}
		if filled > 0 {
			out = append(out, seg{t: strings.Repeat(bar, filled), codes: []string{col}})
		}
	}
	return append(out, seg{t: strings.Repeat(bar, barW-filled), codes: []string{v.col().Track}})
}

// Footer protocol: the footer is always the last `drawn` lines and the cursor
// sits at the end of its last line (no trailing newline). A redraw goes back to
// its first row and rewrites every line; print() erases it, writes above, and
// draws it again. After a resize the console re-wraps old lines, so the number
// of rows to go up is recomputed from the drawn line lengths at the current
// width.
func (v *VT) rowsUp() int {
	cols := v.cols()
	if cols < 1 {
		cols = 1
	}
	if len(v.drawnLens) != v.drawn {
		if v.drawn > 0 {
			return v.drawn - 1
		}
		return 0
	}
	rows := 0
	for _, n := range v.drawnLens {
		r := (n + cols - 1) / cols
		if r < 1 {
			r = 1
		}
		rows += r
	}
	if rows < 1 {
		return 0
	}
	return rows - 1
}

func (v *VT) upSeq() string {
	up := v.rowsUp()
	s := "\r"
	if up > 0 {
		s += fmt.Sprintf("%s%dA", esc, up)
	}
	return s + esc + "J"
}

// refreshSeq redraws the footer in place and returns the bytes to write.
func (v *VT) refreshSeq() string {
	if !v.started || v.closed {
		return ""
	}
	_, _ = v.con().EnableVT() // a child process may have reset the console mode; the restore of the first call is kept by New
	lines := v.footer()
	var b strings.Builder
	b.WriteString(esc + "?25l") // children (git, npm) can turn the cursor back on
	if v.drawn > 0 {
		b.WriteString(v.upSeq())
	}
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(v.pad + l + esc + "K")
	}
	v.drawn = len(lines)
	v.drawnLens = v.drawnLens[:0]
	for _, l := range lines {
		v.drawnLens = append(v.drawnLens, len(v.pad)+visibleLen(l))
	}
	return b.String()
}

func (v *VT) clearSeq() string {
	if v.drawn == 0 {
		return ""
	}
	s := v.upSeq()
	v.drawn, v.drawnLens = 0, v.drawnLens[:0]
	return s
}

// printLocked writes text (possibly several lines) above the footer.
func (v *VT) printLocked(text string) {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = v.pad + lines[i]
	}
	body := strings.Join(lines, "\n") + "\n"
	if !v.started || v.closed {
		v.write(body)
		return
	}
	v.write(v.clearSeq() + body + v.refreshSeq())
}

// ---- state changes

func (v *VT) Stage(key string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.est == nil {
		return
	}
	prevGroup := ""
	if c, ok := v.est.Current(); ok {
		prevGroup = c.Group
	}
	v.est.Stage(key)
	if v.waitLabel != "" {
		v.waitLabel, v.waitDeadline = "", time.Time{}
	}
	cur, ok := v.est.Current()
	if !ok || cur.Key != key {
		v.write(v.refreshSeq())
		return
	}
	if v.banners && v.started && cur.Group != prevGroup {
		g := cur.Group
		n := 1
		for i, x := range v.groups {
			if x == g {
				n = i + 1
			}
		}
		hue := v.st.hue(g, false)
		banner := v.st.row(
			[]seg{{t: fmt.Sprintf(" %d/%d  ", n, len(v.groups)), codes: []string{hue, bold}}, {t: v.st.icon(g) + " " + g, codes: []string{hue, bold}}, {t: " "}},
			[]seg{{t: " " + fmtClockSec(v.clk.Now()), codes: []string{v.col().Dim}}},
			v.width(), v.th.Glyphs.Rule, []string{v.st.hue(g, true)})
		v.printLocked("\n" + banner)
		return
	}
	v.write(v.refreshSeq())
}

func (v *VT) sectionIcon(title string) string {
	best, icon := -1, v.th.Glyphs.Section
	for prefix, ic := range v.th.SectionIcons {
		if strings.HasPrefix(title, prefix) && len(prefix) > best {
			best, icon = len(prefix), ic
		}
	}
	return icon
}

func (v *VT) Section(title string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.banners && v.started && title == "Pre-flight" {
		return // the banner already says it
	}
	g := v.group()
	hue := v.st.hue(g, false)
	row := v.st.row(
		[]seg{{t: " " + v.sectionIcon(title) + " ", codes: []string{hue, bold}}, {t: title, codes: []string{hue, bold}}, {t: " "}},
		[]seg{{t: " " + fmtClockSec(v.clk.Now()), codes: []string{v.col().Dim}}},
		v.width(), v.th.Glyphs.RuleThin, []string{v.st.hue(g, true)})
	v.printLocked("\n" + row)
}

func trimPrefixFold(s, prefix string) string {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return strings.TrimSpace(s[len(prefix):])
	}
	return s
}

func (v *VT) Line(level Level, text string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c, g := v.col(), v.th.Glyphs
	var out string
	switch level {
	case LevelNote:
		out = "   " + v.st.sgr(g.Info+" ", c.Accent, bold) + strings.TrimSpace(strings.TrimPrefix(text, "note:"))
	case LevelWarn:
		v.warnings++
		out = "   " + v.st.sgr(g.Warn+" ", c.Warn, bold) + v.st.sgr(trimPrefixFold(text, "WARN"), c.Warn)
	case LevelStop:
		out = "   " + v.st.sgr(g.Err+" "+trimPrefixFold(text, "STOP"), c.Err, bold)
	case LevelOK:
		out = "   " + v.st.sgr(g.OK+" "+text, c.OK, bold)
	case LevelResult:
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "RESULT:"))
		if strings.HasPrefix(t, "OK") {
			out = v.st.fit([]seg{{t: "   " + g.OK + " ", codes: []string{c.OK, bold}}, {t: strings.Trim(t, ": "), codes: []string{c.OK, bold}}}, v.width())
		} else {
			out = "   " + v.st.sgr(g.Err+" "+t, c.Err, bold)
		}
	default:
		out = v.st.free(text)
	}
	v.printLocked(out)
}

func (v *VT) Rows(rows ...Row) {
	if len(rows) == 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = v.st.kv(r)
	}
	v.printLocked(strings.Join(lines, "\n"))
}

// marks re-mark glyphs the console font lacks (display only; the log keeps
// the raw text).
type mark struct {
	src, dst string
	colour   string
}

func (v *VT) marks() []mark {
	g, c := v.th.Glyphs, v.col()
	return []mark{
		{"✓", g.OK, c.OK}, {"✔", g.OK, c.OK}, {"✗", g.Err, c.Err}, {"✘", g.Err, c.Err},
		{"⚠️", g.Warn, c.Warn}, {"⚠", g.Warn, c.Warn}, {"ℹ", g.Info, c.Dim},
		{"☤", g.Command, c.Accent}, {"⚕", g.Command, c.Accent},
	}
}

func hiddenOutput(line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	for _, h := range hermes.HiddenUpdateLines {
		if strings.Contains(line, h) {
			return true
		}
	}
	return strings.Contains(line, hermes.CuaManualHint)
}

func (v *VT) Output(line string) {
	if hiddenOutput(line) {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	c, g := v.col(), v.th.Glyphs
	marks := v.marks()
	body := strings.TrimRight(line, "\r\n")
	for _, m := range marks {
		if strings.Contains(body, m.src) && !strings.HasPrefix(strings.TrimLeft(body, " |"), m.src) {
			body = strings.ReplaceAll(body, m.src, m.dst)
		}
	}
	grp := v.group()
	gutter := v.st.sgr("   "+g.Gutter+" ", v.st.hue(grp, true))
	indent := len(body) - len(strings.TrimLeft(body, " \t"))
	text := strings.TrimSpace(body)
	if m := hermes.GCLockedRe.FindStringSubmatch(text); m != nil {
		v.printLocked(gutter + strings.Repeat(" ", indent) + v.st.sgr(g.Info+" ", c.Dim, bold) +
			v.st.sgr("old dependency set "+m[1]+" still in use; cleaned up after verify", c.Soft))
		return
	}
	for _, m := range marks {
		if strings.HasPrefix(text, m.src) {
			text = v.st.sgr(m.dst, m.colour, bold) + text[len(m.src):]
			break
		}
	}
	if strings.HasPrefix(text, g.Arrow) {
		text = v.st.sgr(g.Arrow, v.st.hue(grp, false), bold) + v.st.sgr(text[len(g.Arrow):], bold)
	}
	var colour []string
	if strings.Contains(body, "Update complete!") || strings.Contains(body, "Code updated!") {
		colour = []string{c.OK, bold}
	}
	if strings.Contains(body, "platform '") && strings.Contains(body, "toolset") {
		colour = []string{c.Dim}
	}
	if len(colour) > 0 {
		text = v.st.sgr(text, colour...)
	}
	v.printLocked(gutter + strings.Repeat(" ", indent) + text)
}

func (v *VT) Command(argv string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c := v.col()
	v.printLocked("   " + v.st.sgr(v.th.Glyphs.Command+" ", v.st.hue(v.group(), false), bold) +
		v.st.sgr("command  ", c.Label) + v.st.sgr(argv, c.Soft))
}

func (v *VT) waitLocked(label string, deadline time.Time, countdown string) {
	if countdown == "" {
		countdown = "auto-cancel in"
	}
	v.waitLabel, v.waitDeadline, v.countdown = label, deadline, countdown
	if v.est != nil {
		v.est.SetWaiting(label != "")
	}
	if label != "" {
		v.flashLocked()
	}
	v.write(v.refreshSeq())
}

func (v *VT) flashLocked() {
	if v.th.Flash && !v.con().IsForeground() {
		v.con().Flash()
	}
}

func (v *VT) Waiting(label string, deadline time.Time, countdownLabel string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.waitLocked(label, deadline, countdownLabel)
}

func (v *VT) Prompt(ctx context.Context, p PromptSpec) (Answer, error) {
	label := p.CountdownLabel
	if label == "" {
		label = "auto-cancel in"
	}
	var deadline time.Time
	if p.Timeout > 0 {
		deadline = v.clk.Now().Add(p.Timeout)
	}
	v.mu.Lock()
	c := v.col()
	lines := wrapWords(p.Text, maxInt(20, v.width()-5))
	out := []string{""}
	for i, t := range lines {
		if i == 0 {
			out = append(out, "   "+v.st.sgr("? ", c.Prompt)+v.st.sgr(t, bold))
		} else {
			out = append(out, "     "+v.st.sgr(t, bold))
		}
	}
	v.printLocked(strings.Join(out, "\n"))
	v.waitLocked(waitingLabel, deadline, label)
	v.mu.Unlock()
	defer v.Waiting("", time.Time{}, "")
	return readAnswer(ctx, v.con(), p)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// readAnswer reads keys until one of spec.Keys, Ctrl+C (if allowed), the
// timeout or ctx. Arrow and function keys are ignored (D3).
func readAnswer(ctx context.Context, con platform.Console, p PromptSpec) (Answer, error) {
	rctx := ctx
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		rctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}
	for {
		k, err := con.ReadKey(rctx)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return Answer{}, cerr
			}
			if rctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
				return Answer{TimedOut: true}, nil
			}
			return Answer{}, err
		}
		if k.CtrlC {
			if p.AbortOnCtrlC {
				return Answer{Abort: true}, nil
			}
			continue
		}
		if k.Other || k.Rune == 0 {
			continue
		}
		for _, want := range p.Keys {
			if k.Rune == want {
				return Answer{Key: k.Rune}, nil
			}
		}
	}
}

func (v *VT) Complete(ok *bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.est != nil {
		v.est.Complete(ok)
	}
	v.waitLabel, v.waitDeadline = "", time.Time{}
	v.flashLocked()
	v.write(v.refreshSeq())
}

func (v *VT) Summary(card Card) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c, g := v.col(), v.th.Glyphs
	m, col := g.Info, c.Accent
	switch {
	case card.OK != nil && *card.OK:
		m, col = g.OK, c.OK
	case card.OK != nil:
		m, col = g.Err, c.Err
	}
	lines := []string{"", "   " + v.st.sgr(m+" "+card.Headline, col, bold)}
	for _, r := range card.Rows {
		lines = append(lines, v.st.kv(r))
	}
	v.printLocked(strings.Join(lines, "\n"))
}

func (v *VT) Warnings() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.warnings
}

// ---- ticker, title, taskbar

func (v *VT) titleText(frac float64) string {
	finished, okp := v.result()
	switch {
	case finished && okTrue(okp):
		return v.title + " · done"
	case finished && okp != nil:
		return v.title + " · stopped"
	case v.waitLabel != "":
		return v.title + " · waiting for you"
	}
	clock := ""
	if eta, ok := v.est.FinishETA(); ok {
		clock = " · done by " + fmtClock(eta)
	}
	rough := ""
	if v.est.RoughNote("") != "" {
		rough = " (rough)"
	}
	return fmt.Sprintf("%s · %d%% · %s%s%s", v.title, int(frac*100), fmtLeft(v.est.Remaining().Seconds()), rough, clock)
}

func (v *VT) progState() platform.ProgressState {
	finished, okp := v.result()
	switch {
	case finished && okTrue(okp):
		return platform.ProgressNormal
	case finished && okp != nil:
		return platform.ProgressError
	case v.waitLabel != "":
		return platform.ProgressPaused
	}
	return platform.ProgressNormal
}

// syncExternalLocked pushes title and taskbar progress when they changed.
func (v *VT) syncExternalLocked() {
	frac := v.est.Fraction()
	title := v.titleText(frac)
	state := v.progState()
	key := fmt.Sprintf("%d|%d|%s", state, int(frac*1000), title)
	if key == v.lastKey {
		return
	}
	v.lastKey = key
	if v.th.Taskbar && v.plat.Progress != nil {
		v.plat.Progress.Set(state, frac)
	}
	_ = v.con().SetTitle(title)
}

// Tick redraws the footer and refreshes title and taskbar progress. The
// background ticker calls it once a second; tests call it by hand.
func (v *VT) Tick() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.started || v.closed {
		return
	}
	v.write(v.refreshSeq())
	v.syncExternalLocked()
}

func (v *VT) Close() {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	stop, done := v.stop, v.done
	v.mu.Unlock()
	if stop != nil {
		v.stopOnce.Do(func() { close(stop) })
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	if v.started {
		_ = v.con().SetTitle(v.titleText(1))
		v.write(v.refreshSeq())
		v.write("\n" + esc + "?25h" + reset + "\n")
		if v.th.Taskbar && v.plat.Progress != nil {
			final := platform.ProgressNone
			if finished, okp := v.result(); finished && okp != nil {
				final = platform.ProgressError
				if *okp {
					final = platform.ProgressNormal
				}
			}
			v.plat.Progress.Set(final, 1.0) // the full bar stays while the window waits
			v.plat.Progress.Close()
		}
	}
	v.closed = true
	if v.opts.Restore != nil {
		v.opts.Restore()
	}
}
