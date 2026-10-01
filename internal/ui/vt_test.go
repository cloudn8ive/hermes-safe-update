package ui

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

var sgrRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func strip(s string) string { return sgrRe.ReplaceAllString(s, "") }

type harness struct {
	t    *testing.T
	buf  *bytes.Buffer
	plat *platform.Platform
	con  *platform.FakeConsole
	prog *platform.FakeProgress
	clk  *testutil.Clock
	r    *VT
	est  *timings.Estimator
}

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// newHarness builds a VT renderer on fakes, not started.
func newHarness(t *testing.T, cols int, color bool) *harness {
	t.Helper()
	h := &harness{t: t, buf: &bytes.Buffer{}, clk: testutil.NewClock(t0)}
	h.plat = platform.NewFake()
	h.con = h.plat.Console.(*platform.FakeConsole)
	h.con.Cols, h.con.Rows = cols, 30
	h.con.Terminal = true
	h.prog = h.plat.Progress.(*platform.FakeProgress)
	cfg := config.Defaults()
	h.r = NewVT(h.buf, h.plat, cfg.Theme, Options{Clock: h.clk, NoTicker: true, Color: color})
	return h
}

func (h *harness) startUpdate(banners bool) {
	tun := config.Defaults().Tunables
	h.est = timings.NewEstimator(timings.KindUpdate, timings.UpdateSteps(tun.StepSeconds), nil, tun, h.clk)
	h.r.Start("Hermes Safe Update", h.est.Steps, h.est, banners)
}

func (h *harness) startCheck() {
	tun := config.Defaults().Tunables
	h.est = timings.NewEstimator(timings.KindCheck, timings.CheckSteps(tun.StepSeconds), nil, tun, h.clk)
	h.r.Start("Hermes Update Check", h.est.Steps, h.est, false)
}

func (h *harness) out() string { return h.buf.String() }

func (h *harness) mark() int { return h.buf.Len() }
func (h *harness) since(m int) string {
	return h.buf.String()[m:]
}

func TestHeaderHasDateOnlyTopRightAndHidesCursor(t *testing.T) {
	for _, cols := range []int{80, 120} {
		h := newHarness(t, cols, true)
		h.startUpdate(true)
		lines := strings.Split(strip(h.out()), "\n")
		if lines[0] != "" {
			t.Fatalf("cols %d: first line must be blank, got %q", cols, lines[0])
		}
		hdr := lines[1]
		w := cols - 1 - 4
		if got := runeLen(hdr); got != 2+w {
			t.Errorf("cols %d: header width = %d, want %d: %q", cols, got, 2+w, hdr)
		}
		if !strings.HasPrefix(hdr, "    ♦ Hermes Safe Update") || !strings.HasSuffix(hdr, "Thu 1 Oct") {
			t.Errorf("cols %d: header = %q", cols, hdr)
		}
		if !strings.Contains(h.out(), "\x1b[?25l") {
			t.Error("cursor not hidden")
		}
		if strings.Contains(hdr, "09:00") || strings.Contains(hdr, "AM") {
			t.Errorf("header must show the date only: %q", hdr)
		}
	}
}

func TestFooterHasFiveLinesWithinWidth(t *testing.T) {
	for _, cols := range []int{80, 120} {
		h := newHarness(t, cols, true)
		h.startUpdate(true)
		h.r.Stage("remote")
		lines := h.r.footer()
		if len(lines) != 5 {
			t.Fatalf("footer lines = %d", len(lines))
		}
		w := cols - 5
		if lines[0] != "" {
			t.Errorf("first footer line must be blank: %q", lines[0])
		}
		for i, l := range lines[1:] {
			if n := visibleLen(l); n > w {
				t.Errorf("cols %d line %d is %d wide (max %d): %q", cols, i+1, n, w, strip(l))
			}
		}
		if n := visibleLen(lines[2]); n != w {
			t.Errorf("cols %d: bar line is %d wide, want exactly %d", cols, n, w)
		}
		if got := strip(lines[1]); !strings.HasPrefix(got, " ── Hermes Safe Update ──") || !strings.HasSuffix(got, " started 9:00 AM") {
			t.Errorf("rule = %q", got)
		}
	}
}

func TestBarLineStatusFieldIsFixedWidthAndRightJustified(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("remote")
	bar := strip(h.r.footer()[2])
	// " <bar>  <pct>  <status 50>"
	i := strings.Index(bar, "  0%")
	if i < 0 {
		t.Fatalf("no pct in %q", bar)
	}
	status := bar[i+len("  0%")+2:]
	if runeLen(status) != 50 {
		t.Errorf("status field is %d columns: %q", runeLen(status), status)
	}
	if !strings.HasPrefix(strings.TrimLeft(status, " "), "0:00 elapsed · about 10 min left (rough) · 9:10 AM") {
		t.Errorf("status = %q", status)
	}
	if status != strings.TrimLeft(status, " ") && !strings.HasPrefix(status, " ") {
		t.Errorf("status must be right-justified: %q", status)
	}
}

