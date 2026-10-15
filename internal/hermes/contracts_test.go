package hermes

import (
	"strings"
	"testing"
)

// Seed fixtures for the wording contracts (D14). I1a extends these with
// fixture files under testdata/contracts/.

func TestTriggerForFirstMatchWins(t *testing.T) {
	cases := map[string]string{
		"  → Fetching updates from origin":           "fetch",
		"→ Pulling updates…":                         "pull",
		"Installing Python dependencies… (uv)":       "pydeps",
		"Building desktop packaged app… please wait": "desktop",
		"✓ Update complete! (v0.21.4 → v0.21.5)":     "restart",
		"nothing interesting":                        "",
	}
	for line, want := range cases {
		if got := TriggerFor(line); got != want {
			t.Errorf("TriggerFor(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestUpdateCompleteRe(t *testing.T) {
	m := UpdateCompleteRe.FindStringSubmatch("✓ Update complete! (v0.21.4 → v0.21.5)")
	if m == nil || m[1] != "v0.21.4" || m[2] != "v0.21.5" {
		t.Errorf("m = %v", m)
	}
}

func TestPauseGateRe(t *testing.T) {
	out := "Traceback...\nRuntimeError: Could not map Windows gateway PIDs to profiles: active gateway lock has no PID metadata\nmore"
	m := PauseGateRe.FindStringSubmatch(out)
	if m == nil || m[1] != "Could not map Windows gateway PIDs to profiles: active gateway lock has no PID metadata" {
		t.Errorf("m = %v", m)
	}
	if PauseGateRe.MatchString("RuntimeError: Could not open file") {
		t.Error("unrelated RuntimeError matched")
	}
}

func TestCheckSaysBehind(t *testing.T) {
	cases := map[string]bool{
		"Your install is 3 commits behind origin/main": true,
		"Update available: v0.21.6":                    true,
		"Already up to date.":                          false,
		"":                                             false,
	}
	for in, want := range cases {
		if got := CheckSaysBehind(in); got != want {
			t.Errorf("CheckSaysBehind(%q) = %v", in, got)
		}
	}
	if m := CheckCommitsRe.FindStringSubmatch("1 commit behind"); m == nil || m[1] != "1" {
		t.Errorf("commits: %v", m)
	}
}

func TestFileKindRegexes(t *testing.T) {
	if !NPMFilesRe.MatchString("apps/desktop/package.json") || !NPMFilesRe.MatchString("package-lock.json") || NPMFilesRe.MatchString("apackage.json") {
		t.Error("NPMFilesRe")
	}
	if !PyFilesRe.MatchString("uv.lock") || PyFilesRe.MatchString("sub/pyproject.toml") {
		t.Error("PyFilesRe is root-only")
	}
}

func TestDefaultPortRe(t *testing.T) {
	src := "import x\nexport const DEFAULT_PORT = 47891\nconst HOST = '127.0.0.1'\n"
	if m := DesktopDefaultPortRe.FindStringSubmatch(src); m == nil || m[1] != "47891" {
		t.Errorf("m = %v", m)
	}
}

func TestCuaInstalledRe(t *testing.T) {
	const exe = `C:\hermes\tools\cua-driver-0.21.0-win32-x64\cua-driver.exe`
	cases := map[string]string{
		"cua-driver installed at " + exe:                  exe,
		"cua-driver: installed at " + exe + " (0.21.0)":   exe,
		"cua-driver: installed at " + exe + " (0.21.0)\r": exe,
		"CUA-DRIVER: Installed At " + exe:                 exe,
		"cua-driver: installed at /opt/cua-driver (1.0)":  "/opt/cua-driver",
	}
	for in, want := range cases {
		m := CuaInstalledRe.FindStringSubmatch(in)
		if m == nil || m[1] != want {
			t.Errorf("%q -> %q, want %q", in, m, want)
		}
	}
	if CuaInstalledRe.MatchString("cua-driver: not installed") {
		t.Error("matched a not-installed line")
	}
}

func TestContractUnfinishedFixture(t *testing.T) {
	out := readFixture(t, "update-unfinished.txt")
	got := UnfinishedSteps(out + "\n" + out) // repeated lines are listed once
	want := Unfinished{What: "Update follow-up 'build'", Reason: "web UI build", Hint: "the next launch or `hermes update` retries it"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v", got)
	}
	if s := got[0].String(); s != "Update follow-up 'build': web UI build; the next launch or `hermes update` retries it" {
		t.Errorf("String() = %q", s)
	}
	var starts, ends int
	for _, l := range strings.Split(out, "\n") {
		if CommandDumpStartRe.MatchString(l) {
			starts++
		}
		if CommandDumpEndRe.MatchString(l) {
			ends++
		}
	}
	// only the "web UI build failed: … -c …" line starts a dump; the
	// follow-up line also echoes "-c" but says "did not finish", not "failed:"
	if starts != 1 || ends != 1 {
		t.Errorf("dump start/end lines = %d/%d, want 1/1", starts, ends)
	}
}

func TestParseUnfinishedWordings(t *testing.T) {
	cases := []struct {
		line string
		want Unfinished
		ok   bool
	}{
		{"  ⚠ Gateway multiplex migration did not finish: lock busy (run `hermes gateway migrate` later)",
			Unfinished{What: "Gateway multiplex migration", Reason: "lock busy", Hint: "run `hermes gateway migrate` later"}, true},
		{"  ⚠ Source update completion did not finish: interrupted",
			Unfinished{What: "Source update completion", Reason: "interrupted"}, true},
		// a reason ending in ordinary parentheses keeps them (no hint words)
		{"  ⚠ Source update completion did not finish: step failed (code 3)",
			Unfinished{What: "Source update completion", Reason: "step failed (code 3)"}, true},
		// a short "x: y" reason stays whole
		{"  ⚠ Update follow-up 'skills' did not finish: sync: disk full",
			Unfinished{What: "Update follow-up 'skills'", Reason: "sync: disk full"}, true},
		{"  ✓ Building desktop packaged app", Unfinished{}, false},
		{"step did not finish: no warning sign", Unfinished{}, false},
	}
	for _, c := range cases {
		got, ok := ParseUnfinished(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseUnfinished(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
	long := "  ⚠ X did not finish: " + strings.Repeat("a", 300)
	if u, _ := ParseUnfinished(long); len([]rune(u.Reason)) != 120 {
		t.Errorf("long reason not capped: %d runes", len([]rune(u.Reason)))
	}
}

// Newer Hermes builds run the Python dependency step after the desktop
// build (older ones before the frontend build); both orders must map to the
// same step keys, and the new "Resolving …" line must start the step.
func TestContractPydepsAfterBuildFixture(t *testing.T) {
	out := readFixture(t, "update-pydeps-after-build.txt")
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if step := TriggerFor(l); step != "" && (len(got) == 0 || got[len(got)-1] != step) {
			got = append(got, step)
		}
	}
	want := []string{"fetch", "pull", "frontend", "desktop", "package", "pydeps", "finalize", "restart"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, want %v", got, want)
	}
	if TriggerFor("  → Resolving Python dependencies…") != "pydeps" {
		t.Error("the Resolving line must start the pydeps step")
	}
}

func TestParseGCLocked(t *testing.T) {
	out := readFixture(t, "update-pydeps-after-build.txt")
	var hits []GCLocked
	for _, l := range strings.Split(out, "\n") {
		if !strings.Contains(l, GCSkippedMarker) {
			continue
		}
		g, ok := ParseGCLocked(l)
		if !ok {
			t.Fatalf("the fixture line was not recognised: %q", l)
		}
		hits = append(hits, g)
	}
	// the path is a Python repr: doubled backslashes must not hide the id
	want := GCLocked{Gen: "0123456789abcdef0123456789abcdef",
		Dir: `C:\Users\you\AppData\Local\hermes\installs\0123456789abcdef\environments\0123456789abcdef0123456789abcdef`}
	if len(hits) != 1 || hits[0] != want {
		t.Errorf("got %+v, want %+v", hits, want)
	}
	cases := map[string]struct {
		g  GCLocked
		ok bool
	}{
		`dependency generation cleanup skipped: [WinError 32] in use: 'C:\x\environments\abcd1234ef\venv'`: {GCLocked{"abcd1234ef", `C:\x\environments\abcd1234ef`}, true},
		`dependency generation cleanup skipped: [WinError 5] Access is denied: 'no path here'`:             {GCLocked{}, true},
		// other reasons are not "a file was in use": they stay visible as Hermes printed them
		`dependency generation cleanup skipped: [WinError 112] There is not enough space on the disk`: {GCLocked{}, false},
		`dependency generation cleanup skipped: disk error`:                                           {GCLocked{}, false},
		`Installing Python dependencies…`:                                                             {GCLocked{}, false},
	}
	for line, w := range cases {
		if g, ok := ParseGCLocked(line); g != w.g || ok != w.ok {
			t.Errorf("ParseGCLocked(%q) = %+v, %v; want %+v, %v", line, g, ok, w.g, w.ok)
		}
	}
}

// "→ Found N new commit(s)" is the commit count the update really pulled.
func TestContractFoundCommitsFixture(t *testing.T) {
	out := readFixture(t, "update-pydeps-after-build.txt")
	if n, ok := ParseFoundCommits(out); !ok || n != 29 {
		t.Errorf("fixture: got %d, %v; want 29", n, ok)
	}
	cases := map[string]struct {
		n  int
		ok bool
	}{
		"→ Found 1 new commit(s)":       {1, true},
		"  → Found 105 new commit(s)\r": {105, true},
		"→ Found 0 new commit(s)":       {0, false},
		"Already up to date.":           {0, false},
		"Found many new commit(s)":      {0, false},
	}
	for in, want := range cases {
		if n, ok := ParseFoundCommits(in); n != want.n || ok != want.ok {
			t.Errorf("%q: got %d, %v", in, n, ok)
		}
	}
}

func TestTriggerForWebUIBuild(t *testing.T) {
	// source_build.py labels the web UI build "Building the web UI"; the
	// progress line is "  → Building the web UI…" (U+2026).
	for line, want := range map[string]string{
		"  → Building the web UI…":        "frontend",
		"  ✓ Building the web UI":         "", // the done line is not a trigger
		"  → Building the TUI…":           "frontend",
		"  ⚠ web UI build failed: x":      "",
		"Updating Python dependencies":    "pydeps", // older Hermes; harmless on v0.22
		"  → Updating Node dependencies…": "",
	} {
		if got := TriggerFor(line); got != want {
			t.Errorf("TriggerFor(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestContractOwedFixture(t *testing.T) {
	out := readFixture(t, "update-owed.txt")
	want := []string{
		"TUI build failed: node /home/you/hermes-agent/scripts/build/tui.mjs exited 2",
		"desktop app build failed: node /home/you/hermes-agent/scripts/build/desktop.mjs exited 1",
		"Skipped rebuilding the desktop app: this update is running inside it (pid 4242)",
		"Desktop app build owed: the update completion did not finish",
		"Gateway restart still owed: 2 gateway(s) still running the old code (or stopped)",
		"Interrupted after the code was updated: the new code is in place; its remaining steps are owed and the next launch or `hermes update` finishes them",
	}
	for name, text := range map[string]string{"LF": out, "CRLF": strings.ReplaceAll(out, "\n", "\r\n")} {
		var got []string
		for _, u := range UnfinishedSteps(text + "\n" + text) { // repeats are listed once
			got = append(got, u.String())
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: got\n  %s\nwant\n  %s", name, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}

func TestParseOwedWordings(t *testing.T) {
	cases := []struct {
		line string
		want string // Unfinished.String(), "" = not an owed line
	}{
		{"  Desktop app build owed: web UI build failed", "Desktop app build owed: web UI build failed"},
		{"Desktop app build owed: the source-update tail did not finish\r", "Desktop app build owed: the source-update tail did not finish"},
		{"⚠ Gateway restart still owed: 1 gateway(s) still running the old code (or stopped).", "Gateway restart still owed: 1 gateway(s) still running the old code (or stopped)"},
		{"  ⚠ Skipped rebuilding the desktop app: this update is running inside it (pid 7),", "Skipped rebuilding the desktop app: this update is running inside it (pid 7)"},
		{"  ⚠ Node dependencies failed: npm exited 1", "Node dependencies failed: npm exited 1"},
		{"  ⚠ web UI build failed: python.exe -I -S -c import ctypes, subprocess", "web UI build failed: command details in the log"},
		// not owed work
		{"  ✓ Building desktop packaged app", ""},
		{"Gateway restart is not owed", ""},
		{"  ⚠ Update follow-up 'build' did not finish: web UI build failed: x (retries it)", ""}, // ParseUnfinished's line
		{"the build failed: nothing to see", ""},                                                 // no warning sign
	}
	for _, c := range cases {
		u, ok := ParseOwed(c.line)
		if (c.want != "") != ok || (ok && u.String() != c.want) {
			t.Errorf("ParseOwed(%q) = %q, %v; want %q", c.line, u.String(), ok, c.want)
		}
	}
}

func TestUnfinishedStepsDeduplicates(t *testing.T) {
	// Hermes prints the "⚠ <name> failed" line AND records a follow-up that
	// names the same product: one failure is one row.
	both := "  ⚠ web UI build failed: node build.mjs exited 1\n" +
		"  ⚠ Update follow-up 'build' did not finish: web UI build: node build.mjs exited 1 (the next launch or `hermes update` retries it)\n"
	if got := UnfinishedSteps(both); len(got) != 1 || got[0].What != "Update follow-up 'build'" {
		t.Errorf("failed + follow-up = %+v, want only the follow-up", got)
	}
	// Without the follow-up (its text can be cut off) the failure stays visible.
	if got := UnfinishedSteps("  ⚠ web UI build failed: node build.mjs exited 1\n"); len(got) != 1 {
		t.Errorf("failed alone = %+v", got)
	}
	// The gateway_restart follow-up already says "gateway restart owed".
	gw := readFixture(t, "update-fleet-followup.txt") + "⚠ Gateway restart still owed: 1 gateway(s) still running the old code (or stopped).\n"
	if got := UnfinishedSteps(gw); len(got) != 1 || got[0].What != "Update follow-up 'gateway_restart'" {
		t.Errorf("gateway follow-up + owed = %+v, want one row", got)
	}
}

func TestContractFleetFollowupFixture(t *testing.T) {
	// Hermes v0.22 (contract C3): the fleet check never fails the update. The
	// known-false-fleet text still prints, but the exit code is 0 and the
	// follow-up line is the signal.
	out := readFixture(t, "update-fleet-followup.txt")
	if !strings.Contains(out, KnownFalseFleet) {
		t.Fatal("fixture lost the known false fleet text")
	}
	got := UnfinishedSteps(out)
	if len(got) != 1 || got[0].What != "Update follow-up 'gateway_restart'" ||
		!strings.Contains(got[0].Reason, "gateways may still run pre-update code") ||
		got[0].Hint != "the next launch or `hermes update` retries it" {
		t.Fatalf("got %+v", got)
	}
	if m := UpdateCompleteRe.FindStringSubmatch(out); m == nil || m[1] != "v1.0" || m[2] != "v1.1" {
		t.Errorf("complete = %v", m)
	}
	var steps []string
	for _, l := range strings.Split(out, "\n") {
		if s := TriggerFor(l); s != "" && (len(steps) == 0 || steps[len(steps)-1] != s) {
			steps = append(steps, s)
		}
	}
	if want := "fetch pull frontend finalize restart"; strings.Join(steps, " ") != want {
		t.Errorf("steps = %v, want %s", steps, want)
	}
}
