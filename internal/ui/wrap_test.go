package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// printed runs f and returns the lines it wrote above the footer, raw (the
// two-column margin and the escape codes are still there).
func printed(h *harness, f func()) []string {
	h.t.Helper()
	m := h.mark()
	f()
	got := h.since(m)
	i := strings.Index(got, "\x1b[J")
	j := strings.LastIndex(got, "\x1b[?25l")
	if i < 0 || j < i {
		h.t.Fatalf("no printed block in %q", got)
	}
	return strings.Split(strings.TrimSuffix(got[i+3:j], "\n"), "\n")
}

func stripped(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strip(l)
	}
	return out
}

// wrapHarness is a 60-column console (55 usable columns), update started and
// in the Update group.
func wrapHarness(t *testing.T, color bool) *harness {
	t.Helper()
	h := newHarness(t, 60, color)
	h.startUpdate(true)
	h.r.Stage("pull")
	if got := h.r.width(); got != 55 {
		t.Fatalf("width = %d, want 55", got)
	}
	return h
}

func wantLines(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d lines, want %d:\n%s", name, len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s line %d:\n got %q\nwant %q", name, i, got[i], want[i])
		}
	}
}

const words = "aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj kkkk llll mmmm nnnn oooo"

func TestWrappedWarningLineHangsUnderTheText(t *testing.T) {
	h := wrapHarness(t, false)
	got := printed(h, func() {
		h.r.Line(LevelWarn, "WARN hook Skill merge failed (exit 1): AttributeError: module 'tools.skills_sync' has no attribute '_dir_hash'. Did you mean: 'dir_hash'?")
	})
	wantLines(t, "warn", stripped(got),
		"     ! hook Skill merge failed (exit 1): AttributeError:",
		"       module 'tools.skills_sync' has no attribute",
		"       '_dir_hash'. Did you mean: 'dir_hash'?")
}

func TestWrappedLevelsHangUnderTheirGlyph(t *testing.T) {
	h := wrapHarness(t, false)
	for _, c := range []struct {
		name  string
		level Level
		text  string
		want  []string
	}{
		{"note", LevelNote, "note: " + words, []string{
			"     i aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj",
			"       kkkk llll mmmm nnnn oooo"}},
		{"stop", LevelStop, "STOP " + words, []string{
			"     × aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj",
			"       kkkk llll mmmm nnnn oooo"}},
		{"ok", LevelOK, words, []string{
			"     √ aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj",
			"       kkkk llll mmmm nnnn oooo"}},
		{"result ok", LevelResult, "OK: " + words, []string{
			"     √ OK: aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii",
			"       jjjj kkkk llll mmmm nnnn oooo"}},
		{"result failed", LevelResult, "PROBLEM: " + words, []string{
			"     × PROBLEM: aaaa bbbb cccc dddd eeee ffff gggg hhhh",
			"       iiii jjjj kkkk llll mmmm nnnn oooo"}},
		{"free text keeps its indent", LevelInfo, "  " + words, []string{
			"       aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj",
			"       kkkk llll mmmm nnnn oooo"}},
	} {
		wantLines(t, c.name, stripped(printed(h, func() { h.r.Line(c.level, c.text) })), c.want...)
	}
}

func TestWrappedFreeKeyValueLineHangsUnderTheValue(t *testing.T) {
	h := wrapHarness(t, false)
	wantLines(t, "label: value", stripped(printed(h, func() { h.r.Line(LevelInfo, "disk free: "+words) })),
		"     ● disk free      aaaa bbbb cccc dddd eeee ffff gggg",
		"                      hhhh iiii jjjj kkkk llll mmmm nnnn",
		"                      oooo")
	wantLines(t, "nested label: value", stripped(printed(h, func() { h.r.Line(LevelInfo, "  checkout: "+words) })),
		"       checkout       aaaa bbbb cccc dddd eeee ffff gggg",
		"                      hhhh iiii jjjj kkkk llll mmmm nnnn",
		"                      oooo")
	// a label as wide as the label column takes the wide nested layout
	hang := strings.Repeat(" ", 2+5+2+nestedLabelW) // margin, 5, the two blanks of indent, the label column
	wantLines(t, "wide nested label: value", stripped(printed(h, func() { h.r.Line(LevelInfo, "  a-nested-label-name: "+words) })),
		"         a-nested-label-name"+strings.Repeat(" ", nestedLabelW-19)+"aaaa bbbb cccc",
		hang+"dddd eeee ffff",
		hang+"gggg hhhh iiii",
		hang+"jjjj kkkk llll",
		hang+"mmmm nnnn oooo")
}

