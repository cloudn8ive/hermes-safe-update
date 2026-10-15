package update

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Summary rows of a release channel (Safe Update v0.3.0): the Channel row,
// the Version row's count, the prediction against the release target and the
// Newer row. Hermes' texts are the synthetic fixtures of internal/hermes.

const (
	relSHA   = "abcdef0123456789abcdef0123456789abcdef01" // the release commit, 40 hex
	relSHA12 = "abcdef012345"                             // as Hermes prints it
)

// stableHarness is a run on mode stable whose check names v0.22.1.
func stableHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check", "--channel", "stable")
	h.stableUpdateStream(t)
	h.con.Keys = []platform.Key{key('y')}
	return h
}

// ---- Channel row ----

func TestChannelRowFromConfig(t *testing.T) {
	cases := []struct {
		name string
		mode string
		flag bool
		want string
	}{
		{"stable from the yaml", config.ChannelStable, false, "stable (update.channel in safe-update.yaml)"},
		{"stable from --channel", config.ChannelStable, true, "stable (--channel)"},
		{"main from the yaml", config.ChannelMain, false, "main (update.channel in safe-update.yaml)"},
		{"main from --channel", config.ChannelMain, true, "main (--channel)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := stableHarness(t)
			if c.mode == config.ChannelMain {
				// a main run: `--branch main` makes Hermes print no channel line
				h.onUpdate(0, "→ Fetching updates\nUpdate complete! (v1.0 → v1.1)", true)
			}
			h.cfg.Update.Channel, h.cfg.Run.ChannelFlag = c.mode, c.flag
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if got := cardRow(h.output(), "Channel"); got != c.want {
				t.Errorf("Channel = %q, want %q\n%s", got, c.want, h.output())
			}
		})
	}
}

func TestChannelRowFromResolution(t *testing.T) {
	cases := []struct {
		name   string
		help   string // "" = the probe gets no answer
		record string
		origin string
		bare   bool // Hermes' own channel cannot be read offline
		want   string
	}{
		{"a record written in Hermes", helpWithChannels, "main", officialURL, false, "main (set in Hermes)"},
		{"a stable record", helpWithChannels, "stable", officialURL, false, "stable (set in Hermes)"},
		{"no record, the official checkout", helpWithChannels, "", officialURL, false, "stable (Hermes default)"},
		{"no record, a fork", helpWithChannels, "", "https://github.com/someone/fork.git", false, "main (Hermes default)"},
		{"a Hermes without release channels", helpLegacy, "", officialURL, false, "main (this Hermes has no release channels)"},
		{"the probe failed", "", "", officialURL, false, "main (could not ask Hermes about channels)"},
		{"read from the check's first line", helpWithChannels, "", officialURL, true, "stable (reported by Hermes)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.Update.Channel = config.ChannelFollow
			if !c.bare {
				h.channelHome(t, c.record, c.origin)
			}
			if c.help != "" {
				h.hermesHelp(c.help)
			}
			if strings.HasPrefix(c.want, "stable") {
				h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check")
				h.stableUpdateStream(t)
			} // else a main run: the default `--branch main` check and update
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if got := cardRow(h.output(), "Channel"); got != c.want {
				t.Errorf("Channel = %q, want %q\n%s", got, c.want, h.output())
			}
		})
	}
}

// The update's own "Update channel" line is the last word: if Hermes acted
// on another channel than the plan expected, the row says what Hermes said.
func TestChannelRowFromUpdateOutput(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelFollow
	h.channelHome(t, "stable", officialURL)
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check")
	h.onUpdate(0, strings.ReplaceAll(fixture(t, "update-stable-switching.txt"), "Update channel: stable", "Update channel: canary"), true)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if got := cardRow(h.output(), "Channel"); got != "canary (reported by Hermes)" {
		t.Errorf("Channel = %q\n%s", got, h.output())
	}

	// and when no stage has a plan, the update output alone names it
	r := &Run{Facts: Facts{Channel: ChannelFacts{Applied: hermes.UpdateRelease{Channel: "stable"}}}}
	if got := r.channelRow(); got != "stable" {
		t.Errorf("channelRow without a plan = %q", got)
	}
	if got := (&Run{}).channelRow(); got != "" {
		t.Errorf("channelRow with nothing known = %q", got)
	}
}

