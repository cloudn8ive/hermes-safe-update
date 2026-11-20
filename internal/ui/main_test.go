package ui

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

// TestMain blocks every non-loopback network attempt (see testutil.Main).
func TestMain(m *testing.M) {
	// the glyph tests describe the classic console; the gate has its own tests
	allowlistFallback = func() bool { return true }
	testutil.Main(m)
}