func TestBarGradientInBandsOfSixCells(t *testing.T) {
	h := newHarness(t, 100, true)
	segs := h.r.barSegs(14.0/30.0, 30, "ok")
	var runs []string
	for _, s := range segs {
		runs = append(runs, strings.Repeat("█", 0)+itoa(runeLen(s.t))+":"+strings.Join(s.codes, ","))
	}
	want := []string{
		"6:" + h.r.st.barColour(0, 30), "6:" + h.r.st.barColour(6, 30), "2:" + h.r.st.barColour(12, 30),
		"16:38;5;236",
	}
	if strings.Join(runs, " ") != strings.Join(want, " ") {
		t.Errorf("runs = %v\nwant   %v", runs, want)
	}
	for _, s := range segs {
		if strings.Trim(s.t, "█") != "" {
			t.Errorf("bar cells must be full blocks: %q", s.t)
		}
	}
}

func TestBarSolidAmberWhenPausedAndRedWhenStopped(t *testing.T) {
	h := newHarness(t, 100, true)
	for state, want := range map[string]string{"paused": "38;5;221", "stopped": "38;5;203"} {
		segs := h.r.barSegs(0.5, 20, state)
		if len(segs) != 2 || runeLen(segs[0].t) != 10 || segs[0].codes[0] != want {
			t.Errorf("%s: %+v", state, segs)
		}
	}
}

func TestBarFilledUsesBankersRounding(t *testing.T) {
	h := newHarness(t, 100, true)
	// 0.25 * 10 = 2.5 -> 2 (half to even); 0.35*10=3.5 -> 4
	if n := runeLen(h.r.barSegs(0.25, 10, "ok")[0].t); n != 2 {
		t.Errorf("filled(2.5) = %d, want 2", n)
	}
	if n := runeLen(h.r.barSegs(0.35, 10, "ok")[0].t); n != 4 {
		t.Errorf("filled(3.5) = %d, want 4", n)
	}
}

func TestStepLineShowsLabelElapsedUsualAndPosition(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("remote")
	h.clk.Advance(32 * time.Second)
	step := strip(h.r.footer()[3])
	if !strings.HasPrefix(step, " ► Checking for updates  0:32  (usually 1:15)") {
		t.Errorf("step line = %q", step)
	}
	// "step n of N" counts steps with default > 0 (18 of 20 here: wait has none), local skipped
	if !strings.HasSuffix(step, " step 1 of 18") {
		t.Errorf("position: %q", step)
	}
	if n := runeLen(step); n != 95 {
		t.Errorf("step row width = %d, want 95", n)
	}
}

func TestStepLineWarnsWhenTakingLonger(t *testing.T) {
	h := newHarness(t, 120, true)
	h.startUpdate(true)
	h.r.Stage("remote")
	h.clk.Advance(108 * time.Second) // 75*1.3+10 = 107.5
	raw := h.r.footer()[3]
	if !strings.Contains(strip(raw), "(usually 1:15, taking longer this time)") {
		t.Errorf("step line = %q", strip(raw))
	}
	if !strings.Contains(raw, "\x1b[38;5;221m  (usually 1:15, taking longer this time)") {
		t.Errorf("note must be amber: %q", raw)
	}
	h.clk.Advance(-2 * time.Second)
}

func TestShortStepsShowNoUsualNote(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("local") // default 1 s
	if s := strip(h.r.footer()[3]); strings.Contains(s, "usually") {
		t.Errorf("short step must not show an estimate: %q", s)
	}
}

func TestBeforeFirstStageAndWhenFinished(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	if s := strip(h.r.footer()[3]); s != " ○ Starting…" {
		t.Errorf("before first stage: %q", s)
	}
	h.r.Stage("remote")
	ok := true
	h.r.Complete(&ok)
	f := h.r.footer()
	if s := strip(f[3]); s != " √ Finished" {
		t.Errorf("finished: %q", s)
	}
	if s := strip(f[4]); !strings.HasSuffix(s, " all stages done") {
		t.Errorf("stages line: %q", s)
	}
	if s := strip(f[2]); !strings.Contains(s, "100%") || !strings.Contains(s, "done in 0:00") {
		t.Errorf("bar line: %q", s)
	}
	bad := h2(t, false)
	if s := strip(bad.footer()[3]); s != " × Stopped: see the messages above" {
		t.Errorf("stopped: %q", s)
	}
	if s := strip(bad.footer()[2]); !strings.Contains(s, "stopped after 0:00") {
		t.Errorf("stopped bar: %q", s)
	}
}

