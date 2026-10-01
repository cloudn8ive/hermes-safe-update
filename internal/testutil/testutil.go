// Package testutil has helpers shared by tests of several packages:
// golden-file comparison with -update, CRLF normalisation and a fixed
// clock. It is imported only from _test.go files.
package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// Golden compares got with testdata/<name> (CRLF normalised). With -update
// it rewrites the file instead. Review golden diffs before committing.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", p, err)
	}
	if NormalizeNewlines(string(got)) != NormalizeNewlines(string(want)) {
		t.Errorf("output differs from %s (run with -update to accept)\n--- got ---\n%s\n--- want ---\n%s", p, got, want)
	}
}

// NormalizeNewlines turns CRLF into LF.
func NormalizeNewlines(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// Clock is a manual clock for tests (implements timings.Clock).
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock starts at t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