// The main path gets the row too, and the check card has it as well.
func TestChannelRowOnMainAndInTheCheckCard(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelMain
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if got := cardRow(h.output(), "Channel"); got != "main (update.channel in safe-update.yaml)" {
		t.Errorf("run card: Channel = %q", got)
	}
	// the order: Channel follows Version
	if v, c := strings.Index(h.output(), "  Version "), strings.Index(h.output(), "  Channel "); v < 0 || c < v {
		t.Errorf("Channel row not after Version:\n%s", h.output())
	}

	for _, mode := range []string{config.ChannelMain, config.ChannelStable} {
		c := stableHarness(t)
		c.mode = ModeCheck
		c.cfg.Update.Channel = mode
		if err := c.run(); err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, c.output())
		}
		want := mode + " (update.channel in safe-update.yaml)"
		if got := cardRow(c.output(), "Channel"); got != want {
			t.Errorf("check card: Channel = %q, want %q\n%s", got, want, c.output())
		}
	}
}

// ---- Version row ----

func TestVersionRowNoForecastForRelease(t *testing.T) {
	h := stableHarness(t)
	// the prediction counted 7 commits to the release; git cannot count the
	// real move afterwards, and a release update has no "Found N" line
	h.mirror = &fakeMirror{relCompare: &mirror.Compare{Commits: 7, Status: "ahead", FilesComplete: true}}
	h.repo.countOK = false
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if got := cardRow(h.output(), "Version"); got != "v0.22.0 → v0.22.1" {
		t.Errorf("Version = %q: a release must not fall back to the forecast", got)
	}
	h.mustContain("release v0.22.1 available, 7 commits") // the forecast itself was shown before the update

	// the same facts on main do fall back to the forecast
	m := newHarness(t)
	m.cfg.Update.Channel = config.ChannelMain
	m.con.Keys = []platform.Key{key('y')}
	m.repo.countOK = false
	if err := m.run(); err != nil {
		t.Fatal(err)
	}
	if got := cardRow(m.output(), "Version"); got != "v1.0 → v1.1  (3 commits)" {
		t.Errorf("main Version = %q", got)
	}
}

func TestVersionRowRewindNoCount(t *testing.T) {
	cases := []struct {
		name      string
		fwd, back int
		fwdOK     bool
		found     string
		want      string
	}{
		{"a forward move", 9, 0, true, "", "v1.0 → v1.1  (9 commits)"},
		{"a single commit", 1, 0, true, "", "v1.0 → v1.1  (1 commit)"},
		{"moved back", 0, 38, true, "→ Found 7 new commit(s)\n", "v1.0 → v1.1  (moved back 38 commits)"},
		{"moved back one", 0, 1, true, "", "v1.0 → v1.1  (moved back 1 commit)"},
		// the new HEAD lacks commits the old one had: no number, whatever the
		// Found line or the forecast says
		{"histories diverged", 3, 2, true, "→ Found 7 new commit(s)\n", "v1.0 → v1.1"},
		{"git cannot count: the Found line", 0, 0, false, "→ Found 7 new commit(s)\n", "v1.0 → v1.1  (7 commits)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.Update.Channel = config.ChannelMain
			h.repo.count, h.repo.countOK, h.repo.back = c.fwd, c.fwdOK, c.back
			h.onUpdate(0, c.found+"Update complete! (v1.0 → v1.1)", true)
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatal(err)
			}
			if got := cardRow(h.output(), "Version"); got != c.want {
				t.Errorf("Version = %q, want %q\n%s", got, c.want, h.output())
			}
		})
	}
}

// ---- prediction against the release target ----

func releaseHarnessWith(t *testing.T, edit func(*harness, *fakeMirror)) (*harness, *fakeMirror) {
	t.Helper()
	h := stableHarness(t)
	h.mode = ModeCheck
	m := &fakeMirror{}
	h.mirror = m
	if edit != nil {
		edit(h, m)
	}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	return h, m
}