// h2 returns a started renderer completed with ok.
func h2(t *testing.T, ok bool) *VT {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("remote")
	h.r.Complete(&ok)
	return h.r
}

func TestStagesLineMarkersAndPosition(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("pull")
	line := strip(h.r.footer()[4])
	// Check and Close Hermes were skipped without ever running -> faint dot; Update is current.
	want := " · Check ── · Close Hermes ── ► Update ── ○ Build ── ○ Finish"
	if !strings.HasPrefix(line, want) || !strings.HasSuffix(line, " stage 3 of 5") {
		t.Errorf("stages = %q", line)
	}
	h.r.Stage("settings")
	if line := strip(h.r.footer()[4]); !strings.Contains(line, "√ Update ── · Build ── ► Finish") {
		t.Errorf("stages = %q", line)
	}
}

func TestStagesLineInCheckRunShowsGroupIconsForPending(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startCheck()
	h.r.Stage("procs")
	line := strip(h.r.footer()[4])
	want := " · Local ── · Updates ── ► Hermes ── ♣ Housekeeping"
	if !strings.HasPrefix(line, want) {
		t.Errorf("stages = %q", line)
	}
}

func TestWaitingFooterShowsCountdownAndPausedBar(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("wait")
	h.r.Waiting("Waiting for your answer", h.clk.Now().Add(30*time.Second), "auto-cancel in")
	f := h.r.footer()
	if s := strip(f[3]); s != " ► Waiting for your answer  auto-cancel in 30s" {
		t.Errorf("step: %q", s)
	}
	if s := strip(f[2]); !strings.Contains(s, "0:00 elapsed · paused") {
		t.Errorf("bar: %q", s)
	}
	h.clk.Advance(12*time.Second + 400*time.Millisecond)
	if s := strip(h.r.footer()[3]); !strings.HasSuffix(s, "auto-cancel in 17s") {
		t.Errorf("countdown truncates: %q", s)
	}
	h.r.Waiting("", time.Time{}, "")
	if s := strip(h.r.footer()[3]); strings.Contains(s, "auto-cancel") {
		t.Errorf("wait not cleared: %q", s)
	}
}

func TestWaitingFlashesOnlyWhenNotForeground(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Waiting("x", time.Time{}, "")
	if h.con.Flashes != 0 {
		t.Errorf("flashed in the foreground")
	}
	h.con.Background = true
	h.r.Waiting("x", time.Time{}, "")
	if h.con.Flashes != 1 {
		t.Errorf("flashes = %d", h.con.Flashes)
	}
}

func TestFooterRedrawProtocol(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	m := h.mark()
	h.r.Tick()
	got := h.since(m)
	if !strings.HasPrefix(got, "\x1b[?25l\r\x1b[4A\x1b[J") {
		t.Errorf("redraw must hide the cursor, go up 4 rows and clear: %q", got[:min(40, len(got))])
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("footer must not end with a newline")
	}
	if n := strings.Count(got, "\x1b[K"); n != 5 {
		t.Errorf("each footer line ends with ESC[K, got %d", n)
	}
	for _, l := range strings.Split(got, "\n")[1:] {
		if !strings.HasPrefix(l, "  ") {
			t.Errorf("footer line not indented by the margin: %q", l)
		}
	}
}

func TestPrintClearsFooterWritesAboveAndRedraws(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	m := h.mark()
	h.r.Line(LevelNote, "hello there")
	got := h.since(m)
	if !strings.HasPrefix(got, "\r\x1b[4A\x1b[J     "+h.r.st.sgr("i ", "38;5;80", "1")+"hello there\n\x1b[?25l") {
		t.Errorf("print sequence: %q", got)
	}
}

func TestResizeRecomputesRowsUpFromDrawnLengths(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("remote")
	h.con.Cols = 60 // lines drawn at 97 columns now wrap to 2 rows each; the blank row stays 1
	m := h.mark()
	h.r.Tick()
	got := h.since(m)
	if !strings.HasPrefix(got, "\x1b[?25l\r\x1b[8A\x1b[J") {
		t.Errorf("want 8 rows up (1+2+2+2+2-1), got %q", got[:min(30, len(got))])
	}
	// and the new footer is laid out for 60 columns: 55 usable
	for _, l := range h.r.footer()[1:] {
		if visibleLen(l) > 55 {
			t.Errorf("line still too wide after resize: %d", visibleLen(l))
		}
	}
	// a further redraw at the same width is back to 4 up
	m = h.mark()
	h.r.Tick()
	if got := h.since(m); !strings.HasPrefix(got, "\x1b[?25l\r\x1b[4A") {
		t.Errorf("after resize: %q", got[:min(30, len(got))])
	}
}

