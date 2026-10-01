// Package apperr defines the sentinel errors that decide the process exit
// code, and the one function that maps them (cmd/ calls it; nothing else
// calls os.Exit). The table is documented in docs/design.md; scripts and the
// Start-menu shims depend on it.
package apperr

import (
	"context"
	"errors"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

// Exit codes. 0-3 keep the Python updater's meaning.
const (
	OK              = 0  // done: update verified, check finished, already up to date
	Failure         = 1  // the update ran but needs attention (RESULT PROBLEM), or an unexpected error
	LauncherMissing = 2  // the hermes launcher was not found (Python: "STOP launcher not found")
	Cancelled       = 3  // aborted by the user or a timeout before anything changed
	PreflightStop   = 4  // pre-flight STOP: marker held, low disk, update check failed (D1)
	NotCheckout     = 5  // not a managed source checkout (packaged app, MSIX, Docker, Nix): nothing changed
	HermesRunning   = 6  // a settings command refused because the desktop app is running
	Usage           = 64 // bad flags or invalid configuration
)

var (
	ErrUpdateProblem    = errors.New("update needs attention")
	ErrLauncherMissing  = errors.New("hermes launcher not found")
	ErrCancelled        = errors.New("cancelled; nothing was changed")
	ErrPreflightStop    = errors.New("pre-flight stopped the update; nothing was changed")
	ErrNotCheckout      = errors.New("not a managed source-checkout install")
	ErrHermesRunning    = errors.New("the Hermes desktop app is running")
	ErrUsage            = errors.New("usage error")
	ErrUntestedDeclined = errors.New("untested platform not accepted")
	// ErrNotImplemented marks skeleton stubs still to be filled in by the
	// owning implementer task. None may remain at release.
	ErrNotImplemented = errors.New("not implemented yet")
)

// ExitCode maps an error chain to the documented exit code.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return OK
	case errors.Is(err, ErrUsage), errors.Is(err, config.ErrInvalid):
		return Usage
	case errors.Is(err, ErrLauncherMissing):
		return LauncherMissing
	case errors.Is(err, ErrCancelled), errors.Is(err, ErrUntestedDeclined), errors.Is(err, context.Canceled):
		return Cancelled
	case errors.Is(err, ErrPreflightStop):
		return PreflightStop
	case errors.Is(err, ErrNotCheckout):
		return NotCheckout
	case errors.Is(err, ErrHermesRunning):
		return HermesRunning
	default:
		return Failure
	}
}