func TestPredictReleaseFromTheLocalTag(t *testing.T) {
	yes := true
	h, m := releaseHarnessWith(t, func(h *harness, m *fakeMirror) {
		h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.1", relSHA
		h.repo.pairs = map[[2]string]int{{oldSHA, relSHA}: 7}
		h.repo.diffNPM = &yes
		f := false
		h.repo.diffPy = &f
	})
	h.mustContain("release v0.22.1 available, 7 commits (Hermes release check)")
	if got := cardRowTight(h.output(), "Node packages"); !strings.HasPrefix(got, "changed") {
		t.Errorf("Node packages = %q", got)
	}
	if got := cardRow(h.output(), "Python deps"); got != "unchanged" {
		t.Errorf("Python deps = %q", got)
	}
	if got := cardRow(h.output(), "Available"); got != "release v0.22.1 (7 commits)" {
		t.Errorf("Available = %q", got)
	}
	// asked about the tag Hermes named, diffed against the tag, never main
	if len(h.repo.resolved) != 1 || h.repo.resolved[0] != [2]string{"v0.22.1", relSHA12} {
		t.Errorf("ResolveRelease calls: %v", h.repo.resolved)
	}
	if len(h.repo.diffRefs) != 1 || h.repo.diffRefs[0] != "refs/tags/v0.22.1" {
		t.Errorf("DiffKinds refs: %v", h.repo.diffRefs)
	}
	if len(m.relCompares) != 0 {
		t.Errorf("GitHub was asked although the release is local: %v", m.relCompares)
	}
}

func TestPredictReleaseFromTheFullShaWhenTheTagIsNotLocal(t *testing.T) {
	h, _ := releaseHarnessWith(t, func(h *harness, m *fakeMirror) {
		h.repo.relRef, h.repo.relSHA = relSHA, relSHA
		h.repo.pairs = map[[2]string]int{{oldSHA, relSHA}: 2}
	})
	h.mustContain("release v0.22.1 available, 2 commits")
	if len(h.repo.diffRefs) != 1 || h.repo.diffRefs[0] != relSHA {
		t.Errorf("DiffKinds refs: %v", h.repo.diffRefs)
	}
}

func TestPredictReleaseFromTheGitHubCompare(t *testing.T) {
	h, m := releaseHarnessWith(t, func(h *harness, m *fakeMirror) {
		m.relCompare = &mirror.Compare{Commits: 7, Files: []string{"apps/desktop/package.json", "README.md"}, FilesComplete: true, Status: "ahead"}
	})
	h.mustContain("release v0.22.1 available, 7 commits")
	if got := cardRowTight(h.output(), "Node packages"); !strings.HasPrefix(got, "changed") {
		t.Errorf("Node packages = %q", got)
	}
	if got := cardRow(h.output(), "Python deps"); got != "unchanged" {
		t.Errorf("Python deps = %q", got)
	}
	if len(m.relCompares) != 1 || m.relCompares[0] != [2]string{oldSHA, "v0.22.1"} {
		t.Errorf("APICompareTo calls: %v (want HEAD...the tag)", m.relCompares)
	}
	if len(h.repo.diffRefs) != 0 {
		t.Errorf("git diffed %v although the compare answered", h.repo.diffRefs)
	}
}

// A compare whose answer is not an exact base-to-target diff gives a count
// but never "unchanged".
func TestPredictReleaseDivergedCompareKnowsNoPackages(t *testing.T) {
	h, _ := releaseHarnessWith(t, func(h *harness, m *fakeMirror) {
		m.relCompare = &mirror.Compare{Commits: 2, Status: "diverged"}
	})
	h.mustContain("release v0.22.1 available, 2 commits")
	if got := cardRowTight(h.output(), "Node packages"); got != "unknown" {
		t.Errorf("Node packages = %q: a diverged compare proves nothing", got)
	}
	if got := cardRow(h.output(), "Python deps"); got != "unknown" {
		t.Errorf("Python deps = %q", got)
	}
}