func TestWidthIsClampedBetween40AndMax(t *testing.T) {
	h := newHarness(t, 30, true)
	if w := h.r.width(); w != 40 {
		t.Errorf("min width = %d", w)
	}
	h.con.Cols = 1000
	if w := h.r.width(); w != 400 {
		t.Errorf("max width = %d", w)
	}
	h.con.Cols = 0 // unknown: Python default 100
	if w := h.r.width(); w != 95 {
		t.Errorf("default width = %d", w)
	}
}

func TestStageBannerNumberedWithHueAndStamp(t *testing.T) {
	h := newHarness(t, 80, true)
	h.startUpdate(true)
	m := h.mark()
	h.r.Stage("remote")
	got := h.since(m)
	if !strings.Contains(got, "\x1b[38;5;80;1m 1/5  \x1b[0m\x1b[38;5;80;1m◊ Check\x1b[0m") {
		t.Errorf("banner head: %q", got)
	}
	if !strings.Contains(got, "\x1b[38;5;30m━") {
		t.Errorf("rule must use the dark hue: %q", got)
	}
	plain := strip(got)
	if !strings.Contains(plain, "\n   1/5  ◊ Check ━") || !strings.Contains(plain, "━ 9:00:00 AM\n") {
		t.Errorf("banner = %q", plain)
	}
	m = h.mark()
	h.r.Stage("procs") // same group: no banner
	if strings.Contains(h.since(m), "━") {
		t.Error("banner printed within the same group")
	}
	m = h.mark()
	h.r.Stage("close")
	if p := strip(h.since(m)); !strings.Contains(p, "2/5  ■ Close Hermes ━") {
		t.Errorf("second banner: %q", p)
	}
}

func TestNoBannersInCheckRun(t *testing.T) {
	h := newHarness(t, 80, true)
	h.startCheck()
	m := h.mark()
	h.r.Stage("remote")
	if strings.Contains(h.since(m), "━") {
		t.Error("check run printed a banner")
	}
}

func TestSectionRuleAndIcons(t *testing.T) {
	h := newHarness(t, 80, true)
	h.startUpdate(true)
	h.r.Stage("verify")
	for title, icon := range map[string]string{"Verify": "√", "Close the desktop": "■", "Relaunch": "↑", "Dependency generation cleanup": "♣", "cua-driver (Computer Use) refresh": "►", "Something else": "▸"} {
		m := h.mark()
		h.r.Section(title)
		got := strip(h.since(m))
		if !strings.Contains(got, "\n   "+icon+" "+title+" ─") {
			t.Errorf("section %q: %q", title, got)
		}
	}
	m := h.mark()
	h.r.Section("Pre-flight") // banners on: the banner already says it
	if strings.Contains(strip(h.since(m)), "Pre-flight ─") {
		t.Error("Pre-flight section must be suppressed with banners")
	}
	h = newHarness(t, 80, true)
	h.startCheck()
	m = h.mark()
	h.r.Section("Pre-flight")
	if !strings.Contains(strip(h.since(m)), "◊ Pre-flight ─") {
		t.Error("Pre-flight section is shown without banners")
	}
}

func TestLevelsRenderLikeTheTunedLook(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	cases := []struct {
		level Level
		text  string
		want  string
	}{
		{LevelNote, "no hurry", "   " + "\x1b[38;5;80;1mi \x1b[0m" + "no hurry"},
		{LevelWarn, "disk is low", "   \x1b[38;5;221;1m! \x1b[0m\x1b[38;5;221mdisk is low\x1b[0m"},
		{LevelStop, "STOP busy", "   \x1b[38;5;203;1m× busy\x1b[0m"},
		{LevelStop, "busy", "   \x1b[38;5;203;1m× busy\x1b[0m"},
		{LevelOK, "all good", "   \x1b[38;5;78;1m√ all good\x1b[0m"},
		{LevelResult, "OK", "\x1b[38;5;78;1m   √ \x1b[0m\x1b[38;5;78;1mOK\x1b[0m"},
		{LevelResult, "OK: verified", "\x1b[38;5;78;1m   √ \x1b[0m\x1b[38;5;78;1mOK: verified\x1b[0m"},
		{LevelResult, "PROBLEM: update failed", "   \x1b[38;5;203;1m× PROBLEM: update failed\x1b[0m"},
		{LevelInfo, "  indented text 12 GB", "     indented text \x1b[1m12 GB\x1b[0m"},
	}
	for _, c := range cases {
		m := h.mark()
		h.r.Line(c.level, c.text)
		got := h.since(m)
		i := strings.Index(got, "\x1b[J")
		line := got[i+3 : strings.Index(got, "\n")]
		if line != "  "+c.want {
			t.Errorf("level %d %q:\n got %q\nwant %q", c.level, c.text, line, "  "+c.want)
		}
	}
	if h.r.Warnings() != 1 {
		t.Errorf("Warnings = %d", h.r.Warnings())
	}
}

