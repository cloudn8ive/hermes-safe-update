package update

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// F1 N9: Hermes' own "Holding at this build ... predates the gateway runtime"
// is the hold (the run waits, `hermes update` never runs).
func TestStableHoldHermesOwnHoldWording(t *testing.T) {
	h := holdHarnessCheck(t, "update-check-stable-holding.txt")
	h.holdReleaseDiverged()
	if err := h.run("unattended"); err != nil {
		t.Fatalf("a wait is exit 0, got %v\n%s", err, h.output())
	}
	h.mustContain("Waiting for stable:", "stable v0.22.0")
	if got := h.updateArgv(); got != "" {
		t.Errorf("hermes update ran: %q", got)
	}
	h.nothingClosed(t)
}

// F1 N1: the hold texts name the channel the run is on, not always "stable".
func TestHoldTextsNameTheEffectiveChannel(t *testing.T) {
	r := &Run{}
	r.Facts.VersionBefore = "v1.0.0+3.gabc"
	r.Facts.Channel.Plan.Effective = "beta"
	ch := hermes.ChannelCheck{Release: "beta", Version: "1.0.0"}
	for name, got := range map[string]string{
		"text": r.holdText(ch, holdRelation{state: holdNewer, commits: 3}),
		"row":  r.holdRowText(ch, holdRelation{state: holdDiverged}),
		"move": r.holdWords(holdChange),
		"unp":  r.holdText(hermes.ChannelCheck{}, holdRelation{state: holdUnproven, why: "no git"}),
	} {
		if strings.Contains(got, "stable") {
			t.Errorf("%s still says stable: %s", name, got)
		}
		if !strings.Contains(got, "beta") {
			t.Errorf("%s does not name beta: %s", name, got)
		}
	}
	// the name comes from the check when the plan has none, and stable stays stable
	r.Facts.Channel.Plan.Effective = ""
	r.Facts.Channel.Check.Release = "beta"
	if got := r.holdName(); got != "beta" {
		t.Errorf("from the check: %q", got)
	}
	r.Facts.Channel.Check.Release = ""
	r.Facts.Channel.Plan.Effective = "stable"
	if got := r.holdWords(holdChange); got != holdChange {
		t.Errorf("stable text changed: %s", got)
	}
}
