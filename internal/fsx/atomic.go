// Package fsx holds small file-system helpers shared by every package:
// atomic writes with a Windows-aware rename retry (DECISIONS D6).
package fsx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	renameAttempts   = 10
	renameFirstDelay = 20 * time.Millisecond
	renameMaxDelay   = 500 * time.Millisecond
)

// WriteFileAtomic writes data to path via a temp file in the same directory,
// fsyncs it and renames it over path. A crash leaves the old or the new file,
// never half of one. The rename is retried on Windows sharing violations
// (antivirus, indexer, an editor holding the file).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("chmod %q: %w", path, err)
	}
	if err := Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Rename is os.Rename with a bounded retry on errors that mean "someone else
// briefly holds the file" (Windows errno 5 access denied, 32 sharing violation).
func Rename(from, to string) error {
	return defaultRenamer.do(from, to)
}

type renamer struct {
	rename    func(from, to string) error
	sleep     func(time.Duration)
	retryable func(error) bool
}

var defaultRenamer = renamer{rename: os.Rename, sleep: time.Sleep, retryable: isSharingError}

func (r renamer) do(from, to string) error {
	delay := renameFirstDelay
	var err error
	for i := 0; i < renameAttempts; i++ {
		if err = r.rename(from, to); err == nil {
			return nil
		}
		if !r.retryable(err) || i == renameAttempts-1 {
			break
		}
		r.sleep(delay)
		delay *= 2
		if delay > renameMaxDelay {
			delay = renameMaxDelay
		}
	}
	return fmt.Errorf("rename %q -> %q: %w", from, to, err)
}

// isSharingError reports Windows ERROR_ACCESS_DENIED (5) and
// ERROR_SHARING_VIOLATION (32). On POSIX those numbers are EIO/EPIPE, which a
// rename never returns for a held file, so retrying them is harmless there.
func isSharingError(err error) bool {
	var e syscall.Errno
	if !errors.As(err, &e) {
		return false
	}
	return e == 5 || e == 32
}
