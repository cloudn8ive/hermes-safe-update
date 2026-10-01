package cua

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

func TestParitySamePath(t *testing.T) {
	var cases []struct {
		A, B string
		Want bool
	}
	testutil.ParityFixture(t, "same_path", &cases)
	for _, c := range cases {
		if got := samePath(c.A, c.B); got != c.Want {
			t.Errorf("samePath(%q, %q) = %v, Python %v", c.A, c.B, got, c.Want)
		}
	}
}
