package hermes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func touch(t *testing.T, p string, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func versionOut(dir string) execx.Result {
	return execx.Result{Output: "Hermes Agent v0.21.5+5279.g4e74031 (2026.9.24) · upstream 234badf4\r\nInstall directory: " + dir + "\r\nInstall method: git\r\nPython: 3.14.7\r\n"}
}

type fixture struct {
	home, checkout string
	paths          *platform.FakePaths
	run            *execx.Fake
	loc            *RealLocator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	f := &fixture{home: home, checkout: filepath.Join(home, "hermes-agent"), paths: &platform.FakePaths{Home: home}, run: &execx.Fake{}}
	f.loc = &RealLocator{
		Paths:    f.paths,
		Run:      f.run,
		Env:      platform.Env{Getenv: func(string) string { return "" }, UserHome: home},
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
	}
	return f
}

func (f *fixture) makeCheckout(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(f.checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLocateCheckoutFull(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	launcher := touch(t, filepath.Join(f.home, "bin", "hermes.exe"), "")
	f.paths.Launchers = []string{launcher}
	f.run.On([]string{launcher, "--version"}, versionOut(f.checkout))
	touch(t, filepath.Join(f.home, "tools", "python-3.13.1", "python.exe"), "")
	py := touch(t, filepath.Join(f.home, "tools", "python-3.14.7", "python.exe"), "")
	f.paths.PythonGlobs = []string{filepath.Join(f.home, "tools", "python-*", "python.exe")}
	git := touch(t, filepath.Join(f.home, "tools", "git-2.53.0", "cmd", "git.exe"), "")
	f.paths.GitGlobs = []string{filepath.Join(f.home, "tools", "git-*", "cmd", "git.exe")}
	f.loc.LookPath = func(string) (string, error) { return "/usr/bin/git", nil }
	nm := filepath.Join(f.checkout, "apps", "desktop", "node_modules", "electron")
	touch(t, filepath.Join(nm, "path.txt"), "electron.exe\n")
	el := touch(t, filepath.Join(nm, "dist", "electron.exe"), "")
	desk := touch(t, filepath.Join(f.checkout, "apps", "desktop", "release", "win-unpacked", "Hermes.exe"), "")
	f.paths.DesktopApps = []string{filepath.Join(f.home, "nope.exe"), desk}
	f.paths.UserDataDir = filepath.Join(f.home, "userdata")

	got, err := f.loc.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Install{Kind: KindCheckout, Home: f.home, Checkout: f.checkout, Launcher: launcher, Python: py, Git: git, Electron: el, Desktop: desk, UserData: f.paths.UserDataDir}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestLocateGitFallsBackToPATH(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	l := touch(t, filepath.Join(f.home, "bin", "hermes"), "")
	f.paths.Launchers = []string{l}
	f.run.On([]string{l, "--version"}, versionOut(f.checkout))
	f.loc.LookPath = func(string) (string, error) { return "/usr/bin/git", nil }
	got, err := f.loc.Locate(context.Background())
	if err != nil || got.Git != "/usr/bin/git" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLocateOptionalPiecesMissingAreEmpty(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	l := touch(t, filepath.Join(f.home, "bin", "hermes"), "")
	f.paths.Launchers = []string{l}
	f.run.On([]string{l, "--version"}, versionOut(f.checkout))
	got, err := f.loc.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Python != "" || got.Git != "" || got.Electron != "" || got.Desktop != "" {
		t.Errorf("want empty optional paths, got %+v", got)
	}
}

func TestLocateElectronLeafWithoutPathTxt(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	l := touch(t, filepath.Join(f.home, "bin", "hermes"), "")
	f.paths.Launchers = []string{l}
	f.run.On([]string{l, "--version"}, versionOut(f.checkout))
	f.paths.Electron = "electron"
	el := touch(t, filepath.Join(f.checkout, "apps", "desktop", "node_modules", "electron", "dist", "electron"), "")
	got, _ := f.loc.Locate(context.Background())
	if got.Electron != el {
		t.Errorf("Electron = %q want %q", got.Electron, el)
	}
}

func TestLauncherSelection(t *testing.T) {
	cases := []struct {
		name     string
		versions map[int]execx.Result // by candidate index; missing = not runnable
		want     int                  // index, -1 = ErrLauncherMissing
	}{
		{"first matches", map[int]execx.Result{0: versionOut("CK"), 1: versionOut("CK")}, 0},
		{"first is another install", map[int]execx.Result{0: versionOut("/other/hermes-agent"), 1: versionOut("CK")}, 1},
		{"path compare ignores case", map[int]execx.Result{0: versionOut("CKUP")}, 0},
		{"no Install directory line: first runnable is used", map[int]execx.Result{0: {Output: "Hermes Agent v1\n"}, 1: versionOut("CK")}, 0},
		{"version not runnable: next", map[int]execx.Result{0: {Code: 127, Output: "boom"}, 1: versionOut("CK")}, 1},
		{"all mismatch", map[int]execx.Result{0: versionOut("/a"), 1: versionOut("/b")}, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.makeCheckout(t)
			var cands []string
			for _, n := range []string{"l0", "l1"} {
				cands = append(cands, touch(t, filepath.Join(f.home, n, "hermes"), ""))
			}
			f.paths.Launchers = append([]string{filepath.Join(f.home, "missing", "hermes")}, cands...)
			for i, r := range c.versions {
				r.Output = strings.ReplaceAll(r.Output, "CKUP", strings.ToUpper(f.checkout))
				r.Output = strings.ReplaceAll(r.Output, "CK", f.checkout)
				f.run.On([]string{cands[i], "--version"}, r)
			}
			got, err := f.loc.Locate(context.Background())
			if c.want < 0 {
				if !errors.Is(err, apperr.ErrLauncherMissing) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got.Launcher != cands[c.want] {
				t.Fatalf("got %q, %v; want %q", got.Launcher, err, cands[c.want])
			}
		})
	}
}

func TestLocateNoLauncherCandidatesIsMissing(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	_, err := f.loc.Locate(context.Background())
	if !errors.Is(err, apperr.ErrLauncherMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestLocateConfigOverrides(t *testing.T) {
	f := newFixture(t)
	other := t.TempDir()
	ck := filepath.Join(other, "src")
	if err := os.MkdirAll(filepath.Join(ck, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := touch(t, filepath.Join(other, "bin", "hermes"), "")
	f.paths.Launchers = []string{l}
	f.run.On([]string{l, "--version"}, versionOut(ck))
	f.loc.Cfg = config.Paths{HermesHome: "$MYHOME", Checkout: ck, UserData: filepath.Join(other, "ud")}
	f.loc.Env.Getenv = func(k string) string {
		if k == "MYHOME" {
			return other
		}
		return ""
	}
	got, err := f.loc.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Home != other || got.Checkout != ck || got.UserData != filepath.Join(other, "ud") {
		t.Errorf("got %+v", got)
	}
}

func TestInstallKinds(t *testing.T) {
	cases := []struct {
		name     string
		checkout bool
		hints    []string // kinds to create on disk; "!kind" = hint listed but absent
		want     InstallKind
		advice   string
	}{
		{"checkout", true, nil, KindCheckout, ""},
		{"checkout wins over a mac app", true, []string{"macos-app"}, KindCheckout, ""},
		{"mac app", false, []string{"macos-app"}, KindMacApp, "Check for Updates"},
		{"msix", false, []string{"msix"}, KindMSIX, "App Installer"},
		{"docker", false, []string{"docker"}, KindDocker, "image"},
		{"nix", false, []string{"nix"}, KindNix, "nix"},
		{"appimage", false, []string{"appimage"}, KindAppImage, "AppImage"},
		{"first existing hint wins", false, []string{"!msix", "nix"}, KindNix, "nix"},
		{"unknown", false, []string{"!macos-app"}, KindUnknown, ""},
		{"unknown with no hints", false, nil, KindUnknown, ""},
	}
	uses := map[string]string{
		"macos-app": "use Check for Updates in the app", "msix": "updates come from the App Installer feed",
		"docker": "pull a new image", "nix": "update through nix", "appimage": "replace the AppImage",
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.checkout {
				f.makeCheckout(t)
				l := touch(t, filepath.Join(f.home, "bin", "hermes"), "")
				f.paths.Launchers = []string{l}
				f.run.On([]string{l, "--version"}, versionOut(f.checkout))
			}
			for _, h := range c.hints {
				kind := strings.TrimPrefix(h, "!")
				p := filepath.Join(f.home, "hints", kind)
				if h == kind {
					touch(t, p, "")
				}
				f.paths.Packaged = append(f.paths.Packaged, platform.PackagedHint{Kind: kind, Path: p, Use: uses[kind]})
			}
			got, err := f.loc.Locate(context.Background())
			if err != nil {
				t.Fatalf("a packaged install must not fail Locate: %v", err)
			}
			if got.Kind != c.want {
				t.Errorf("kind = %q want %q", got.Kind, c.want)
			}
			if !strings.Contains(strings.ToLower(got.StepAside), strings.ToLower(c.advice)) {
				t.Errorf("advice = %q, want it to contain %q", got.StepAside, c.advice)
			}
			if c.want != KindCheckout && got.Launcher != "" {
				t.Errorf("non-checkout installs get no launcher: %q", got.Launcher)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name, in                  string
		version, build, dir, meth string
	}{
		{"windows crlf", "Hermes Agent v0.21.5+5279.g4e74031 (2026.9.24) · upstream 234badf4\r\nInstall directory: C:\\x\\hermes-agent\r\nInstall method: git\r\n", "v0.21.5+5279.g4e74031", "2026.9.24", `C:\x\hermes-agent`, "git"},
		{"no build", "Hermes Agent v1.0.0\nInstall directory: /h/hermes-agent\n", "v1.0.0", "", "/h/hermes-agent", ""},
		{"empty", "", "", "", "", ""},
		{"garbage", "command not found", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ParseVersion(c.in)
			if v.Version != c.version || v.Build != c.build || v.InstallDir != c.dir || v.Method != c.meth {
				t.Errorf("got %+v", v)
			}
		})
	}
}

func TestCurrentVersionReadsTheLauncher(t *testing.T) {
	r := &execx.Fake{}
	r.On([]string{"hermes.exe", "--version"}, versionOut(`C:\h\hermes-agent`))
	if got := CurrentVersion(context.Background(), r, "hermes.exe"); got != "v0.21.5+5279.g4e74031" {
		t.Errorf("version = %q", got)
	}
	r.On([]string{"broken.exe", "--version"}, execx.Result{Code: 1, Output: "boom"})
	for _, l := range []string{"broken.exe", "missing.exe", ""} {
		if got := CurrentVersion(context.Background(), r, l); got != "" {
			t.Errorf("launcher %q: version = %q, want empty (unknown is not an error)", l, got)
		}
	}
}
