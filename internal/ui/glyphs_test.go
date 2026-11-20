package ui

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

// hermesPrinted is every non-ASCII symbol the Hermes source prints in update,
// check, plugin, skill and pm output (grep of hermes_cli and pm, 2026-11-02),
// the ones WGL4 fonts have included: the test must see both outcomes.
var hermesPrinted = []rune("—─→⚠✗✓ℹ…•◆↷⏳⇒⚕☤↑–★≈↻←§⟲⚡◇▸▶⏸⊘≥≤−↪↔↓·🔌")

func TestEveryHermesGlyphIsRenderableOnScreen(t *testing.T) {
	h := wrapHarness(t, false)
	for _, g := range hermesPrinted {
		line := "x " + string(g) + " y"
		got := strings.Join(stripped(printed(h, func() { h.r.Output(line) })), "\n")
		for _, r := range got {
			if !renderable(r) && !h.r.keepGlyphs()(r) && r != '│' {
				t.Errorf("Output(%q) still has %q (U+%04X): %q", line, r, r, got)
			}
		}
		free := strings.Join(stripped(printed(h, func() { h.r.Line(LevelInfo, line) })), "\n")
		for _, r := range free {
			if !renderable(r) && !h.r.keepGlyphs()(r) {
				t.Errorf("Line(%q) still has %q (U+%04X): %q", line, r, r, free)
			}
		}
	}
}

func TestDiamondBulletOfTheSnapshotLinesIsMapped(t *testing.T) {
	h := wrapHarness(t, false)
	got := strings.Join(stripped(printed(h, func() {
		h.r.Output("◆ Pre-update snapshot: 20260101-000000-pre-update")
		h.r.Output("◆ Sibling profile snapshot(s): 2")
	})), "\n")
	if strings.Contains(got, "◆") || !strings.Contains(got, "♦ Pre-update snapshot") || !strings.Contains(got, "♦ Sibling profile snapshot(s)") {
		t.Errorf("the U+25C6 diamond must show as the WGL4 diamond:\n%s", got)
	}
}

func TestEveryGlyphTableTargetIsRenderable(t *testing.T) {
	for src, dst := range hermesGlyphs {
		if renderable(src) {
			t.Errorf("%q is in the table but the console font has it", src)
		}
		for _, r := range dst {
			if !renderable(r) {
				t.Errorf("table maps %q to %q, which the console font lacks", src, dst)
			}
		}
	}
}

func TestUnknownGlyphsFallBackToASCIIAndLogIsNotTouched(t *testing.T) {
	if got := screenSafe("a\u2603b\U0001F600c\u26A0\uFE0Fd", nil); got != "a?b?c?d" {
		t.Errorf("fallback: %q", got)
	}
	if got := screenSafe("⠀⣿", nil); got != " *" {
		t.Errorf("braille: %q", got)
	}
	// a theme glyph outside WGL4 that the user chose is kept
	keep := func(r rune) bool { return r == '▸' }
	if got := screenSafe("▸", keep); got != "▸" {
		t.Errorf("kept glyph changed: %q", got)
	}
}

func TestPassthroughBlockGolden(t *testing.T) {
	h := newHarness(t, 100, false)
	h.startUpdate(true)
	h.r.Stage("pull")
	var b strings.Builder
	for _, l := range []string{
		"◆ Pre-update snapshot: 20260101-000000-pre-update",
		"◆ Sibling profile snapshot(s): 2",
		"→ Fetching updates",
		"  ✓ Configuration is up to date",
		"  ✗ failed to do it",
		"  ⚠ careful",
		"ℹ info — see `hermes doctor` … done",
		"  ⏳ waiting ⇒ next ↷ skipped ↻ again ⊘ off ▶ go ⏸ pause",
		"  ★ new ⚡ fast ◇ open ▸ sub 🔌 plugin ✅ ok ❌ no",
		"  ─── box ═ ┌┐ ♦ █ ≥ ≤ ≈ − ↔ ↑ ↓ • ·",
	} {
		b.WriteString(strings.Join(stripped(printed(h, func() { h.r.Output(l) })), "\n"))
		b.WriteString("\n")
	}
	testutil.Golden(t, "passthrough-glyphs.golden", []byte(b.String()))
}

// REVIEW-1 N11 (v0.8.0): tabs count as one rune but several columns, and a
// carriage return inside a line rewinds the console; wide glyphs are mapped
// to one-column stand-ins, so every character of a screen line is one column.
func TestTabsAndCarriageReturnsDoNotBreakTheColumnCount(t *testing.T) {
	if got := screenSafe("a\tb", nil); got != "a       b" {
		t.Errorf("tab: %q", got)
	}
	if got := screenSafe("12345678\tx", nil); got != "12345678        x" {
		t.Errorf("tab at a stop: %q", got)
	}
	if got := screenSafe("progress 10%\rprogress 100%", nil); got != "progress 100%" {
		t.Errorf("carriage return: %q", got)
	}
	if got := screenSafe("a\nb\tc", nil); got != "a\nb       c" {
		t.Errorf("tab after a line break: %q", got)
	}
	if got := screenSafe("\u4e2d\u6587 \U0001F600", nil); got != "?? ?" {
		t.Errorf("wide glyphs: %q", got)
	}
	h := wrapHarness(t, false)
	out := strings.Join(stripped(printed(h, func() { h.r.Output("name\tvalue\tsomething longer than the row can hold at all for sure 1234567890") })), "\n")
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "\t") {
			t.Errorf("tab on screen: %q", l)
		}
	}
}
