//go:build windows

package platform

import (
	"os"
	"testing"
	"time"
)

// processStarted is taken while the test binary initialises: a moment after
// the kernel created the process, measured with Go's own clock.
var processStarted = time.Now()

// ProcessCreated feeds the "ct:" line of the update marker, which Hermes
// compares with its own reading within 2 s. For the test process the creation
// time must be at or just before the moment its Go runtime initialised: never
// after it (beyond clock rounding), and not more than a minute before (a slow
// launch, antivirus). That catches a wrong epoch or unit (FILETIME counts
// 100 ns ticks since 1601) without depending on how long the tests take.
func TestProcessCreatedOwnPidIsStartedJustBeforeInit(t *testing.T) {
	got, ok := ProcessCreated(os.Getpid())
	if !ok || got.IsZero() {
		t.Fatalf("ProcessCreated(own pid) = %v, %v", got, ok)
	}
	if got.After(processStarted.Add(time.Second)) {
		t.Errorf("creation time %v is after the runtime started (%v)", got, processStarted)
	}
	if lag := processStarted.Sub(got); lag > time.Minute {
		t.Errorf("creation time %v is %v before the runtime started: wrong epoch or unit", got, lag)
	}
	if time.Since(got) < 0 {
		t.Errorf("creation time %v is in the future", got)
	}
}

// A second reading of the same live process gives the same instant.
func TestProcessCreatedIsStable(t *testing.T) {
	a, ok1 := ProcessCreated(os.Getpid())
	b, ok2 := ProcessCreated(os.Getpid())
	if !ok1 || !ok2 || !a.Equal(b) {
		t.Errorf("readings differ: %v/%v vs %v/%v", a, ok1, b, ok2)
	}
}

func TestProcessCreatedRefusesWhatItCannotRead(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if got, ok := ProcessCreated(pid); ok || !got.IsZero() {
			t.Errorf("pid %d: %v, %v", pid, got, ok)
		}
	}
}
