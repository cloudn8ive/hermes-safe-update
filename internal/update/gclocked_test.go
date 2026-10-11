package update

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/gc"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// synthetic update output: Python dependencies after the builds, and
// Hermes' own cleanup of an old dependency set skipped (file in use)
func gcLockedOut(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "hermes", "testdata", "contracts", "update-pydeps-after-build.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

const gcLockedRaw = "dependency generation cleanup skipped: [WinError 5] Access is denied"

func TestGCLockedLineIsCalmOnScreenRawInLog(t *testing.T) {
	h := newHarness(t)
	var log bytes.Buffer
	h.logSink = &log
	h.onUpdate(0, gcLockedOut(t), true)
	// our own cleanup removed the set: its folder is gone afterwards
	h.exists = func(p string) bool { return !strings.Contains(p, "environments") }
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	out := h.output()
	h.mustContain(gcLockedNote, "RESULT: OK")
	for _, bad := range []string{"WinError", ".pyd", "Access is denied", "still in place"} {
		if strings.Contains(out, bad) {
			t.Errorf("screen shows %q\n%s", bad, out)
		}
	}
	if !strings.Contains(log.String(), gcLockedRaw) || !strings.Contains(log.String(), "mod.cp314-win_amd64.pyd") {
		t.Errorf("the log lost the raw line\n%s", log.String())
	}
	if strings.Contains(log.String(), "WARN") && strings.Contains(log.String(), "still in place") {
		t.Errorf("warned although the set is gone\n%s", log.String())
	}
}

func TestGCLockedSetStillThereIsReported(t *testing.T) {
	cases := map[string]func(h *harness){
		"our cleanup found nothing": func(h *harness) {
			h.gc.res = &gc.Result{Line: "nothing to remove"}
		},
		"our cleanup failed": func(h *harness) {
			h.gc.err = errors.New("generation cleanup rc=1")
		},
		"the update was not verified, so no cleanup ran": func(h *harness) {
			h.verifier = fakeVerifier{ok: false, out: "desktop build missing"}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			var log bytes.Buffer
			h.logSink = &log
			h.onUpdate(0, gcLockedOut(t), true)
			h.exists = func(string) bool { return true } // the set is still on disk
			setup(h)
			h.con.Keys = []platform.Key{key('y')}
			_ = h.run()
			h.mustContain(gcLockedNote, "Cleanup", "1 old dependency set(s) still in place (a file was in use); the next update retries")
			if !strings.Contains(log.String(), "SUMMARY   Cleanup: ") {
				t.Errorf("summary row missing from the log\n%s", log.String())
			}
			if h.verifier.ok {
				// the cleanup step ran: it says so with a warning, and the count shows
				h.mustContain("old dependency set 01234567 is still in place after the cleanup", "Warnings")
			}
		})
	}
}

// the cleanup removed a different set than the one Hermes could not remove
// (the folder named in the line is still there): the row must not read clean
func TestGCLockedSummaryRowKeepsOurCleanupText(t *testing.T) {
	h := newHarness(t)
	h.onUpdate(0, gcLockedOut(t), true)
	h.exists = func(string) bool { return true }
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("removed 2 old dependency set(s), freed 300 MB; 1 old dependency set(s) still in place")
}

// reasons other than "a file was in use" are not softened
func TestGCSkippedForOtherReasonsStaysVisible(t *testing.T) {
	line := "dependency generation cleanup skipped: [WinError 112] There is not enough space on the disk"
	if got := displayLine("  " + line); got != "  "+line {
		t.Errorf("displayLine rewrote %q to %q", line, got)
	}
	if got := displayLine("  dependency generation cleanup skipped: [WinError 5] Access is denied: 'C:\\x\\environments\\0123abcd\\a.pyd'"); got != "  "+gcLockedNote {
		t.Errorf("displayLine = %q", got)
	}
}

// a skipped-cleanup line whose path cannot be read proves nothing about the
// set: the summary must not read clean
func TestGCLockedWithoutReadablePathIsNeverReportedClean(t *testing.T) {
	h := newHarness(t)
	h.onUpdate(0, "→ Pulling updates...\n"+gcLockedRaw+": 'x'\nUpdate complete! (v1.0 → v1.1)", true)
	h.exists = func(string) bool { return false }
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("old dependency set (unknown) is still in place", "1 old dependency set(s) still in place")
}
