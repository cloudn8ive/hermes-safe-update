package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// runSession is the scripted run behind the golden files: a real-looking
// update with banners, rows, updater output, a prompt and a summary card.
func yes() *bool { b := true; return &b }

func runSession(r Renderer, est *timings.Estimator, clk *testutil.Clock, con *platform.FakeConsole) {
	adv := func(s float64) { clk.Advance(time.Duration(s * float64(time.Second))) }
	tick := func() {
		if v, ok := r.(*VT); ok {
			v.Tick()
		}
	}
	r.Start("Hermes Safe Update", est.Steps, est, true)
	r.Stage("local")
	r.Section("Pre-flight")
	r.Rows(
		Row{Key: "disk free", Value: "120.4 GB (enough)"},
		Row{Key: "checkout", Value: "clean"},
		Row{Key: "gateway", Value: "running the new code 1b8569f41f"},
	)
	adv(1)
	r.Stage("remote")
	adv(40)
	tick()
	r.Rows(
		Row{Key: "updates", Value: "3 new commits on main"},
		Row{Key: "version", Value: "v0.21.5+4156.g34e09bf"},
		Row{Key: "running", Value: "desktop, gateway ×2"},
	)
	r.Stage("procs")
	adv(3)
	r.Rows(Row{Key: "sessions", Value: "2 active\n  · 20260101_000000_aaaaaa  Drafting the weekly project summary\n  · 20260101_000001_bbbbbb  Fix the nightly build"})
	r.Stage("wait")
	con.Keys = []platform.Key{{Other: true}, {Rune: 'y'}}
	a, _ := r.Prompt(context.Background(), PromptSpec{
		Text:           "Hermes will be closed and updated: about 8 min. Nothing is lost, but the desktop app and the gateway are stopped meanwhile. Press Y to go on, N to cancel.",
		Keys:           []rune{'y', 'n'},
		Timeout:        30 * time.Second,
		CountdownLabel: "auto-cancel in",
	})
	_ = a
	r.Stage("close")
	r.Line(LevelNote, "closing the desktop app (graceful)")
	adv(8)
	r.Stage("gateway_stop")
	adv(20)
	r.Stage("fetch")
	r.Command("hermes update --yes --keep-stash --branch main")
	r.Output("→ Fetching updates...")
	r.Output("")
	r.Output("  ✓ Configuration is up to date")
	r.Output("Tip: You can now select a provider and model:")
	adv(10)
	r.Stage("pull")
	r.Output("☤ Pulling 3 commits")
	r.Output("  ⚠ skills sync skipped for 1 profile")
	r.Line(LevelWarn, "WARN the first sync is slow")
	adv(90)
	tick()
	r.Stage("frontend")
	adv(25)
	r.Stage("desktop")
	adv(200)
	tick()
	r.Stage("verify")
	r.Section("Verify")
	r.Output("✓ Update complete! (v0.21.5+4156.g34e09bf → v0.21.5+4160.g1b8569f)")
	r.Line(LevelResult, "RESULT: OK")
	adv(5)
	r.Stage("cua") // hooks and relaunch come before it; the last step ends the run
	adv(3)
	r.Complete(yes())
	r.Summary(Card{OK: yes(), Headline: "Hermes is updated and verified.", Rows: []Row{
		{Key: "Version", Value: "v0.21.5+4156.g34e09bf → v0.21.5+4160.g1b8569f  (3 commits)"},
		{Key: "Time", Value: "5:19 until Hermes reopened  (estimate was 6:26)"},
		{Key: "Source", Value: "local mirror, refreshed 10 min ago"},
		{Key: "Warnings", Value: "1 (see above)"},
		{Key: "Log", Value: `C:\Users\you\AppData\Local\hermes\logs\safe-update.log`},
	}})
	r.Close()
}

func scripted(h *harness) {
	h.startUpdate(true)
	runSessionOn(h)
}

func runSessionOn(h *harness) {
	runSession(h.r, h.est, h.clk, h.con)
}

