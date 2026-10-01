package apperr

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

func TestExitCodeTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, OK},
		{"generic", errors.New("x"), Failure},
		{"update problem", fmt.Errorf("verify: %w", ErrUpdateProblem), Failure},
		{"usage", fmt.Errorf("flag: %w", ErrUsage), Usage},
		{"bad config", fmt.Errorf("load: %w", config.ErrInvalid), Usage},
		{"cancelled", fmt.Errorf("wait: %w", ErrCancelled), Cancelled},
		{"ctrl-c", context.Canceled, Cancelled},
		{"preflight stop", fmt.Errorf("disk: %w", ErrPreflightStop), PreflightStop},
		{"not a checkout", fmt.Errorf("x: %w", ErrNotCheckout), NotCheckout},
		{"untested declined", ErrUntestedDeclined, Cancelled},
		{"launcher missing", fmt.Errorf("x: %w", ErrLauncherMissing), LauncherMissing},
		{"hermes running", fmt.Errorf("x: %w", ErrHermesRunning), HermesRunning},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCodesAreDistinct(t *testing.T) {
	seen := map[int]string{}
	for name, c := range map[string]int{
		"OK": OK, "Failure": Failure, "LauncherMissing": LauncherMissing, "Cancelled": Cancelled,
		"PreflightStop": PreflightStop, "NotCheckout": NotCheckout, "HermesRunning": HermesRunning, "Usage": Usage,
	} {
		if prev, dup := seen[c]; dup {
			t.Errorf("%s and %s share code %d", name, prev, c)
		}
		seen[c] = name
	}
}
