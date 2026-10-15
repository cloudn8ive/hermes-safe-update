//go:build !windows

package platform

import "time"

// ProcessCreated is only implemented on Windows, where the clock is the one
// Hermes reads (GetProcessTimes). Elsewhere it reports "unknown", so the
// update marker stays format v1 and no creation-time check is made: a v2
// "ct:" line written from a clock that differs from Hermes' by more than 2 s
// would make Hermes judge our own claim dead.
func ProcessCreated(int) (time.Time, bool) { return time.Time{}, false }
