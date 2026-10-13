package update

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// On macOS/Linux there is no pause-gate probe. "unknown" / "unavailable (see
// log)" read like a failure; say it does not apply.
func TestGatewayPauseCheckNotApplicableIsNotAFailureLine(t *testing.T) {
	h := newHarness(t)
	h.mode = ModeCheck
	h.gw.gate = hermes.GateNotApplicable
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("gateway pause check: not applicable on this OS", "pause check not applicable on this OS")
	for _, bad := range []string{"pause check: unknown", "pause check unavailable"} {
		if strings.Contains(h.output(), bad) {
			t.Errorf("%q reads like a failure:\n%s", bad, h.output())
		}
	}
}

// A probe that really failed keeps the old wording.
func TestGatewayPauseCheckUnknownKeepsItsWording(t *testing.T) {
	h := newHarness(t)
	h.mode = ModeCheck
	h.gw.gate = hermes.GateUnknown
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("gateway pause check: unknown (probe)", "pause check unavailable (see log)")
}