// Nothing local and no answer from GitHub: unknown, never the main diff.
func TestPredictReleaseUnknownIsNeverTheMainDiff(t *testing.T) {
	h, _ := releaseHarnessWith(t, nil)
	h.mustContain("release v0.22.1 available (Hermes release check)")
	if strings.Contains(cardRow(h.output(), "Available"), "commit") {
		t.Errorf("a count appeared from nowhere: %q", cardRow(h.output(), "Available"))
	}
	for _, k := range []string{"Python deps", "Node packages"} {
		if got := cardRowTight(h.output(), k); got != "unknown" {
			t.Errorf("%s = %q, want unknown", k, got)
		}
	}
	for _, ref := range h.repo.diffRefs {
		t.Errorf("DiffKinds(%q) ran with no release target", ref)
	}
}

func TestPredictReleaseSeedsFromTheMirror(t *testing.T) {
	four := 4
	h, m := releaseHarnessWith(t, func(h *harness, m *fakeMirror) {
		m.st = mirror.Status{OK: true, ConfiguredPath: `X:\m`, JobActive: true}
		m.relSeed = &mirror.ReleaseSeed{SeedResult: mirror.SeedResult{Used: true, Reason: "ok", Complete: true, Commits: &four}, TargetSHA: relSHA}
		h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.1", relSHA
		// git cannot count the range, so the seed's own count is used
	})
	if len(m.relSeeds) != 1 || m.relSeeds[0] != [3]string{h.install.Checkout, "v0.22.1", relSHA12} {
		t.Errorf("SeedRelease calls: %v", m.relSeeds)
	}
	if m.seeded {
		t.Error("main's Seed ran on a release channel")
	}
	h.mustContain("release v0.22.1 available, 4 commits", "seeded from the local mirror", "nothing left to download from GitHub: yes")
}

func TestPredictReleaseSkipsTheSeedWithoutAMirrorOrATag(t *testing.T) {
	// no mirror set up: only Status() is read
	_, m := releaseHarnessWith(t, nil)
	if len(m.relSeeds) != 0 {
		t.Errorf("SeedRelease ran without a mirror: %v", m.relSeeds)
	}
	// a release channel whose version is no strict tag (canary): the commit
	// Hermes names is looked up locally, but no tag is invented
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.mode = ModeCheck
	m = &fakeMirror{st: mirror.Status{OK: true, ConfiguredPath: `X:\m`, JobActive: true}}
	h.mirror = m
	h.answer(execx.Result{Output: strings.ReplaceAll(fixture(t, "update-check-stable-available.txt"), "v0.22.1", "v0.22.2+canary.20261001T000000Z")}, "update", "--check", "--channel", "stable")
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if len(m.relSeeds) != 0 || len(m.relCompares) != 0 {
		t.Errorf("a canary version was turned into a tag: seeds=%v compares=%v", m.relSeeds, m.relCompares)
	}
	if len(h.repo.resolved) != 1 || h.repo.resolved[0] != [2]string{"", relSHA12} {
		t.Errorf("ResolveRelease calls: %v", h.repo.resolved)
	}
}

// ---- Newer row on a release channel ----

func newerReleaseHarness(t *testing.T, n mirror.NewerRelease, ok bool) (*harness, *fakeMirror) {
	t.Helper()
	h := stableHarness(t)
	m := &fakeMirror{newerRel: n, newerRelOK: ok, newer: mirror.Newer{Ahead: true, Commits: ptrInt(50)}, newerOK: true}
	h.mirror = m
	return h, m
}

func TestNewerReleaseAhead(t *testing.T) {
	h, m := newerReleaseHarness(t, mirror.NewerRelease{Ahead: true, Tag: "v0.22.2", Commits: ptrInt(3)}, true)
	if err := h.run(); err != nil {
		t.Fatalf("a newer release must never fail the run: %v", err)
	}
	const want = "release v0.22.2 was published while this ran; run Safe Update again when convenient"
	if got := cardRow(h.output(), "Newer"); got != want {
		t.Errorf("Newer = %q, want %q\n%s", got, want, h.output())
	}
	// asked about the installed HEAD, with a short deadline; main was not looked at
	if m.newerRelCalls != 1 || m.newerRelHead != newSHA || !m.newerRelDl {
		t.Errorf("NewerRelease x%d head=%q deadline=%v", m.newerRelCalls, m.newerRelHead, m.newerRelDl)
	}
	if m.newerCalls != 0 {
		t.Errorf("main's NewerThan ran on a release channel")
	}
}