// plainDiff checks the one rule that matters for glyphs: every rune written is
// ASCII, a theme glyph, a group icon, or in the small set the look uses.
func allowedRunes() map[rune]bool {
	th := config.Defaults().Theme
	ok := map[rune]bool{}
	add := func(s string) {
		for _, r := range s {
			ok[r] = true
		}
	}
	g := th.Glyphs
	for _, s := range []string{g.OK, g.Err, g.Warn, g.Info, g.Command, g.Active, g.Pending, g.Dot, g.Bar, g.Rule, g.RuleThin, g.Sep, g.Gutter, g.Arrow, g.Title, g.Section} {
		add(s)
	}
	for _, h := range th.Hues {
		add(h.Icon)
	}
	for _, i := range th.SectionIcons {
		add(i)
	}
	add("…") // "Starting…"
	add("×") // desktop, gateway ×2 (row text)
	return ok
}

func checkGlyphs(t *testing.T, out string) {
	t.Helper()
	ok := allowedRunes()
	bad := map[rune]bool{}
	for _, r := range out {
		if r < 0x80 || ok[r] || unicode.IsControl(r) {
			continue
		}
		bad[r] = true
	}
	for r := range bad {
		t.Errorf("non-whitelisted rune %q (U+%04X) emitted", r, r)
	}
}

func goldenName(base string, cols int, ext string) string {
	return base + "-" + itoa(cols) + ext
}

func newSessionHarness(t *testing.T, cols int, color bool) *harness {
	h := newHarness(t, cols, color)
	tun := config.Defaults().Tunables
	h.est = timings.NewEstimator(timings.KindUpdate, timings.UpdateSteps(tun.StepSeconds), nil, tun, h.clk)
	return h
}

// vtVisual renders raw VT output as readable text for golden review: escape
// sequences are kept but ESC is written as ␛-free "<ESC>", and CRs shown.
func vtVisual(s string) string {
	s = strings.ReplaceAll(s, "\x1b", "<ESC>")
	s = strings.ReplaceAll(s, "\r", "<CR>")
	return s
}

func TestGoldenVTSession(t *testing.T) {
	for _, cols := range []int{80, 120} {
		for _, color := range []bool{true, false} {
			name := goldenName("session-vt", cols, ".golden")
			if !color {
				name = goldenName("session-vt-nocolor", cols, ".golden")
			}
			t.Run(name, func(t *testing.T) {
				h := newSessionHarness(t, cols, color)
				runSession(h.r, h.est, h.clk, h.con)
				out := h.out()
				checkGlyphs(t, out)
				if !color && strings.Contains(out, "38;") {
					t.Error("NO_COLOR session has colour sequences")
				}
				testutil.Golden(t, name, []byte(vtVisual(out)))
			})
		}
	}
}

func TestPlainSessionGoldens(t *testing.T) {
	for _, cols := range []int{80, 120} {
		t.Run(goldenName("session-plain", cols, ".golden"), func(t *testing.T) {
			clk := testutil.NewClock(t0)
			con := &platform.FakeConsole{Cols: cols, Rows: 30}
			var buf bytes.Buffer
			r := NewPlain(&buf, con)
			r.SetClock(clk)
			// The session answers its prompt from queued keys, so whether the
			// countdown goroutine gets to print "  30s" before the answer is a
			// scheduling accident. The countdown has its own tests; keep it
			// out of this golden.
			r.countEvery = time.Hour
			tun := config.Defaults().Tunables
			est := timings.NewEstimator(timings.KindUpdate, timings.UpdateSteps(tun.StepSeconds), nil, tun, clk)
			runSession(r, est, clk, con)
			out := buf.String()
			if strings.Contains(out, "\x1b") {
				t.Errorf("plain output has escapes: %q", out)
			}
			// raw updater output ("  | " lines) is passed through untouched; the log keeps it too
			var own []string
			for _, l := range strings.Split(out, "\n") {
				if !strings.HasPrefix(l, "  | ") {
					own = append(own, l)
				}
			}
			checkGlyphs(t, strings.Join(own, "\n"))
			testutil.Golden(t, goldenName("session-plain", cols, ".golden"), []byte(out))
		})
	}
}
