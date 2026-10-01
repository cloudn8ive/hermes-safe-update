package config

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

// TestMain blocks every non-loopback network attempt (see testutil.Main).
func TestMain(m *testing.M) { testutil.Main(m) }
