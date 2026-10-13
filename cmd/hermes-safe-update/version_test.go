package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func fakeInfo(settings ...string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		bi := &debug.BuildInfo{}
		for i := 0; i+1 < len(settings); i += 2 {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
		}
		return bi, true
	}
}

func TestResolveVersion(t *testing.T) {
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	cases := []struct {
		name, ldflag string
		info         func() (*debug.BuildInfo, bool)
		want         string
	}{
		{"release is untouched, VCS stamp ignored", "v0.1.2", fakeInfo("vcs.revision", "3a8a2c7ffffffff", "vcs.modified", "true"), "v0.1.2"},
		{"derived dev version is untouched", "v0.1.2+23.3a8a2c7.dirty", none, "v0.1.2+23.3a8a2c7.dirty"},
		{"CI style", "refs-3a8a2c7", none, "refs-3a8a2c7"},
		{"dev with a clean commit", "dev", fakeInfo("vcs.revision", "3a8a2c7ffffffff", "vcs.modified", "false"), "dev+3a8a2c7"},
		{"empty ldflag counts as dev", "", fakeInfo("vcs.revision", "3a8a2c7ffffffff"), "dev+3a8a2c7"},
		{"dev with local changes", "dev", fakeInfo("vcs.revision", "3a8a2c7ffffffff", "vcs.modified", "true"), "dev+3a8a2c7.dirty"},
		{"short revision kept", "dev", fakeInfo("vcs.revision", "abc"), "dev+abc"},
		{"dev without VCS settings", "dev", fakeInfo("-trimpath", "true"), "dev"},
		{"dev without build info", "dev", none, "dev"},
	}
	for _, c := range cases {
		if got := resolveVersion(c.ldflag, c.info); got != c.want {
			t.Errorf("%s: resolveVersion(%q) = %q, want %q", c.name, c.ldflag, got, c.want)
		}
	}
}

func TestToolVersionPinnedBeatsBuild(t *testing.T) {
	if got := (&environment{ver: "v9"}).toolVersion(); got != "v9" {
		t.Errorf("pinned = %q", got)
	}
	want := resolveVersion(version, readBuildInfo)
	if got := (&environment{}).toolVersion(); got != want || got == "" {
		t.Errorf("unpinned = %q, want %q", got, want)
	}
}

// The rule that keeps release binaries reproducible: with an explicit
// version the scripts must not consult git at all.
func TestBuildScriptsOnlyDeriveWhenNoVersionIsGiven(t *testing.T) {
	sh, err := os.ReadFile(filepath.Join(scriptsDir, "build-all.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sh), `VERSION="${1:-dev}"`) || !strings.Contains(string(sh), `if [ "$VERSION" = dev ]`) {
		t.Error("build-all.sh must use an explicit VERSION as given and derive only for dev/empty")
	}
	ps, err := os.ReadFile(filepath.Join(scriptsDir, "build-all.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ps), `if (-not $Version -or $Version -eq "dev")`) {
		t.Error("build-all.ps1 must use an explicit -Version as given and derive only for dev/empty")
	}
}

// copyDevVersionScripts puts both derivation scripts under root/scripts.
func copyDevVersionScripts(t *testing.T, root string) {
	t.Helper()
	for _, f := range []string{"dev-version.sh", "dev-version.ps1"} {
		b, err := os.ReadFile(filepath.Join(scriptsDir, f))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, "scripts", f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// devRepo builds a throwaway repository shaped like the dev repo: a
// CHANGELOG with a released section, the release commit, then n more
// commits. It returns the repo root and the short HEAD sha.
func devRepo(t *testing.T, n int, tag bool) (root, sha string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	root = t.TempDir()
	g := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=", "GIT_CONFIG_SYSTEM=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyDevVersionScripts(t, root)
	g("init", "-q")
	write("CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n- thing\n\n## [0.0.9] - 2026-01-01\n\n- old\n")
	g("add", "-A")
	g("commit", "-q", "-m", "work")
	write("CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [0.1.2] - 2026-10-11\n\n- thing\n\n## [0.0.9] - 2026-01-01\n\n- old\n")
	g("add", "-A")
	g("commit", "-q", "-m", "release")
	if tag {
		g("tag", "v0.1.2")
	}
	for i := 0; i < n; i++ {
		write("f.txt", strings.Repeat("x", i+1))
		g("add", "-A")
		g("commit", "-q", "-m", "more")
	}
	return root, g("rev-parse", "--short=7", "HEAD")
}

func derivers(t *testing.T) map[string]func(root string) string {
	t.Helper()
	d := map[string]func(string) string{}
	if bash, err := exec.LookPath("bash"); err == nil {
		d["sh"] = func(root string) string {
			out, err := exec.Command(bash, filepath.ToSlash(filepath.Join(root, "scripts", "dev-version.sh"))).CombinedOutput()
			if err != nil {
				t.Fatalf("dev-version.sh: %v\n%s", err, out)
			}
			return strings.TrimSpace(string(out))
		}
	}
	if runtime.GOOS == "windows" {
		if ps, err := exec.LookPath("powershell"); err == nil {
			d["ps1"] = func(root string) string {
				script := ". '" + filepath.Join(root, "scripts", "dev-version.ps1") + "'; Get-DevVersion"
				out, err := exec.Command(ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
				if err != nil {
					t.Fatalf("dev-version.ps1: %v\n%s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
		}
	}
	if len(d) == 0 {
		t.Skip("neither bash nor powershell available")
	}
	return d
}

// Both scripts must give the same answer in every state: exactly the
// release, N commits after it (with and without a tag), dirty, no history.
func TestDevVersionScripts(t *testing.T) {
	steps := []struct {
		name  string
		n     int
		tag   bool
		dirty bool
		want  func(sha string) string
	}{
		{"at the release commit", 0, false, false, func(string) string { return "v0.1.2" }},
		{"after the release, no tag", 3, false, false, func(sha string) string { return "v0.1.2+3." + sha }},
		{"after the release, tagged", 3, true, false, func(sha string) string { return "v0.1.2+3." + sha }},
		{"dirty after the release", 2, false, true, func(sha string) string { return "v0.1.2+2." + sha + ".dirty" }},
		{"dirty at the release", 0, false, true, func(sha string) string { return "v0.1.2+0." + sha + ".dirty" }},
	}
	for name, derive := range derivers(t) {
		for _, s := range steps {
			root, sha := devRepo(t, s.n, s.tag)
			if s.dirty {
				if err := os.WriteFile(filepath.Join(root, "CHANGELOG.md"), []byte("# Changelog\n\n## [0.1.2]\n\nedited\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := derive(root); got != s.want(sha) {
				t.Errorf("%s / %s: got %q, want %q", name, s.name, got, s.want(sha))
			}
		}
		// no git history at all: a plain "dev"
		bare := t.TempDir()
		copyDevVersionScripts(t, bare)
		if err := os.WriteFile(filepath.Join(bare, "CHANGELOG.md"), []byte("## [0.1.2] - x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := derive(bare); got != "dev" {
			t.Errorf("%s / no git: got %q, want dev", name, got)
		}
	}
}