func TestNewerReleaseIdenticalBehindAndAPIDown(t *testing.T) {
	cases := []struct {
		name string
		n    mirror.NewerRelease
		ok   bool
	}{
		{"identical: the latest release is what was installed", mirror.NewerRelease{Tag: "v0.22.1"}, true},
		{"behind: the latest release is older", mirror.NewerRelease{Tag: "v0.22.0"}, true},
		{"the API is down", mirror.NewerRelease{}, false},
		{"a stale answer from a failure", mirror.NewerRelease{Ahead: true, Tag: "v0.22.2"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, m := newerReleaseHarness(t, c.n, c.ok)
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if got := cardRow(h.output(), "Newer"); got != "" {
				t.Errorf("row shown: %q", got)
			}
			if m.newerRelCalls != 1 {
				t.Errorf("NewerRelease called %d times", m.newerRelCalls)
			}
			h.mustContain("Hermes is updated and verified.")
		})
	}
}

// Main moving says nothing on a release channel: even when main is far ahead,
// no "more commits" row appears.
func TestNoNewerRowOnStableWhenMainMoves(t *testing.T) {
	h, m := newerReleaseHarness(t, mirror.NewerRelease{Tag: "v0.22.1"}, true)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "landed on GitHub") || strings.Contains(h.output(), "more commit") {
		t.Errorf("main's movement was reported on a release channel:\n%s", h.output())
	}
	if m.newerCalls != 0 {
		t.Errorf("NewerThan called %d times", m.newerCalls)
	}
}

func TestNewerReleaseNeverRunsForAnUnverifiedUpdateOrACheck(t *testing.T) {
	h, m := newerReleaseHarness(t, mirror.NewerRelease{Ahead: true, Tag: "v0.22.2"}, true)
	h.verifier.ok = false
	_ = h.run()
	if m.newerRelCalls != 0 || cardRow(h.output(), "Newer") != "" {
		t.Errorf("unverified: calls=%d row=%q", m.newerRelCalls, cardRow(h.output(), "Newer"))
	}
	c, m2 := newerReleaseHarness(t, mirror.NewerRelease{Ahead: true, Tag: "v0.22.2"}, true)
	c.mode = ModeCheck
	if err := c.run(); err != nil {
		t.Fatal(err)
	}
	if m2.newerRelCalls != 0 || strings.Contains(c.output(), "was published while this ran") {
		t.Errorf("--check asked: calls=%d", m2.newerRelCalls)
	}
}

// Without a mirror service nothing can be asked: no row.
func TestNewerReleaseWithoutMirrorServiceIsOmitted(t *testing.T) {
	h := stableHarness(t)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if got := cardRow(h.output(), "Newer"); got != "" {
		t.Errorf("row shown: %q", got)
	}
}

// slowReleaseMirror blocks in NewerRelease until its context is done.
type slowReleaseMirror struct{ *fakeMirror }

func (s *slowReleaseMirror) NewerRelease(ctx context.Context, _ string) (mirror.NewerRelease, bool) {
	<-ctx.Done()
	return mirror.NewerRelease{}, false
}

func TestNewerReleaseIsBoundedByTheBudget(t *testing.T) {
	old := newerBudget
	newerBudget = 50 * time.Millisecond
	defer func() { newerBudget = old }()
	h := stableHarness(t)
	h.mirror = &slowReleaseMirror{&fakeMirror{}}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v", err)
	}
	h.mustContain("Hermes is updated and verified.")
	if got := cardRow(h.output(), "Newer"); got != "" {
		t.Errorf("row shown: %q", got)
	}
}

// cardRowTight is cardRow for a label as wide as the label column ("Node
// packages"), which leaves one space before the value.
func cardRowTight(out, key string) string {
	m := regexp.MustCompile(`(?m)^\s{2}` + regexp.QuoteMeta(key) + `\s+(.*?)\s*$`).FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}
