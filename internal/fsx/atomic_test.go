package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWriteFileAtomicCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := WriteFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "two" {
		t.Fatalf("got %q, %v", b, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

func TestWriteFileAtomicMissingDirIsError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "no", "such", "x.json")
	if err := WriteFileAtomic(p, []byte("x"), 0o600); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestRenameRetriesOnlySharingErrors(t *testing.T) {
	calls := 0
	var slept []time.Duration
	r := renamer{
		rename: func(_, _ string) error {
			calls++
			if calls < 3 {
				return &os.LinkError{Op: "rename", Err: syscall.Errno(32)}
			}
			return nil
		},
		sleep:     func(d time.Duration) { slept = append(slept, d) },
		retryable: func(err error) bool { var e syscall.Errno; return errors.As(err, &e) && (e == 5 || e == 32) },
	}
	if err := r.do("a", "b"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(slept) != 2 || slept[1] != 2*slept[0] {
		t.Errorf("calls=%d slept=%v", calls, slept)
	}

	calls = 0
	r.rename = func(_, _ string) error { calls++; return errors.New("boom") }
	if err := r.do("a", "b"); err == nil || calls != 1 {
		t.Errorf("non-retryable error must not retry: calls=%d err=%v", calls, err)
	}

	calls = 0
	r.rename = func(_, _ string) error { calls++; return &os.LinkError{Err: syscall.Errno(5)} }
	if err := r.do("a", "b"); err == nil || calls != renameAttempts {
		t.Errorf("want %d attempts then error, got calls=%d err=%v", renameAttempts, calls, err)
	}
}
