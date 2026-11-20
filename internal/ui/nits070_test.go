package ui

import (
	"strings"
	"testing"
)

// v0.7.0 nits seen in the 25 Oct screenshot.

func TestLongSentenceHeadIsNotCutAsALabel(t *testing.T) {
	h := wrapHarness(t, false)
	got := strings.Join(stripped(printed(h, func() {
		h.r.Line(LevelInfo, "Nothing to update: already at the latest release (stable v0.22.0).")
	})), "\n")
	if strings.Contains(got, "Nothing to upd ") || !strings.Contains(got, "Nothing to update: already at the latest") {
		t.Errorf("the sentence head was cut as a label:\n%s", got)
	}
	// a short head is still a label, and an indented wide head keeps its layout
	short := strings.Join(stripped(printed(h, func() { h.r.Line(LevelInfo, "disk free: 100 GB") })), "\n")
	if !strings.Contains(short, "disk free") || strings.Contains(short, "disk free:") {
		t.Errorf("a short head must stay a label:\n%s", short)
	}
}

func TestHermesCheckMarkInAFreeLineUsesTheToolGlyph(t *testing.T) {
	h := wrapHarness(t, false)
	got := strings.Join(stripped(printed(h, func() {
		h.r.Line(LevelInfo, "update --check (rc=0): \u2713 Up to date with the latest release.")
	})), "\n")
	if strings.Contains(got, "\u2713") || !strings.Contains(got, "\u221a Up to date") {
		t.Errorf("the check mark was not mapped to the tool's glyph:\n%s", got)
	}
	cross := strings.Join(stripped(printed(h, func() { h.r.Line(LevelInfo, "x: \u2717 broken") })), "\n")
	if strings.Contains(cross, "\u2717") {
		t.Errorf("the cross was not mapped:\n%s", cross)
	}
}
