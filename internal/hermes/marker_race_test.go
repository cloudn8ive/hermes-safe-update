package hermes

import (
	"os"
	"testing"
	"time"
)

// Hermes appends its "delegate:" line under its own mutex, which Safe Update
// does not hold. If that happens between the refresh's read and its write, the
// refresh must start over from what is there now, never replace the file with
// a copy that lacks the new line.
func TestMarkerRefreshDoesNotDropALineAppendedMeanwhile(t *testing.T) {
	m, path, _ := marker(t)
	const before = "4242\n1790000000\nct:1789999000.123\n"
	const delegate = "delegate:5151 ct:1790000001.500\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Unix(1_790_000_120, 0) }
	reads := 0
	m.afterRead = func() {
		reads++
		if reads == 1 { // Hermes' child appends its line after our first read
			if err := os.WriteFile(path, []byte(before+delegate), 0o644); err != nil {
				t.Error(err)
			}
		}
	}
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if want := "4242\n1790000120\nct:1789999000.123\n" + delegate; readMarker(t, path) != want {
		t.Errorf("marker = %q, want %q (the refreshed time and the delegate line)", readMarker(t, path), want)
	}
	if reads != 2 {
		t.Errorf("the refresh read the file %d times, want 2 (start over once)", reads)
	}
}

// The marker was replaced by another updater while we refreshed: it is left
// exactly as that updater wrote it.
func TestMarkerRefreshLeavesAMarkerTakenOverMeanwhile(t *testing.T) {
	m, path, _ := marker(t)
	if err := os.WriteFile(path, []byte("4242\n1790000000\nct:1789999000.123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const theirs = "777\n1790000100\nct:1789999900.000\n"
	m.afterRead = func() {
		if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
			t.Error(err)
		}
	}
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(t, path); got != theirs {
		t.Errorf("marker = %q, want the other updater's %q", got, theirs)
	}
}

// A file that keeps changing is not written at all (the next tick retries):
// it is better to skip one refresh than to drop a line.
func TestMarkerRefreshSkipsWhenTheFileKeepsChanging(t *testing.T) {
	m, path, _ := marker(t)
	content := "4242\n1790000000\nct:1789999000.123\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Unix(1_790_000_120, 0) }
	n := 0
	m.afterRead = func() {
		n++
		content += "run:abc-" + string(rune('0'+n)) + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Error(err)
		}
	}
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(t, path); got != content {
		t.Errorf("marker = %q, want it untouched: %q", got, content)
	}
}
