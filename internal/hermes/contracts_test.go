package hermes

import "testing"

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