func TestCommandRow(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("pull")
	m := h.mark()
	h.r.Command(`hermes update --yes --keep-stash --branch main`)
	got := h.since(m)
	want := "     \x1b[38;5;75;1m» \x1b[0m\x1b[38;5;110mcommand  \x1b[0m\x1b[38;5;250mhermes update --yes --keep-stash --branch main\x1b[0m\n"
	if !strings.Contains(got, strings.TrimPrefix(want, "  ")) {
		t.Errorf("command row = %q", got)
	}
}

func rowOut(h *harness, rows ...Row) string {
	m := h.mark()
	h.r.Rows(rows...)
	got := h.since(m)
	if i := strings.Index(got, "\x1b[J"); i >= 0 {
		got = got[i+3:]
	}
	if i := strings.Index(got, "\x1b[?25l"); i >= 0 {
		got = got[:i]
	}
	return got
}

func TestRowsAlignValuesAtColumn20AndColourDots(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	cases := []struct {
		row   Row
		dot   string
		value string
	}{
		{Row{Key: "dirty", Value: "clean"}, "38;5;78", "clean"},
		{Row{Key: "disk free", Value: "120 GB (enough)"}, "38;5;78", "120 GB (enough)"},
		{Row{Key: "updates", Value: "3 new commits on main"}, "38;5;221", "3 new commits on main"},
		{Row{Key: "gateway", Value: "state is missing"}, "38;5;203", "state is missing"},
		{Row{Key: "sessions", Value: "none active"}, "38;5;78", "none active"},
		{Row{Key: "branch", Value: "main"}, "38;5;242", "main"},
	}
	for _, c := range cases {
		got := rowOut(h, c.row)
		line := strings.TrimRight(strip(got), "\n")
		if !strings.HasPrefix(line, "     ● "+c.row.Key) {
			t.Errorf("%q: %q", c.row.Key, line)
		}
		// 2 margin + 3 + dot + space + 15 label = value at index 2+20
		if idx := runeLen(line[:strings.Index(line, c.value)]); idx != 22 {
			t.Errorf("%q: value starts at col %d, want 22 (incl. margin): %q", c.row.Key, idx, line)
		}
		if !strings.Contains(got, "\x1b["+c.dot+"m● ") {
			t.Errorf("%q: dot colour want %s in %q", c.row.Key, c.dot, got)
		}
	}
}

func TestRowVerdictOverrideBeatsRegex(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	bad := VerdictBad
	got := rowOut(h, Row{Key: "k", Value: "clean", Verdict: &bad})
	if !strings.Contains(got, "\x1b[38;5;203m● ") {
		t.Errorf("override ignored: %q", got)
	}
	attn := VerdictAttention
	got = rowOut(h, Row{Key: "k", Value: "fine", Verdict: &attn})
	if !strings.Contains(got, "\x1b[38;5;221m● ") {
		t.Errorf("override ignored: %q", got)
	}
}

func TestRowKeyTruncatedTo14AndNestedRows(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	got := strip(rowOut(h, Row{Key: "a very long label here", Value: "x"}))
	if !strings.HasPrefix(got, "     ● a very long la x") {
		t.Errorf("long key: %q", got)
	}
	got = strip(rowOut(h, Row{Key: "stale", Value: "x", Nested: true}))
	if !strings.HasPrefix(got, "       stale          x") {
		t.Errorf("nested: %q", got)
	}
	got = strip(rowOut(h, Row{Key: "dependency set abcd1234", Value: "still in use", Nested: true}))
	if !strings.HasPrefix(got, "       dependency set abcd1234           still in use") {
		t.Errorf("long nested label gets a 34-wide column: %q", got)
	}
}

func TestRowContinuationLinesHangAtValueColumn(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	got := strip(rowOut(h, Row{Key: "sessions", Value: "2 active\n  · 20260101_000000_aaaaaa  Drafting\nsecond"}))
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if lines[1] != "                      · 20260101_000000_aaaaaa  Drafting" {
		t.Errorf("continuation = %q", lines[1])
	}
	if lines[2] != "                      · second" {
		t.Errorf("continuation = %q", lines[2])
	}
}

