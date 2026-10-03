package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// synthHome builds a fake Hermes home in a temp dir: checkout with .git,
// launcher, bundled git. Nothing real is touched.
type synthHome struct {
	home, checkout, launcher, git string
	plat                          *platform.Platform
	run                           *execx.Fake
	out, errb                     bytes.Buffer
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newSynthHome(t *testing.T) *synthHome {
	t.Helper()
	s := &synthHome{home: filepath.Join(t.TempDir(), "hermes")}
	s.checkout = filepath.Join(s.home, "hermes-agent")
	s.launcher = filepath.Join(s.home, "bin", "hermes.exe")
	s.git = filepath.Join(s.home, "tools", "git-2", "git.exe")
	if err := os.MkdirAll(filepath.Join(s.checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, s.launcher)
	touch(t, s.git)
	s.plat = platform.NewFake()
	fp := s.plat.Paths.(*platform.FakePaths)
	fp.Home = s.home
	fp.Launchers = []string{s.launcher}
	fp.GitGlobs = []string{filepath.Join(s.home, "tools", "git-*", "git.exe")}
	fp.Markers = []string{"win-unpacked"}
	s.run = &execx.Fake{}
	s.run.On([]string{s.launcher, "--version"}, execx.Result{Output: "Hermes Agent v1.0\nInstall directory: " + s.checkout})
	s.run.OnPrefix([]string{s.git, "-c", "gc.auto=0", "--no-optional-locks", "-C", s.checkout, "rev-parse", "--short"}, execx.Result{Output: "1111111\n"})
	s.run.OnPrefix([]string{s.git, "-c", "gc.auto=0", "--no-optional-locks", "-C", s.checkout, "rev-parse", "HEAD"}, execx.Result{Output: strings.Repeat("1", 40) + "\n"})
	s.run.OnPrefix([]string{s.git, "-c", "gc.auto=0", "--no-optional-locks", "-C", s.checkout, "status"}, execx.Result{Output: ""})
	s.run.OnPrefix([]string{s.git, "-c", "gc.auto=0", "--no-optional-locks", "-C", s.checkout, "diff", "--quiet"}, execx.Result{Code: 0})
	return s
}

func (s *synthHome) cli(args ...string) int {
	env := &environment{
		platform: s.plat,
		getenv:   func(string) string { return "" },
		stdout:   &s.out,
		stderr:   &s.errb,
		runner:   s.run,
	}
	return run(context.Background(), args, env)
}

func TestCheckRunsTheFlowAndChangesNothing(t *testing.T) {
	s := newSynthHome(t)
	s.run.On([]string{s.launcher, "update", "--check"}, execx.Result{Output: "2 commits behind origin/main"})
	s.plat.Procs.(*platform.FakeProcs).Add(platform.Process{PID: 77, Name: "Hermes.exe",
		Cmdline: filepath.Join(s.checkout, "apps", "desktop", "release", "win-unpacked", "Hermes.exe")})
	start := time.Now()
	code := s.cli("--plain", "check")
	t.Logf("took %v\n%s", time.Since(start), s.errb.String())
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, s.errb.String())
	}
	all := s.errb.String()
	for _, want := range []string{"Update available. Hermes was not touched.", "2 new commit(s)", "desktop"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}
	for _, c := range s.run.Calls() {
		if len(c.Argv) > 2 && c.Argv[1] == "update" && c.Argv[2] == "--yes" {
			t.Errorf("check ran the update: %v", c.Argv)
		}
	}
	if calls := s.plat.App.(*platform.FakeApp).Calls(); len(calls) != 0 {
		t.Errorf("check touched the app: %v", calls)
	}
	if _, err := os.Stat(filepath.Join(s.home, "logs", "safe-update-timings.json")); err != nil {
		t.Errorf("timings not saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.home, ".hermes-update-in-progress")); err == nil {
		t.Error("check wrote the update marker")
	}
}

func TestUpToDateExitsZero(t *testing.T) {
	s := newSynthHome(t)
	s.run.On([]string{s.launcher, "update", "--check"}, execx.Result{Output: "Already up to date"})
	if code := s.cli("--plain", "--unattended"); code != 0 {
		t.Fatalf("exit %d\n%s", code, s.errb.String())
	}
	if !strings.Contains(s.errb.String(), "Hermes is up to date") {
		t.Errorf("stderr:\n%s", s.errb.String())
	}
}

func TestNoCheckoutStepsAside(t *testing.T) {
	s := newSynthHome(t)
	if err := os.RemoveAll(filepath.Join(s.checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	if code := s.cli("--plain", "check"); code != apperr.NotCheckout {
		t.Fatalf("exit %d\n%s", code, s.errb.String())
	}
}

func TestMissingLauncherExits2(t *testing.T) {
	s := newSynthHome(t)
	if err := os.Remove(s.launcher); err != nil {
		t.Fatal(err)
	}
	if code := s.cli("--plain", "check"); code != apperr.LauncherMissing {
		t.Fatalf("exit %d\n%s", code, s.errb.String())
	}
}

func TestAppIDChildModeNeedsAWindow(t *testing.T) {
	s := newSynthHome(t)
	if code := s.cli("--appid", "0"); code != 2 {
		t.Errorf("--appid 0: exit %d", code)
	}
	if strings.Contains(s.errb.String(), "untested") {
		t.Error("the identity child must never prompt")
	}
}

func TestMirrorStatusPrintsToStdout(t *testing.T) {
	s := newSynthHome(t)
	if code := s.cli("mirror", "status"); code != 0 {
		t.Fatalf("exit %d\n%s", code, s.errb.String())
	}
	if !strings.Contains(s.out.String(), "mirror:") {
		t.Errorf("stdout %q", s.out.String())
	}
}

func TestMirrorRefreshWithoutMirrorIsSilentZero(t *testing.T) {
	s := newSynthHome(t)
	if code := s.cli("mirror", "refresh"); code != 0 || s.out.Len() != 0 {
		t.Errorf("exit %d stdout %q", code, s.out.String())
	}
}

func TestMachineStateComesFromTheConfiguredHome(t *testing.T) {
	s := newSynthHome(t)
	// the platform default home holds a machine state that must NOT be used
	def := t.TempDir()
	if err := os.WriteFile(filepath.Join(def, "safe-update.json"), []byte(`{"mirror":{"path":"Z:/elsewhere"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.plat.Paths.(*platform.FakePaths).Home = def
	cfgFile := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(cfgFile, []byte("paths:\n  hermes_home: \""+filepath.ToSlash(s.home)+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := parseArgs([]string{"--config", cfgFile, "check"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(o, &environment{platform: s.plat, getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mirror.Path != "" || strings.Contains(cfg.Sources.JSON, def) {
		t.Errorf("machine state read from the default home: mirror.path=%q json=%q", cfg.Mirror.Path, cfg.Sources.JSON)
	}
}

func TestListBackupsEmpty(t *testing.T) {
	s := newSynthHome(t)
	if code := s.cli("list-backups"); code != 0 {
		t.Fatalf("exit %d\n%s", code, s.errb.String())
	}
	if !strings.Contains(s.out.String(), "no settings backups") {
		t.Errorf("stdout %q", s.out.String())
	}
}

func TestSettingsCommandsRefuseWhileDesktopRuns(t *testing.T) {
	for _, cmd := range []string{"migrate-settings", "revert-settings"} {
		s := newSynthHome(t)
		s.plat.Procs.(*platform.FakeProcs).Add(platform.Process{PID: 77, Name: "Hermes.exe",
			Cmdline: filepath.Join(s.checkout, "apps", "desktop", "release", "win-unpacked", "Hermes.exe")})
		if code := s.cli(cmd); code != apperr.HermesRunning {
			t.Errorf("%s: exit %d, want %d\n%s", cmd, code, apperr.HermesRunning, s.errb.String())
		}
	}
}
