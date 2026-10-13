package update

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

const cuaUpdateOut = "→ Fetching updates\n" +
	"→ cua-driver refresh deferred: run hermes computer-use install --upgrade\n" +
	"  Run: hermes computer-use install --upgrade\n" +
	"Update complete! (v1.0 → v1.1)"

// Where the safe updater does the deferred cua-driver refresh itself
// (logon-task support: Windows), Hermes' hint is replaced by one line.
func TestCUADeferralIsAnnouncedWhereTheUpdaterHandlesIt(t *testing.T) {
	h := newHarness(t)
	h.plat.Autostart = &platform.FakeAutostart{Tasks: map[string]string{}}
	h.con.Keys = []platform.Key{{Rune: 'y'}}
	h.onUpdate(0, cuaUpdateOut, true)
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("handled by the safe updater after verification")
	if strings.Contains(h.output(), "Run: hermes computer-use install --upgrade") {
		t.Errorf("Hermes' manual hint should be hidden where the updater does it:\n%s", h.output())
	}
}

// Off Windows the cua service does nothing, so the screen must not claim it
// was handled, and Hermes' own hint has to reach the user.
func TestCUADeferralIsNotClaimedWhereNothingHandlesIt(t *testing.T) {
	h := newHarness(t)
	h.plat.Autostart = &platform.FakeAutostart{} // Supported() == false, like macOS/Linux
	h.con.Keys = []platform.Key{{Rune: 'y'}}
	h.onUpdate(0, cuaUpdateOut, true)
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if strings.Contains(h.output(), "handled by the safe updater") {
		t.Errorf("claimed a refresh that never ran:\n%s", h.output())
	}
	h.mustContain("cua-driver refresh deferred: run hermes computer-use install --upgrade", "Run: hermes computer-use install --upgrade")
}
