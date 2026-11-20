package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// v0.8.0: on an attended up-to-date run the plugin, skill and tool questions
// come BEFORE the repair offer; the offer is the last thing asked.

func idxAll(t *testing.T, out string, subs ...string) []int {
	t.Helper()
	at := make([]int, len(subs))
	for i, s := range subs {
		at[i] = strings.Index(out, s)
		if at[i] < 0 {
			t.Fatalf("%q is not in the transcript:\n%s", s, out)
		}
	}
	return at
}

func TestQuestionsComeBeforeTheRepairOfferInTheTranscript(t *testing.T) {
	h, _, _ := extrasHarness(t)
	h.con.Keys = []platform.Key{key('y'), key('y'), key('x')} // plugin, skills, then the offer
	if err := h.run(); err != nil {
		t.Fatal(err, h.output())
	}
	at := idxAll(t, h.output(),
		"Nothing to update",
		"Plugins, skills and tools",
		"Update memory-demo (your memory provider)",
		"update is waiting for hub skills",
		"Press R to repair this install")
	for i := 1; i < len(at); i++ {
		if at[i] <= at[i-1] {
			t.Fatalf("prompts out of order (offsets %v):\n%s", at, h.output())
		}
	}
	if h.updates != 0 {
		t.Errorf("hermes update ran without R: %d", h.updates)
	}
	if strings.Contains(h.output(), "exits without changes") {
		t.Errorf("the offer says it exits without changes after updates were applied:\n%s", h.output())
	}
	h.mustContain("Any other key ends the run without repairing")
}

func TestRepairAfterPluginUpdatesRetakesTheGuardSnapshotAndRunsExtrasOnce(t *testing.T) {
	h, s, _ := extrasHarness(t)
	upToDate(h)
	// the plugin update (the user's y) leaves the plugin disabled and the provider cleared:
	// that is the baseline of the repair, not an eviction by the repair
	s.updateRes = func(*harness) execx.Result {
		h.setCfg(xEvictedConfig)
		_ = os.WriteFile(filepath.Join(h.extrasHome, "plugins", "memory-demo", "plugin.yaml"), []byte("name: memory-demo\nversion: 2\n"), 0o644)
		return execx.Result{Output: "✓ Plugin memory-demo updated.\n"}
	}
	h.con.Keys = []platform.Key{key('y'), key('y'), key('r')}
	if err := h.run(); err != nil {
		t.Fatal(err, h.output())
	}
	if h.updates != 1 {
		t.Fatalf("the repair did not run: updates=%d\n%s", h.updates, h.output())
	}
	if strings.Contains(h.output(), "Hermes switched off") || h.xRanAny("plugins", "enable") {
		t.Errorf("the guard judged the plugin update as an eviction (stale snapshot):\n%s", h.output())
	}
	if h.xRan("pm", "doctor") != 1 || h.xRan("skills", "check") != 1 || h.xRan("plugins", "check-updates", "--json") != 1 {
		t.Errorf("extras probes ran more than once: %v", h.calls())
	}
	if n := strings.Count(h.output(), "== Plugins, skills and tools"); n != 1 {
		t.Errorf("the extras section was printed %d times", n)
	}
}

func TestRepairStillCatchesAnEvictionDoneByTheRepairAfterTheQuestions(t *testing.T) {
	h, _, _ := extrasHarness(t)
	upToDate(h)
	h.enableScript(0)
	h.update = func(int) execx.Result {
		h.setCfg(xEvictedConfig)
		return execx.Result{Output: "→ Update channel: main\n" + xEvictNotice + "\n✓ Already up to date!"}
	}
	h.con.Keys = []platform.Key{key('n'), key('n'), key('r'), key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err, h.output())
	}
	if !strings.Contains(h.output(), "Hermes switched off") {
		t.Errorf("an eviction by the repair itself was missed:\n%s", h.output())
	}
}

func TestUnansweredPluginQuestionStillEndsTheRun(t *testing.T) {
	h, _, _ := extrasHarness(t)
	h.con.Keys = nil // nobody is there: the plugin question, the skills question and the offer all time out
	if err := h.run(); err != nil {
		t.Fatal(err, h.output())
	}
	if h.xRan("plugins", "update", "memory-demo") != 0 || h.updates != 0 {
		t.Errorf("something ran without an answer: %v", h.calls())
	}
	at := idxAll(t, h.output(), "Update memory-demo", "Press R to repair")
	if at[0] > at[1] {
		t.Errorf("order: %v", at)
	}
}

// v0.8.0 nit: Plugins, Skills and Tools rows were printed during the stage
// and again in the end card. On screen (the UI) they appear once, in the card;
// the log keeps both the stage line and the SUMMARY line.
func TestExtrasRowsAreShownOnceOnScreen(t *testing.T) {
	h, _, cards := extrasHarness(t)
	h.con.Keys = []platform.Key{key('n'), key('n'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err, h.output())
	}
	if n := strings.Count(h.output(), "Tools: ok") + strings.Count(h.output(), "Tools  ok"); n > 1 {
		t.Errorf("the Tools row is on screen %d times:\n%s", n, h.output())
	}
	c := lastCard(t, cards)
	for _, k := range []string{"Plugins", "Skills", "Tools"} {
		if _, ok := rowOf(c, k); !ok {
			t.Errorf("the card lost its %s row", k)
		}
	}
	if strings.Contains(h.output(), "Plugins: ") && strings.Contains(h.output(), "0 of 1 updated") && strings.Count(h.output(), "0 of 1 updated") > 1 {
		t.Errorf("the Plugins row is printed twice:\n%s", h.output())
	}
}

// v0.8.0 (may): Ctrl+C during a plugin step says at once that the step is
// being finished and for how long at most.
func TestCtrlCDuringAPluginStepSaysItIsFinishing(t *testing.T) {
	h, s, _ := extrasHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	s.updateRes = func(*harness) execx.Result {
		cancel()
		time.Sleep(50 * time.Millisecond)
		return execx.Result{Output: "✓ Plugin memory-demo updated.\n"}
	}
	h.con.Keys = []platform.Key{key('y'), key('n'), key('r')}
	_ = h.run()
	h.mustContain("Ctrl+C received: finishing the update of plugin memory-demo first, up to 15 min")
	// a cancelled run does not go on to ask the repair question (and never repairs)
	if strings.Contains(h.output(), "Press R to repair") || h.updates != 0 {
		t.Errorf("the repair was offered or run after Ctrl+C (updates=%d):\n%s", h.updates, h.output())
	}
}
