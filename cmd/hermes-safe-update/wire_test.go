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
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
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
		ver:      testToolVersion,
	}
	return run(context.Background(), args, env)
}

func TestCheckRunsTheFlowAndChangesNothing(t *testing.T) {
	s := newSynthHome(t)
	s.run.On([]string{s.launcher, "update", "--check", "--branch", "main"}, execx.Result{Output: "2 commits behind origin/main"})
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
	// the tool's own version: first line on screen, and in the log
	if first := strings.SplitN(all, "\n", 2)[0]; first != "Hermes Update Check · "+testToolVersion {
		t.Errorf("first line = %q", first)
	}
	if lg, err := os.ReadFile(filepath.Join(s.home, "logs", "safe-update.log")); err != nil || !strings.Contains(string(lg), "version="+testToolVersion) {
		t.Errorf("log start line lacks version=%s (err %v):\n%s", testToolVersion, err, lg)
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
	s.run.On([]string{s.launcher, "update", "--check", "--branch", "main"}, execx.Result{Output: "Already up to date"})
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

// The cron shim runs `mirror refresh` with no flags. On macOS/Linux the
// untested-platform gate used to run first and exit 3 on every scheduled run.
func TestScheduledMirrorRefreshSkipsTheUntestedGate(t *testing.T) {
	for _, args := range [][]string{{"mirror", "refresh"}, {"mirror", "status"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newSynthHome(t)
			s.plat.OS, s.plat.Tested = "linux", false
			if code := s.cli(args...); code != 0 {
				t.Fatalf("exit %d (want 0)\n%s", code, s.errb.String())
			}
			if strings.Contains(s.errb.String(), "untested") {
				t.Errorf("gate fired: %s", s.errb.String())
			}
		})
	}
}

// Everything that closes, updates or writes stays behind the gate.
func TestUntestedGateStillCoversCommandsThatChangeThings(t *testing.T) {
	for _, args := range [][]string{{"run"}, {"mirror", "setup"}, {"migrate-settings"}, {"backup-settings"}, {"revert-settings"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newSynthHome(t)
			s.plat.OS, s.plat.Tested = "linux", false
			code := s.cli(append([]string{"--unattended"}, args...)...)
			if code != apperr.Cancelled || !strings.Contains(s.errb.String(), "untested") {
				t.Errorf("exit %d, stderr %q (want %d + untested notice)", code, s.errb.String(), apperr.Cancelled)
			}
			if c := s.run.Calls(); len(c) != 0 {
				t.Errorf("ran %v before the gate", c)
			}
		})
	}
}

// The look-only commands never ask: the README calls --check safe anywhere.
func TestUntestedGateSkipsTheLookOnlyCommands(t *testing.T) {
	for _, args := range [][]string{{"check"}, {"--check"}, {"list-backups"}, {"migrate-settings", "--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newSynthHome(t)
			s.plat.OS, s.plat.Tested = "linux", false
			s.cli(append([]string{"--plain", "--unattended"}, args...)...)
			if strings.Contains(s.errb.String(), "untested") {
				t.Errorf("gate fired: %s", s.errb.String())
			}
		})
	}
}

func savedAcceptance(t *testing.T, s *synthHome) map[string]string {
	t.Helper()
	st, err := config.LoadMachineState(filepath.Join(s.home, "safe-update.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st.AcceptedUntested
}

// "One-time" is true: --accept-untested is remembered in safe-update.json,
// and a later unattended run (a scheduler) does not stop at the question.
func TestAcceptUntestedFlagIsRemembered(t *testing.T) {
	s := newSynthHome(t)
	s.plat.OS, s.plat.Tested = "linux", false
	if code := s.cli("--unattended", "run"); code != apperr.Cancelled {
		t.Fatalf("before accepting: exit %d, want %d", code, apperr.Cancelled)
	}
	if acc := savedAcceptance(t, s); len(acc) != 0 {
		t.Fatalf("a refused run saved %v", acc)
	}
	s.errb.Reset()
	s.cli("--plain", "--unattended", "--accept-untested", "run")
	if acc := savedAcceptance(t, s); acc["linux"] == "" {
		t.Fatalf("acceptance not saved: %v", acc)
	}
	s.errb.Reset()
	s.cli("--plain", "--unattended", "run")
	if strings.Contains(s.errb.String(), "untested") {
		t.Errorf("asked again after accepting: %s", s.errb.String())
	}
}

func TestAnsweringYIsRemembered(t *testing.T) {
	s := newSynthHome(t)
	s.plat.OS, s.plat.Tested = "linux", false
	s.plat.Console = &platform.FakeConsole{Keys: []platform.Key{{Rune: 'y'}}}
	s.cli("--plain", "run")
	if acc := savedAcceptance(t, s); acc["linux"] == "" {
		t.Fatalf("Y not saved: %v", acc)
	}
	s.errb.Reset()
	s.plat.Console = &platform.FakeConsole{} // no key available: a second question would fail
	s.cli("--plain", "run")
	if strings.Contains(s.errb.String(), "untested") {
		t.Errorf("asked again after Y: %s", s.errb.String())
	}
}

func TestAnsweringNIsNotRemembered(t *testing.T) {
	s := newSynthHome(t)
	s.plat.OS, s.plat.Tested = "linux", false
	s.plat.Console = &platform.FakeConsole{Keys: []platform.Key{{Rune: 'n'}}}
	if code := s.cli("--plain", "run"); code != apperr.Cancelled {
		t.Fatalf("exit %d", code)
	}
	if acc := savedAcceptance(t, s); len(acc) != 0 {
		t.Errorf("N saved %v", acc)
	}
}

// The acceptance belongs to the OS it was given for, and saving it keeps
// the rest of safe-update.json.
func TestAcceptanceIsPerOSAndKeepsOtherState(t *testing.T) {
	s := newSynthHome(t)
	j := filepath.Join(s.home, "safe-update.json")
	if err := os.WriteFile(j, []byte(`{"mirror":{"path":"/m","set_up":"x"},"accepted_untested":{"darwin":"2026-01-01T00:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.plat.OS, s.plat.Tested = "linux", false
	if code := s.cli("--unattended", "run"); code != apperr.Cancelled {
		t.Fatalf("a macOS acceptance must not cover Linux: exit %d", code)
	}
	s.cli("--plain", "--unattended", "--accept-untested", "run")
	st, err := config.LoadMachineState(j)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mirror == nil || st.Mirror.Path != "/m" || !st.UntestedAccepted("darwin") || !st.UntestedAccepted("linux") {
		t.Errorf("state after save: %+v", st)
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