func TestPaintTokensAndNumbers(t *testing.T) {
	st := testStyle(true)
	got := st.paint("v0.21.5+4160 at 1b8569f41f, 3 commits, 12 GB free, 5 min, abc", nil)
	for _, want := range []string{
		"\x1b[38;5;183mv0.21.5+4160\x1b[0m",
		"\x1b[38;5;183m1b8569f41f\x1b[0m",
		"\x1b[1m3\x1b[0m",
		"\x1b[1m12 GB\x1b[0m",
		"\x1b[1m5 min\x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("paint lacks %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[1ma") {
		t.Errorf("letters must not be bold: %q", got)
	}
	// numbers inside words or versions stay plain
	if g := st.paint("abc123 x1.5y", nil); strings.Contains(g, "\x1b[1m") {
		t.Errorf("embedded digits bolded: %q", g)
	}
	if g := st.paint("note 20260101_000000_aaaaaa", nil); !strings.Contains(g, "\x1b[38;5;183m20260101_000000_aaaaaa") {
		t.Errorf("session id: %q", g)
	}
}

func TestOutputGutterRemarkAndHiding(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("pull")
	gutter := "\x1b[38;5;25m   │ \x1b[0m"
	cases := []struct{ in, want string }{
		{"→ Fetching updates", gutter + "\x1b[38;5;75;1m→\x1b[0m\x1b[1m Fetching updates\x1b[0m"},
		{"  ✓ Configuration is up to date", gutter + "  \x1b[38;5;78;1m√\x1b[0m Configuration is up to date"},
		{"  ✗ failed to do it", gutter + "  \x1b[38;5;203;1m×\x1b[0m failed to do it"},
		{"  ⚠ careful ✓ mid", gutter + "  \x1b[38;5;221;1m!\x1b[0m careful √ mid"},
		{"ℹ info", gutter + "\x1b[38;5;245;1mi\x1b[0m info"},
		{"☤ Hermes", gutter + "\x1b[38;5;80;1m»\x1b[0m Hermes"},
		{"✓ Update complete! (v1 → v2)", gutter + "\x1b[38;5;78;1m\x1b[38;5;78;1m√\x1b[0m Update complete! (v1 → v2)\x1b[0m"},
		{"plain line", gutter + "plain line"},
		{"Code updated! 3 files", gutter + "\x1b[38;5;78;1mCode updated! 3 files\x1b[0m"},
		{"Skipping platform 'x' toolset", gutter + "\x1b[38;5;245mSkipping platform 'x' toolset\x1b[0m"},
	}
	for _, c := range cases {
		m := h.mark()
		h.r.Output(c.in)
		got := h.since(m)
		i := strings.Index(got, "\x1b[J")
		line := got[i+3 : strings.Index(got, "\n")]
		if line != "  "+c.want {
			t.Errorf("Output(%q):\n got %q\nwant %q", c.in, line, "  "+c.want)
		}
	}
	for _, hidden := range []string{"", "   ", "Tip: You can now select a provider and model:", "  hermes model              # Select provider and model", "run hermes computer-use install --upgrade later"} {
		m := h.mark()
		h.r.Output(hidden)
		if got := h.since(m); got != "" {
			t.Errorf("Output(%q) must be hidden, wrote %q", hidden, got)
		}
	}
}

func TestOutputGCLockedLineIsRewrittenToGreyNote(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("pull")
	m := h.mark()
	h.r.Output(`  dependency generation cleanup skipped: [WinError 5] Access is denied: 'C:\x\environments\abcd1234'`)
	got := h.since(m)
	want := "\x1b[38;5;25m   │ \x1b[0m  \x1b[38;5;245;1mi \x1b[0m\x1b[38;5;250mold dependency set abcd1234 still in use; cleaned up after verify\x1b[0m\n"
	if !strings.Contains(got, want) {
		t.Errorf("gc line = %q", got)
	}
}

func TestSummaryCard(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	ok, bad := true, false
	cases := []struct {
		ok   *bool
		mark string
		code string
	}{{&ok, "√", "38;5;78"}, {&bad, "×", "38;5;203"}, {nil, "i", "38;5;80"}}
	for _, c := range cases {
		m := h.mark()
		h.r.Summary(Card{OK: c.ok, Headline: "Hermes is updated and verified.", Rows: []Row{
			{Key: "Version", Value: "v1 → v2  (3 commits)"}, {Key: "Warnings", Value: "1 (see above)"},
		}})
		got := h.since(m)
		if !strings.Contains(got, "   \x1b["+c.code+";1m"+c.mark+" Hermes is updated and verified.\x1b[0m\n") {
			t.Errorf("card headline for %v: %q", c.ok, got)
		}
		plain := strip(got)
		if !strings.Contains(plain, "\r  \n     "+c.mark+" Hermes is updated and verified.\n     ● Version        v1 → v2  (3 commits)\n") {
			t.Errorf("blank line above the card / rows: %q", plain)
		}
	}
}

func TestNoColourKeepsLayoutAndDropsAllColours(t *testing.T) {
	h := newHarness(t, 100, false)
	h.startUpdate(true)
	h.r.Stage("remote")
	h.r.Rows(Row{Key: "disk free", Value: "120 GB (enough)"})
	h.r.Output("✓ done")
	h.r.Line(LevelWarn, "careful")
	h.r.Summary(Card{Headline: "x", Rows: []Row{{Key: "a", Value: "b"}}})
	h.clk.Advance(50 * time.Second)
	h.r.Tick()
	if strings.Contains(h.out(), "38;") || strings.Contains(h.out(), "48;") {
		t.Errorf("colour sequences with NO_COLOR: %q", sgrRe.FindAllString(h.out(), -1))
	}
	if !strings.Contains(h.out(), "\x1b[4A") || !strings.Contains(strip(h.out()), "● disk free") {
		t.Error("layout must stay")
	}
	// the bar stays a bar (same glyph, no colour)
	if !strings.Contains(h.out(), "████") && !strings.Contains(h.out(), "█") {
		t.Error("bar missing")
	}
}

func TestOnlyWhitelistedGlyphsAreEmitted(t *testing.T) {
	h := newHarness(t, 100, true)
	scripted(h)
	checkGlyphs(t, h.out())
}

func TestTickUpdatesTitleAndTaskbarOnlyOnChange(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	h.r.Stage("remote")
	h.con.Titles = nil
	h.prog.States, h.prog.Fractions = nil, nil
	h.r.Tick()
	h.r.Tick() // nothing changed
	if len(h.con.Titles) != 0 && len(h.con.Titles) != 1 {
		t.Errorf("titles = %q", h.con.Titles)
	}
	h.clk.Advance(60 * time.Second)
	h.r.Tick()
	last := h.con.Titles[len(h.con.Titles)-1]
	if !strings.HasPrefix(last, "Hermes Safe Update · ") || !strings.Contains(last, "% · about ") || !strings.Contains(last, " · done by ") {
		t.Errorf("title = %q", last)
	}
	if n := len(h.prog.States); n == 0 || h.prog.States[n-1] != platform.ProgressNormal {
		t.Errorf("progress states = %v", h.prog.States)
	}
	h.r.Waiting("Waiting for your answer", time.Time{}, "")
	h.r.Tick()
	if got := h.con.Titles[len(h.con.Titles)-1]; got != "Hermes Safe Update · waiting for you" {
		t.Errorf("title = %q", got)
	}
	if got := h.prog.States[len(h.prog.States)-1]; got != platform.ProgressPaused {
		t.Errorf("state = %v", got)
	}
	h.r.Waiting("", time.Time{}, "")
	ok := false
	h.r.Complete(&ok)
	h.r.Tick()
	if got := h.con.Titles[len(h.con.Titles)-1]; got != "Hermes Safe Update · stopped" {
		t.Errorf("title = %q", got)
	}
	if got := h.prog.States[len(h.prog.States)-1]; got != platform.ProgressError {
		t.Errorf("state = %v", got)
	}
}

func TestTaskbarCanBeDisabledByTheme(t *testing.T) {
	h := newHarness(t, 100, true)
	th := config.Defaults().Theme
	th.Taskbar = false
	h.r = NewVT(h.buf, h.plat, th, Options{Clock: h.clk, NoTicker: true, Color: true})
	h.startUpdate(true)
	h.r.Stage("remote")
	h.r.Tick()
	h.r.Close()
	if len(h.prog.States) != 0 {
		t.Errorf("taskbar used although disabled: %v", h.prog.States)
	}
}

func TestCompleteFlashesWhenInBackgroundAndCloseRestoresConsole(t *testing.T) {
	h := newHarness(t, 100, true)
	h.con.Background = true
	h.startUpdate(true)
	h.r.Stage("remote")
	ok := true
	h.r.Complete(&ok)
	if h.con.Flashes != 1 {
		t.Errorf("flashes = %d", h.con.Flashes)
	}
	h.r.Close()
	if !strings.HasSuffix(h.out(), "\n\x1b[?25h\x1b[0m\n") {
		t.Errorf("close must show the cursor and reset: %q", h.out()[max(0, len(h.out())-30):])
	}
	if !h.prog.Closed || h.prog.States[len(h.prog.States)-1] != platform.ProgressNormal || h.prog.Fractions[len(h.prog.Fractions)-1] != 1 {
		t.Errorf("progress: %v %v closed=%v", h.prog.States, h.prog.Fractions, h.prog.Closed)
	}
	if got := h.con.Titles[len(h.con.Titles)-1]; got != "Hermes Safe Update · done" {
		t.Errorf("final title = %q", got)
	}
	n := h.buf.Len()
	h.r.Close() // idempotent
	h.r.Line(LevelInfo, "late")
	h.r.Tick()
	if got := h.out()[n:]; got != "" && got != "     late\n" {
		t.Errorf("writes after Close must be plain lines only: %q", h.out()[n:])
	}
}

func TestPromptAcceptsOnlyListedKeysAndShowsWaitingRow(t *testing.T) {
	h := newHarness(t, 100, true)
	h.con.Keys = []platform.Key{{Other: true}, {Rune: 'x'}, {Rune: 'Y' + 32}}
	h.startUpdate(true)
	h.r.Stage("wait")
	m := h.mark()
	a, err := h.r.Prompt(context.Background(), PromptSpec{Text: "Close Hermes now and update? Press Y to go on or N to cancel.", Keys: []rune{'y', 'n'}, Timeout: 30 * time.Second, CountdownLabel: "auto-cancel in"})
	if err != nil || a.Key != 'y' || a.TimedOut || a.Abort {
		t.Fatalf("answer = %+v %v", a, err)
	}
	got := h.since(m)
	if !strings.Contains(got, "   \x1b[38;5;221;1m? \x1b[0m\x1b[1mClose Hermes now and update? Press Y to go on or N to cancel.\x1b[0m") {
		t.Errorf("prompt text: %q", got)
	}
	if !strings.Contains(strip(got), "► Waiting for your answer  auto-cancel in 30s") {
		t.Errorf("waiting row missing: %q", strip(got))
	}
	if s := strip(h.r.footer()[3]); strings.Contains(s, "auto-cancel") {
		t.Errorf("wait must be cleared after the answer: %q", s)
	}
	if h.con.Flashes != 0 {
		t.Errorf("foreground flash: %d", h.con.Flashes)
	}
}

func TestPromptWrapsInsideTheMargins(t *testing.T) {
	h := newHarness(t, 60, true)
	h.con.Keys = []platform.Key{{Rune: 'n'}}
	h.startUpdate(true)
	m := h.mark()
	text := "Optional: make future updates faster with a local mirror of the Hermes source (3.1 GB at a folder; one-time download)."
	if _, err := h.r.Prompt(context.Background(), PromptSpec{Text: text, Keys: []rune{'s', 'n'}, Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strip(h.since(m)), "\n") {
		if strings.HasPrefix(l, "   ? ") || strings.HasPrefix(l, "     ") && !strings.Contains(l, "►") && !strings.Contains(l, "██") && strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 3 {
		t.Fatalf("expected a wrapped prompt, got %q", lines)
	}
	for i, l := range lines {
		if runeLen(l) > 2+max(20, 60-1-4-5)+5 {
			t.Errorf("prompt line %d too wide (%d): %q", i, runeLen(l), l)
		}
	}
	if !strings.HasPrefix(lines[1], "       ") {
		t.Errorf("continuation lines start under the text: %q", lines[1])
	}
}

func TestPromptTimeoutAndCtrlCAndContext(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	a, err := h.r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: 30 * time.Millisecond})
	if err != nil || !a.TimedOut {
		t.Errorf("timeout: %+v %v", a, err)
	}
	h.con.Keys = []platform.Key{{CtrlC: true}}
	a, _ = h.r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y'}, AbortOnCtrlC: true, Timeout: time.Second})
	if !a.Abort {
		t.Errorf("ctrl-c: %+v", a)
	}
	// Ctrl+C without AbortOnCtrlC is ignored like any other unlisted key
	h.con.Keys = []platform.Key{{CtrlC: true}, {Rune: 'y'}}
	a, _ = h.r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: time.Second})
	if a.Key != 'y' {
		t.Errorf("ctrl-c without abort: %+v", a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.r.Prompt(ctx, PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: time.Minute}); err == nil {
		t.Error("cancelled context must return an error")
	}
	// Enter and Esc are plain runes
	h.con.Keys = []platform.Key{{Rune: '\r'}}
	a, _ = h.r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'\r', 0x1b}, Timeout: time.Second})
	if a.Key != '\r' {
		t.Errorf("enter: %+v", a)
	}
}