func TestWrappedOutputRepeatsTheGutterAndHangsUnderTheText(t *testing.T) {
	h := wrapHarness(t, false)
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"warning glyph", "⚠ The previous update (2026-10-01) was interrupted before it finished (process exited during the update (last stage: deps)); this update completes the work it still owed.", []string{
			"     │ ! The previous update (2026-10-01) was interrupted",
			"     │   before it finished (process exited during the",
			"     │   update (last stage: deps)); this update",
			"     │   completes the work it still owed."}},
		{"indent and glyph", "  ✓ " + words, []string{
			"     │   √ aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii",
			"     │     jjjj kkkk llll mmmm nnnn oooo"}},
		{"arrow", "→ " + words, []string{
			"     │ → aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii",
			"     │   jjjj kkkk llll mmmm nnnn oooo"}},
		{"indent only", "    " + words, []string{
			"     │     aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii",
			"     │     jjjj kkkk llll mmmm nnnn oooo"}},
		{"no glyph", "Update complete! " + words, []string{
			"     │ Update complete! aaaa bbbb cccc dddd eeee ffff",
			"     │ gggg hhhh iiii jjjj kkkk llll mmmm nnnn oooo"}},
	} {
		wantLines(t, c.name, stripped(printed(h, func() { h.r.Output(c.in) })), c.want...)
	}
}

func TestWrappedRowsHangUnderTheValueAndTheExtraLineText(t *testing.T) {
	h := wrapHarness(t, false)
	hang := strings.Repeat(" ", 2+5+nestedLabelW) // margin, indent and the wide label column of a long nested label
	got := printed(h, func() {
		h.r.Rows(
			Row{Key: "running", Value: "desktop, gateway and a very long list of other things that keep going past the end of the line\n  · second extra line that is also long enough to need wrapping around the console edge ok"},
			Row{Key: "short", Value: "fits"},
			Row{Key: "a-nested-label-name", Nested: true, Value: words},
		)
	})
	wantLines(t, "rows", stripped(got),
		"     ● running        desktop, gateway and a very long",
		"                      list of other things that keep",
		"                      going past the end of the line",
		"                      · second extra line that is also",
		"                        long enough to need wrapping",
		"                        around the console edge ok",
		"     ● short          fits",
		"       a-nested-label-name"+strings.Repeat(" ", nestedLabelW-19)+"aaaa bbbb cccc",
		hang+"dddd eeee ffff",
		hang+"gggg hhhh iiii",
		hang+"jjjj kkkk llll",
		hang+"mmmm nnnn oooo")
}

func TestWrappedSummaryCardHangsHeadlineAndRows(t *testing.T) {
	h := wrapHarness(t, false)
	ok := true
	got := printed(h, func() {
		h.r.Summary(Card{OK: &ok, Headline: "Hermes is updated and verified and every check we ran along the way came back fine.",
			Rows: []Row{{Key: "Version", Value: "v1 → v2 " + words}}})
	})
	wantLines(t, "card", stripped(got),
		"  ",
		"     √ Hermes is updated and verified and every check we",
		"       ran along the way came back fine.",
		"     ● Version        v1 → v2 aaaa bbbb cccc dddd eeee",
		"                      ffff gggg hhhh iiii jjjj kkkk llll",
		"                      mmmm nnnn oooo")
}

func TestWrappedCommandHangsUnderTheArguments(t *testing.T) {
	h := wrapHarness(t, false)
	got := printed(h, func() {
		h.r.Command("hermes update --yes --keep-stash --branch main --some-long-flag=value --another-flag=another-value --x")
	})
	wantLines(t, "command", stripped(got),
		"     » command  hermes update --yes --keep-stash --branch",
		"                main --some-long-flag=value",
		"                --another-flag=another-value --x")
}

func TestWrappedOverLongWordsBreakHard(t *testing.T) {
	h := wrapHarness(t, false)
	path := "/very/long/path/segment/that/never/ends/and/has/no/spaces/at/all/ever/so/it/breaks/hard"
	// right behind the gutter: the first line must not be cut inside the gutter
	wantLines(t, "output", stripped(printed(h, func() { h.r.Output(path) })),
		"     │ /very/long/path/segment/that/never/ends/and/has/no",
		"     │ /spaces/at/all/ever/so/it/breaks/hard")
	wantLines(t, "warn", stripped(printed(h, func() { h.r.Line(LevelWarn, "WARN see "+path) })),
		"     ! see",
		"       /very/long/path/segment/that/never/ends/and/has/no",
		"       /spaces/at/all/ever/so/it/breaks/hard")
	wantLines(t, "row value", stripped(printed(h, func() { h.r.Rows(Row{Key: "path", Value: path}) })),
		"     ● path           /very/long/path/segment/that/never/",
		"                      ends/and/has/no/spaces/at/all/ever/",
		"                      so/it/breaks/hard")
}

func TestLineExactlyTheWidthIsNotWrappedOneMoreIs(t *testing.T) {
	h := wrapHarness(t, false)
	// 5 columns of margin and glyph + 50 = the 55 usable columns
	wantLines(t, "fits", stripped(printed(h, func() { h.r.Line(LevelWarn, strings.Repeat("a", 50)) })),
		"     ! "+strings.Repeat("a", 50))
	wantLines(t, "one over", stripped(printed(h, func() { h.r.Line(LevelWarn, strings.Repeat("a", 51)) })),
		"     ! "+strings.Repeat("a", 50),
		"       a")
}

