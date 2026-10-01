package cua

import (
	"runtime"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

func TestParitySamePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		// samePath cleans with the host's filepath rules, and the cua task
		// path it compares is only ever a Windows path read on Windows.
		t.Skip("samePath compares Windows paths with filepath.Clean: Windows only")
	}
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
