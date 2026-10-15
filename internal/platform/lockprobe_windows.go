//go:build windows

package platform

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// winLockProbe is the Windows LockProbe: the same question Hermes' own
// scripts/desktop-update/marker.ps1 asks (Test-CheckoutLockHeld): open the
// lock file for reading with every share mode, try to take the byte range
// exclusively without waiting (LockFileEx), and give it back at once.
// Hermes' msvcrt.locking and this call are the same kernel byte-range lock,
// so a range held by `hermes update` (or a child it started) conflicts.
type winLockProbe struct{}

// lockRecordMax is how much of the file start is read as the holder record.
const lockRecordMax = 512

func (winLockProbe) ProbeLock(path string, offset, length int64) (LockInfo, error) {
	if length <= 0 || offset < 0 {
		return LockInfo{}, fmt.Errorf("lock probe: bad range %d+%d", offset, length)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return LockInfo{}, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
			return LockInfo{State: LockFree}, nil // only a missing file is free
		default:
			// Exists but cannot be opened (denied, opened without sharing):
			// Hermes' probes answer "held" for it.
			return LockInfo{State: LockUnopenable, Detail: err.Error()}, nil
		}
	}
	defer windows.CloseHandle(h)

	info := LockInfo{Record: readHead(h, lockRecordMax)}
	var ov windows.Overlapped
	ov.Offset, ov.OffsetHigh = uint32(uint64(offset)), uint32(uint64(offset)>>32)
	lo, hi := uint32(uint64(length)), uint32(uint64(length)>>32)
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lo, hi, &ov)
	switch {
	case err == nil:
		var ov2 windows.Overlapped
		ov2.Offset, ov2.OffsetHigh = ov.Offset, ov.OffsetHigh
		_ = windows.UnlockFileEx(h, 0, lo, hi, &ov2)
		info.State = LockFree
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		info.State = LockHeld
	default:
		return LockInfo{}, fmt.Errorf("lock probe: %w", err)
	}
	return info, nil
}

// readHead reads up to limit bytes from the start of the open file. It
// returns nil when nothing could be read (the range is far past the record,
// so a lock on it does not get in the way).
func readHead(h windows.Handle, limit int) []byte {
	buf := make([]byte, limit)
	var n uint32
	if err := windows.ReadFile(h, buf, &n, nil); err != nil || n == 0 {
		return nil
	}
	return buf[:n]
}