func TestWrappingKeepsEscapeCodesWholeAndReopensTheColour(t *testing.T) {
	h := wrapHarness(t, true)
	got := printed(h, func() {
		h.r.Line(LevelWarn, "WARN hook Skill merge failed (exit 1): AttributeError: module 'tools.skills_sync' has no attribute '_dir_hash'.")
	})
	wantLines(t, "warn colour", got,
		"     \x1b[38;5;221;1m! \x1b[0m\x1b[38;5;221mhook Skill merge failed (exit 1): AttributeError:\x1b[0m",
		"       \x1b[38;5;221mmodule 'tools.skills_sync' has no attribute\x1b[0m",
		"       \x1b[38;5;221m'_dir_hash'.\x1b[0m")
	got = printed(h, func() { h.r.Output("→ Fetching updates " + words) })
	if len(got) < 2 {
		t.Fatalf("expected a wrapped line, got %q", got)
	}
	for i, l := range got {
		if opened, closed := strings.Count(l, "\x1b[")-strings.Count(l, "\x1b[0m"), strings.Count(l, "\x1b[0m"); opened != closed {
			t.Errorf("line %d opens %d and closes %d colours: %q", i, opened, closed, l)
		}
		if !strings.HasPrefix(l, "  \x1b[38;5;25m   │ \x1b[0m") {
			t.Errorf("line %d has no coloured gutter: %q", i, l)
		}
	}
}

func TestWrapFollowsTheConsoleWidthAtPrintTime(t *testing.T) {
	h := wrapHarness(t, false)
	text := "WARN " + words
	if got := printed(h, func() { h.r.Line(LevelWarn, text) }); len(got) != 2 {
		t.Fatalf("at 60 columns: %q, want 2 lines", got)
	}
	h.con.Cols = 100 // the window was widened
	if got := stripped(printed(h, func() { h.r.Line(LevelWarn, text) })); len(got) != 1 {
		t.Errorf("at 100 columns: %q, want one line", got)
	}
	h.con.Cols = 45 // and narrowed (usable width is clamped to 40)
	if got := stripped(printed(h, func() { h.r.Line(LevelWarn, text) })); len(got) != 3 {
		t.Errorf("at 45 columns: %q, want three lines", got)
	}
}

func TestNoWrapOptionLeavesLongLinesToTheConsole(t *testing.T) {
	h := wrapHarness(t, false)
	h.r.opts.NoWrap = true
	wantLines(t, "nowrap", stripped(printed(h, func() { h.r.Line(LevelWarn, "WARN "+words) })),
		"     ! "+words)
}

func TestPlainRendererNeverWrapsEvenOnAKnownConsole(t *testing.T) {
	for _, con := range []*platform.FakeConsole{
		{Cols: 40, Rows: 30},                 // redirected: stdout is not a terminal
		{Cols: 40, Rows: 30, Terminal: true}, // --plain or TERM=dumb on a real console
	} {
		var buf bytes.Buffer
		r := NewPlain(&buf, con)
		r.Line(LevelWarn, "WARN "+words)
		r.Output(words)
		r.Rows(Row{Key: "running", Value: words})
		r.Command(words)
		r.Summary(Card{Headline: words, Rows: []Row{{Key: "k", Value: words}}})
		for _, want := range []string{"WARN " + words + "\n", "  | " + words + "\n", "running: " + words + "\n", "command: " + words + "\n", "\n" + words + "\n", "  k            " + words + "\n"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("terminal=%v: plain output wrapped, missing %q in %q", con.Terminal, want, buf.String())
			}
		}
	}
}

func TestWrapStyledKeepsFittingLinesAndWidthZeroNeverWraps(t *testing.T) {
	fits := "\x1b[1mshort\x1b[0m line"
	if got := wrapStyled(fits, 20, "  ", 2); got != fits {
		t.Errorf("a line that fits changed: %q", got)
	}
	long := "\x1b[1mone two three\x1b[0m four five six"
	if got := wrapStyled(long, 0, "  ", 2); got != long {
		t.Errorf("width 0 must not wrap: %q", got)
	}
	want := "\x1b[1mone two three\x1b[0m four five\n> six"
	if got := wrapStyled(long, 24, "> ", 2); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestWrapStyledShortensAHangWiderThanTheLine(t *testing.T) {
	got := strings.Split(wrapStyled(strings.Repeat("ab ", 20), 30, strings.Repeat(" ", 28), 28), "\n")
	if len(got) < 2 {
		t.Fatalf("no wrap: %q", got)
	}
	for i, l := range got {
		if runeLen(l) > 30 {
			t.Errorf("line %d is %d wide: %q", i, runeLen(l), l)
		}
		if i > 0 && !strings.HasPrefix(l, strings.Repeat(" ", 30-minWrapText)+"a") {
			t.Errorf("line %d: hang not shortened to width-%d: %q", i, minWrapText, l)
		}
	}
}
