package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

func testStyle(color bool) *style {
	th := config.Defaults().Theme
	return newStyle(&th, color)
}

func TestFmtDur(t *testing.T) {
	for _, c := range []struct {
		s    float64
		want string
	}{{0, "0:00"}, {59.4, "0:59"}, {59.5, "1:00"}, {0.5, "0:00"}, {1.5, "0:02"}, {75, "1:15"}, {3599.6, "1:00:00"}, {3600, "1:00:00"}, {3725, "1:02:05"}, {-4, "0:00"}} {
		if got := fmtDur(c.s); got != c.want {
			t.Errorf("fmtDur(%v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestFmtLeft(t *testing.T) {
	for _, c := range []struct {
		s    float64
		want string
	}{{0, "under a minute left"}, {44.9, "under a minute left"}, {45, "about 1 min left"}, {89, "about 1 min left"}, {90, "about 2 min left"}, {150, "about 2 min left"}, {518, "about 9 min left"}} {
		if got := fmtLeft(c.s); got != c.want {
			t.Errorf("fmtLeft(%v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestClockAndDateFormatsAreEnglishWithoutLeadingZero(t *testing.T) {
	ts := time.Date(2026, 10, 1, 9, 5, 7, 0, time.UTC)
	if got := fmtClock(ts); got != "9:05 AM" {
		t.Errorf("fmtClock = %q", got)
	}
	if got := fmtClockSec(ts); got != "9:05:07 AM" {
		t.Errorf("fmtClockSec = %q", got)
	}
	if got := fmtDate(ts); got != "Thu 1 Oct" {
		t.Errorf("fmtDate = %q", got)
	}
	noon := time.Date(2026, 10, 11, 0, 30, 0, 0, time.UTC)
	if got := fmtClock(noon); got != "12:30 AM" {
		t.Errorf("fmtClock midnight = %q", got)
	}
	if got := fmtDate(noon); got != "Sun 11 Oct" {
		t.Errorf("fmtDate = %q", got)
	}
}

func TestBarColourGradientBanker(t *testing.T) {
	st := testStyle(true)
	// t = 6/49 -> g = 175 + 40*6/49 = 179.898 -> 180 ; b = 255 - 120*6/49 = 240.30 -> 240
	if got := st.barColour(6, 50); got != "38;2;95;180;240" {
		t.Errorf("barColour(6,50) = %q", got)
	}
	if got := st.barColour(0, 50); got != "38;2;95;175;255" {
		t.Errorf("barColour(0,50) = %q", got)
	}
	if got := st.barColour(49, 50); got != "38;2;95;215;135" {
		t.Errorf("barColour(49,50) = %q", got)
	}
	// exact .5 rounds to even: g = 175 + 40*t, t = 1/16 -> 177.5 -> 178 (even); b = 255-120/16 = 247.5 -> 248 (even)
	if got := st.barColour(1, 17); got != "38;2;95;178;248" {
		t.Errorf("banker's rounding: barColour(1,17) = %q", got)
	}
}

func TestSGRAndNoColour(t *testing.T) {
	on, off := testStyle(true), testStyle(false)
	if got := on.sgr("x", "38;5;80", "1"); got != "\x1b[38;5;80;1mx\x1b[0m" {
		t.Errorf("colour on: %q", got)
	}
	if got := on.sgr("x"); got != "x" {
		t.Errorf("no codes: %q", got)
	}
	if got := off.sgr("x", "38;5;80"); got != "x" {
		t.Errorf("colour off drops colour: %q", got)
	}
	if got := off.sgr("x", "38;5;80", "1"); got != "\x1b[1mx\x1b[0m" {
		t.Errorf("colour off keeps bold: %q", got)
	}
	if got := off.sgr("x", "38;5;221;1"); got != "\x1b[1mx\x1b[0m" {
		t.Errorf("combined colour+bold code reduces to bold: %q", got)
	}
	if got := off.sgr("x", "38;2;1;2;3"); strings.Contains(got, "38;") {
		t.Errorf("rgb leaked: %q", got)
	}
}

func TestRowRightAlignsAndTruncates(t *testing.T) {
	st := testStyle(false)
	got := st.row([]seg{{t: "ab"}}, []seg{{t: "cd"}}, 10, " ", nil)
	if got != "ab      cd" {
		t.Errorf("row = %q", got)
	}
	// too narrow: filler is at least one column, result cut to width
	got = st.row([]seg{{t: "abcdef"}}, []seg{{t: "ghij"}}, 8, "-", nil)
	if visibleLen(got) != 8 || !strings.HasPrefix(got, "abcdef-g") {
		t.Errorf("narrow row = %q", got)
	}
	// wide runes count as one column each (Consolas-safe glyph set is single-width)
	got = st.row([]seg{{t: "♦ x"}}, []seg{{t: "r"}}, 8, " ", nil)
	if got != "♦ x    r" {
		t.Errorf("glyph row = %q", got)
	}
}

func TestVisibleLenIgnoresSGR(t *testing.T) {
	st := testStyle(true)
	s := st.sgr("hello", "38;5;80", "1") + " " + st.sgr("wörld", "1")
	if n := visibleLen(s); n != 11 {
		t.Errorf("visibleLen = %d", n)
	}
	if n := visibleLen("\x1b[?25l\x1b[2Aabc\x1b[K"); n != 3 {
		t.Errorf("visibleLen cursor codes = %d", n)
	}
}

func TestWrapWords(t *testing.T) {
	got := wrapWords("alpha beta gamma delta", 11)
	want := []string{"alpha beta", "gamma delta"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("wrap = %q", got)
	}
	got = wrapWords("supercalifragilistic x", 8)
	if len(got) != 3 || got[0] != "supercal" || got[1] != "ifragili" || got[2] != "stic x" {
		t.Errorf("long word split: %q", got)
	}
	if got := wrapWords("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("empty = %q", got)
	}
}
