//go:build windows

package platform

import (
	"time"

	"golang.org/x/sys/windows"
)

// ProcessCreated returns the creation time of pid from GetProcessTimes: the
// same kernel value (and unix-seconds clock) Hermes' psutil-based
// process_create_time reads, so a marker "ct:" line written from it matches
// Hermes' own probe. ok=false when the process cannot be opened or queried.
func ProcessCreated(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)
	var c, x, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &x, &k, &u) != nil {
		return time.Time{}, false
	}
	return time.Unix(0, c.Nanoseconds()), true
}
