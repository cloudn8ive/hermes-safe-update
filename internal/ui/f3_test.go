package ui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func withFallback(t *testing.T, on bool) {
	old := allowlistFallback
	allowlistFallback = func() bool { return on }
	t.Cleanup(func() { allowlistFallback = old })
}

// F3 item 1: every Hermes-derived text on screen goes through screenSafe.
func TestF3EveryScreenPathIsScreenSafe(t *testing.T) {
	h := wrapHarness(t, false)
	check := func(what, got string) {
		t.Helper()
		if strings.ContainsAny(got, "\t\u25c6") {
			t.Errorf("%s still has a tab or U+25C6: %q", what, got)
		}
	}
	text := "plugin \u25c6 name\tx"
	for name, lv := range map[string]Level{"note": LevelNote, "warn": LevelWarn, "stop": LevelStop, "ok": LevelOK, "result": LevelResult} {
		check("Line "+name, strings.Join(stripped(printed(h, func() { h.r.Line(lv, text) })), "\n"))
	}
	check("Rows", strings.Join(stripped(printed(h, func() { h.r.Rows(Row{Key: "k\u25c6", Value: text}) })), "\n"))
	ok := true
	check("Summary", strings.Join(stripped(printed(h, func() {
		h.r.Summary(Card{OK: &ok, Headline: text, Rows: []Row{{Key: "a", Value: text}}})
	})), "\n"))
	got := strings.Join(stripped(printed(h, func() { h.r.Output(text) })), "\n")
	check("Output", got)
	if !strings.Contains(got, "\u2666") {
		t.Errorf("Output should show the mapped diamond: %q", got)
	}
}

func TestF3PromptIsScreenSafe(t *testing.T) {
	h := wrapHarness(t, false)
	text := "Update plugin \u25c6 foo\tbar?"
	got := strings.Join(stripped(printed(h, func() {
		_, _ = h.r.Prompt(context.Background(), PromptSpec{Text: text, Keys: []rune{'y'}, Timeout: 20 * time.Millisecond})
	})), "\n")
	if strings.ContainsAny(got, "\t\u25c6") || !strings.Contains(got, "Update plugin") {
		t.Errorf("prompt text: %q", got)
	}
}

// F3 item 2: the allowlist fallback is for classic conhost only.
func TestF3ClassicConhostGate(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{"conhost", "windows", nil, true},
		{"windows terminal", "windows", map[string]string{"WT_SESSION": "x"}, false},
		{"vscode", "windows", map[string]string{"TERM_PROGRAM": "vscode"}, false},
		{"conemu", "windows", map[string]string{"ConEmuPID": "12"}, false},
		{"linux", "linux", nil, false},
		{"wsl", "linux", map[string]string{"WT_SESSION": "x"}, false},
		{"mac", "darwin", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, false},
	}
	for _, c := range cases {
		if got := classicConhost(env(c.env), c.goos); got != c.want {
			t.Errorf("%s: classicConhost = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestF3ModernTerminalKeepsCJKAndEmoji(t *testing.T) {
	withFallback(t, false)
	in := "\u4e2d\u6587 \U0001F600 \u25c6 ok"
	if got := screenSafe(in, nil); got != in {
		t.Errorf("modern terminal text changed: %q", got)
	}
	h := wrapHarness(t, false)
	got := strings.Join(stripped(printed(h, func() { h.r.Output(in) })), "\n")
	if !strings.Contains(got, "\u4e2d\u6587 \U0001F600") || strings.Contains(got, "?") {
		t.Errorf("Output on a modern terminal: %q", got)
	}
	// tabs are still expanded
	if got := screenSafe("a\tb", nil); got != "a       b" {
		t.Errorf("tab: %q", got)
	}
}

func TestF3ConhostStillFallsBack(t *testing.T) {
	withFallback(t, true)
	if got := screenSafe("\u4e2d\u6587 \U0001F600", nil); got != "?? ?" {
		t.Errorf("conhost: %q", got)
	}
}

// F3 item 3: tab stops start after the gutter and skip CSI sequences.
func TestF3TabStopsFromGutterAndSkipCSI(t *testing.T) {
	if got := screenSafeAt("a\tb", nil, 5); got != "a  b" {
		t.Errorf("gutter 5: %q", got)
	}
	esc := "\x1b[1;31m"
	if got := screenSafe(esc+"ab\tc", nil); got != esc+"ab      c" {
		t.Errorf("CSI counted as columns: %q", got)
	}
}

func TestF3OutputTabStopCountsTheGutter(t *testing.T) {
	h := wrapHarness(t, false)
	got := strings.Join(stripped(printed(h, func() { h.r.Output("a\tb") })), "\n")
	if !strings.Contains(got, "a  b") || strings.Contains(got, "a   b") {
		t.Errorf("tab should end at the stop after the 5-wide gutter (2 blanks): %q", got)
	}
}
