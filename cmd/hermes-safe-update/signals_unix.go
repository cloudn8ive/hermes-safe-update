//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals end the run like Ctrl-C does: the context is cancelled and
// the finally-steps (marker release, relaunch, gateway restart, timings) run.
// SIGTERM is what launchd, systemd and `kill` send; SIGHUP is a closed
// terminal; cron sends SIGTERM when a job times out. Without them the process
// died on the spot and left the update marker behind. Windows has neither
// signal (see signals_windows.go). Fake-tested; not run under a real launchd
// or systemd.
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
