//go:build !windows

package main

import (
	"context"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// SIGTERM (service stop, cron timeout) and SIGHUP (closed terminal) must
// cancel the run's context like Ctrl-C, so the marker is released.
func TestTerminationSignalsCancelTheRun(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
			defer stop()
			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatalf("%v did not cancel the context", sig)
			}
		})
	}
}
