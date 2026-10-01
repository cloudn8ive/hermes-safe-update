package ui

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

func TestParityFmtDur(t *testing.T) {
	var cases []struct {
		S    float64
		Want string
	}
	testutil.ParityFixture(t, "fmt_dur", &cases)
	for _, c := range cases {
		if got := fmtDur(c.S); got != c.Want {
			t.Errorf("fmtDur(%v) = %q, Python %q", c.S, got, c.Want)
		}
	}
}

func TestParityFmtLeft(t *testing.T) {
	var cases []struct {
		S    float64
		Want string
	}
	testutil.ParityFixture(t, "fmt_left", &cases)
	for _, c := range cases {
		if got := fmtLeft(c.S); got != c.Want {
			t.Errorf("fmtLeft(%v) = %q, Python %q", c.S, got, c.Want)
		}
	}
}
