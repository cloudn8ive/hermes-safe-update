package update

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// F3 item 6: no hold text, row key, refusal or stale row says "stable" on beta.
func TestEveryHoldTextNamesTheChannelNotStable(t *testing.T) {
	ch := hermes.ChannelCheck{Release: "beta", Version: "1.0.0"}
	base := func() *Run {
		r := &Run{}
		r.Mode, r.Config = ModeUpdate, &config.Config{}
		r.Facts.VersionBefore = "v1.0.0+3.gabc"
		r.Facts.Channel.Plan.Effective = "beta"
		return r
	}
	texts := map[string]string{}
	r := base()
	for _, st := range []holdState{holdNewer, holdDiverged, holdUnproven, holdContains} {
		rel := holdRelation{state: st, commits: 3, why: "no git"}
		texts["text "+st.String()] = r.holdText(ch, rel)
		texts["row "+st.String()] = r.holdRowText(ch, rel)
	}
	texts["change"] = r.holdWords(holdChange)
	texts["finish"] = r.holdWords(holdFinish)
	texts["stale"+"-latest"] = r.holdWords("v1.0.0 is the latest stable release")
	key, val := r.staleRowText([]string{"v1.0.0 is the latest stable release", "main has 3 commits since"})
	texts["stale key"], texts["stale row"] = key, val
	rr := base()
	rr.Repair = true
	texts["refusal repair"], _ = rr.holdRefusal()
	rf := base()
	rf.Config.Run.Force = true
	texts["refusal force"], _ = rf.holdRefusal()
	rp := base()
	rp.Facts.Previous = &hermes.PreviousUpdate{}
	texts["refusal previous"], _ = rp.holdRefusal()
	for name, got := range texts {
		if got == "" {
			t.Errorf("%s: empty", name)
			continue
		}
		if strings.Contains(strings.ToLower(got), "stable") {
			t.Errorf("%s still says stable: %s", name, got)
		}
	}
}

// F3 item 8: the plugin snapshot is retaken only when a plugin update ran.
func TestRepairRetakesThePluginSnapshotOnlyAfterAPluginUpdate(t *testing.T) {
	count := func(keys ...rune) int {
		h, _, _ := extrasHarness(t)
		upToDate(h)
		var buf bytes.Buffer
		h.logSink = &buf
		for _, k := range keys {
			h.con.Keys = append(h.con.Keys, key(k))
		}
		if err := h.run(); err != nil {
			t.Fatal(err, h.output())
		}
		if h.updates != 1 {
			t.Fatalf("the repair did not run (keys %q): %s", string(keys), h.output())
		}
		return strings.Count(buf.String(), "plugin guard: before the update")
	}
	if n := count('n', 'n', 'r'); n != 1 {
		t.Errorf("no plugin update ran, the snapshot was taken %d times (want 1)", n)
	}
	if n := count('y', 'y', 'r'); n != 2 {
		t.Errorf("a plugin update ran, the snapshot was taken %d times (want 2)", n)
	}
}

// F3 item 7: nobody at the console: one question times out, the other extras
// questions are not asked, the repair offer is (so two waits, not three).
func TestAbsentUserWaitsOnceForTheExtrasThenTheOffer(t *testing.T) {
	h, _, _ := extrasHarness(t)
	h.cfg.Tunables.ConfirmTimeoutS = 1
	var buf bytes.Buffer
	h.logSink = &buf
	h.con.Keys = nil
	if err := h.run(); err != nil {
		t.Fatal(err, h.output())
	}
	log := buf.String()
	if n := strings.Count(log, "prompt answer"); n != 2 {
		t.Errorf("%d questions were asked, want 2 (one extras question, the offer):\n%s", n, log)
	}
	if n := strings.Count(log, "timeout=true"); n != 2 {
		t.Errorf("%d timeouts, want 2", n)
	}
	if strings.Contains(log, "prompt: ") && strings.Contains(log, "prompt: 1 update is waiting for hub skills") {
		t.Errorf("the skills question was asked:\n%s", log)
	}
	h.mustContain("Press R to repair", "no answer to a question, so the others were not asked")
	if h.xRan("skills", "update") != 0 || h.xRan("plugins", "update", "memory-demo") != 0 || h.updates != 0 {
		t.Errorf("something was applied: %v", h.calls())
	}
}

// a key at the first question is an answer: the next question is asked.
func TestAnsweredExtrasQuestionsAreAllAsked(t *testing.T) {
	h, _, _ := extrasHarness(t)
	var buf bytes.Buffer
	h.logSink = &buf
	h.con.Keys = []platform.Key{key('n'), key('n'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err, h.output())
	}
	if n := strings.Count(buf.String(), "prompt answer"); n != 3 {
		t.Errorf("%d questions asked, want 3", n)
	}
}

func TestHoldCardOnBetaNamesBetaInEveryRow(t *testing.T) {
	r := &Run{}
	r.Mode, r.Config = ModeUpdate, &config.Config{}
	r.Facts.Channel.Plan.Effective = "beta"
	r.Clock = timings.SystemClock{}
	r.Facts.Hold = &Hold{Text: "Waiting for beta: x"}
	r.Facts.Previous = &hermes.PreviousUpdate{}
	c := r.holdCard()
	found := false
	for _, row := range c.Rows {
		if row.Key == "Finish it" {
			found = true
		}
		if strings.Contains(strings.ToLower(row.Value), "stable") {
			t.Errorf("row %q says stable: %s", row.Key, row.Value)
		}
	}
	if !found {
		t.Fatalf("no Finish it row: %+v", c.Rows)
	}
}