func TestPromptDoesNotEatTheNextKeyAfterReturning(t *testing.T) {
	h := newHarness(t, 100, true)
	h.con.Keys = []platform.Key{{Rune: 'y'}, {Rune: 'n'}}
	h.startUpdate(true)
	a, _ := h.r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y', 'n'}, Timeout: time.Second})
	b, _ := h.r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y', 'n'}, Timeout: time.Second})
	if a.Key != 'y' || b.Key != 'n' {
		t.Errorf("answers %c %c", a.Key, b.Key)
	}
}

func TestConcurrentUseDoesNotDeadlockOrInterleaveFooters(t *testing.T) {
	h := newHarness(t, 100, true)
	h.startUpdate(true)
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				h.r.Output("line")
				h.r.Tick()
				h.r.Rows(Row{Key: "k", Value: "v"})
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("deadlock")
		}
	}
}

func TestRealTickerRunsAndStops(t *testing.T) {
	h := newHarness(t, 100, true)
	h.r = NewVT(h.buf, h.plat, config.Defaults().Theme, Options{Clock: h.clk, Color: true, TickEvery: 5 * time.Millisecond})
	h.startUpdate(true)
	time.Sleep(40 * time.Millisecond)
	h.r.Close()
	h.r.Close()
	n := h.buf.Len()
	time.Sleep(30 * time.Millisecond)
	if h.buf.Len() != n {
		t.Error("ticker kept writing after Close")
	}
}
