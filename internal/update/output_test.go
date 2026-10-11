package update

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// synthetic `hermes update` output with a failed follow-up build (shape of
// Hermes' update_receipt.py output; paths are made up)
const unfinishedOut = `→ Fetching updates
  → Building the web UI…
  ✗ Building the web UI failed; last 1 lines of output:
      Error: TypeScript build failed (2)
  ⚠ web UI build failed: C:\Users\you\tools\python.exe -I -S -c import ctypes, subprocess, sys, time
k = ctypes.WinDLL('kernel32', use_last_error=True)
k.AssignProcessToJobObject.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
sys.exit(code)
 1572 C:\Users\you\Temp\hermes-custody-abc.txt C:\Users\you\tools\node.EXE web.mjs exited 1
→ Building desktop packaged app…
  ⚠ Update follow-up 'build' did not finish: web UI build: C:\Users\you\tools\python.exe -I -S -c import ctypes, subprocess, sys, time k = ctypes.WinDLL('kernel32') exited 1 (the next launch or ` + "`hermes update`" + ` retries it)
Update complete! (v1.0 → v1.1)`

func screen(lines string, f *outputFilter) []string {
	var out []string
	for _, l := range strings.Split(lines, "\n") {
		if s := f.display(l); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func TestOutputFilterCollapsesCommandDump(t *testing.T) {
	got := screen(unfinishedOut, &outputFilter{})
	want := []string{
		"→ Fetching updates",
		"  → Building the web UI…",
		"  ✗ Building the web UI failed; last 1 lines of output:",
		"      Error: TypeScript build failed (2)",
		"  ⚠ web UI build failed (command details in the log)",
		"→ Building desktop packaged app…",
		"  ⚠ Update follow-up 'build' did not finish: web UI build (the next launch or `hermes update` retries it)",
		"Update complete! (v1.0 → v1.1)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestOutputFilterStopsHidingWithoutEndLine(t *testing.T) {
	// no "exited N" line: the next progress line ends the dump and is shown
	in := "  ⚠ web UI build failed: py.exe -I -c import x\nx = 1\n→ Building desktop packaged app…\n✓ done"
	got := screen(in, &outputFilter{})
	if len(got) != 3 || got[1] != "→ Building desktop packaged app…" || got[2] != "✓ done" {
		t.Errorf("got %q", got)
	}
	// and the cap ends it even without a progress line
	f := &outputFilter{}
	f.display("  ⚠ web UI build failed: py.exe -I -c import x")
	for i := 0; i < dumpMaxHidden; i++ {
		if s := f.display("body"); s != "" {
			t.Fatalf("line %d shown early: %q", i, s)
		}
	}
	if s := f.display("after the cap"); s != "after the cap" {
		t.Errorf("cap did not end hiding: %q", s)
	}
}

func TestOutputFilterShowsPlainLineAfterDumpEnd(t *testing.T) {
	// the "exited N" line ends the dump: a following line that is not a
	// progress step must be shown again
	in := "  ⚠ web UI build failed: py.exe -I -c import x\nx = 1\n 1572 report.txt node.exe web.mjs exited 1\n  i kept the previous web UI build"
	got := screen(in, &outputFilter{})
	if len(got) != 2 || got[1] != "  i kept the previous web UI build" {
		t.Errorf("got %q", got)
	}
}

func TestOutputFilterKeepsDisplayLine(t *testing.T) {
	f := &outputFilter{}
	for _, l := range []string{"a normal line", "  ✓ Skills are up to date", "build failed: no inline code here"} {
		if got := f.display(l); got != displayLine(l) {
			t.Errorf("display(%q) = %q, displayLine %q", l, got, displayLine(l))
		}
	}
	if f.display("Tip: You can now select a provider and model:") != "" {
		t.Error("tip should stay hidden")
	}
	// a dump start line that already carries the exit does not start hiding
	if got := f.display("  ⚠ x failed: py.exe -c import y exited 2"); got != "  ⚠ x failed (command details in the log)" || f.hiding {
		t.Errorf("got %q hiding=%v", got, f.hiding)
	}
}

func TestCleanRowDropsRepeatedKey(t *testing.T) {
	cases := map[[2]string]string{
		{"Settings", "settings: http://x unchanged"}:    "http://x unchanged",
		{"Start tile", "Start tile: tile art restored"}: "tile art restored",
		{"settings", "settings: no store"}:              "no store",
		{"Cleanup", "removed 1 set"}:                    "removed 1 set",
		{"Gateway", "gateway"}:                          "gateway",
		{"Log", "Log:"}:                                 "Log:",
	}
	for in, want := range cases {
		if got := cleanRow(ui.Row{Key: in[0], Value: in[1]}).Value; got != want {
			t.Errorf("cleanRow(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestUnfinishedStepShownInSummary(t *testing.T) {
	h := newHarness(t)
	h.onUpdate(0, unfinishedOut, true)
	h.cfg.Hooks = []config.Hook{{Name: "Start tile", When: config.HookPostUpdate, Run: []string{"tile.exe"}, TimeoutS: 5}}
	h.runner.OnFunc([]string{"tile.exe"}, func(execx.Cmd) execx.Result {
		return execx.Result{Output: "Start tile: tile art restored"}
	})
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("a step that did not finish must not fail a verified run: %v\n%s", err, h.output())
	}
	h.mustContain("RESULT: OK",
		"Hermes is updated and verified, but 1 step did not finish.",
		"Not finished",
		"Update follow-up 'build': web UI build; the next launch or `hermes update` retries it",
		"web UI build failed (command details in the log)")
	out := h.output()
	for _, bad := range []string{"AssignProcessToJobObject", "hermes-custody", "Start tile: Start tile", "Hermes is updated and verified.\n"} {
		if strings.Contains(out, bad) {
			t.Errorf("output still contains %q\n%s", bad, out)
		}
	}
}
