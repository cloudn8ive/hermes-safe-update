package testutil

import (
	"testing"
	"time"
)

func TestClockAdvance(t *testing.T) {
	start := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	c := NewClock(start)
	c.Advance(90 * time.Second)
	if got := c.Now().Sub(start); got != 90*time.Second {
		t.Errorf("advanced %v", got)
	}
}

func TestNormalizeNewlines(t *testing.T) {
	if NormalizeNewlines("a\r\nb\n") != "a\nb\n" {
		t.Error("CRLF not normalised")
	}
}
