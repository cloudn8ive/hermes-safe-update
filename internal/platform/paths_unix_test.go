package platform

import (
	"reflect"
	"strings"
	"testing"
)

func fakeEnv(home string, vars map[string]string) Env {
	return Env{UserHome: home, Getenv: func(k string) string { return vars[k] }}
}

func TestUnixHermesHome(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
		err  bool
	}{
		{"default", fakeEnv("/home/u", nil), "/home/u/.hermes", false},
		{"suffix appended literally", fakeEnv("/home/u", map[string]string{"HERMES_DATA_DIR_SUFFIX": "-test"}), "/home/u/.hermes-test", false},
		{"HERMES_HOME wins", fakeEnv("/home/u", map[string]string{"HERMES_HOME": "/data/h/"}), "/data/h", false},
		{"profile dir names its parent", fakeEnv("/home/u", map[string]string{"HERMES_HOME": "/data/h/profiles/work"}), "/data/h", false},
		{"userData override without HERMES_HOME", fakeEnv("/home/u", map[string]string{"HERMES_DESKTOP_USER_DATA_DIR": "/tmp/ud"}), "/tmp/ud/hermes-home", false},
		{"HERMES_HOME beats userData override", fakeEnv("/home/u", map[string]string{"HERMES_HOME": "/h", "HERMES_DESKTOP_USER_DATA_DIR": "/tmp/ud"}), "/h", false},
		{"no home known", fakeEnv("", nil), "", true},
		{"nil Getenv", Env{UserHome: "/home/u"}, "/home/u/.hermes", false},
	}
	for _, goos := range []string{"darwin", "linux"} {
		p := newUnixPaths(goos, "amd64")
		for _, c := range cases {
			t.Run(goos+"/"+c.name, func(t *testing.T) {
				got, err := p.HermesHome(c.env)
				if (err != nil) != c.err || got != c.want {
					t.Fatalf("got (%q, %v), want (%q, err=%v)", got, err, c.want, c.err)
				}
			})
		}
	}
}

func TestUnixUserData(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  Env
		want string
		err  bool
	}{
		{"darwin default", "darwin", fakeEnv("/Users/u", nil), "/Users/u/Library/Application Support/Hermes", false},
		{"darwin suffix", "darwin", fakeEnv("/Users/u", map[string]string{"HERMES_DATA_DIR_SUFFIX": "-t"}), "/Users/u/Library/Application Support/Hermes-t", false},
		{"linux default", "linux", fakeEnv("/home/u", nil), "/home/u/.config/Hermes", false},
		{"linux XDG_CONFIG_HOME", "linux", fakeEnv("/home/u", map[string]string{"XDG_CONFIG_HOME": "/x/cfg"}), "/x/cfg/Hermes", false},
		{"linux relative XDG ignored", "linux", fakeEnv("/home/u", map[string]string{"XDG_CONFIG_HOME": "rel"}), "/home/u/.config/Hermes", false},
		{"linux XDG plus suffix", "linux", fakeEnv("/home/u", map[string]string{"XDG_CONFIG_HOME": "/x/cfg", "HERMES_DATA_DIR_SUFFIX": "-t"}), "/x/cfg/Hermes-t", false},
		{"override replaces everything", "linux", fakeEnv("/home/u", map[string]string{"HERMES_DESKTOP_USER_DATA_DIR": "/tmp/ud/", "HERMES_DATA_DIR_SUFFIX": "-t"}), "/tmp/ud", false},
		{"override needs no home", "darwin", fakeEnv("", map[string]string{"HERMES_DESKTOP_USER_DATA_DIR": "/tmp/ud"}), "/tmp/ud", false},
		{"no home", "linux", fakeEnv("", nil), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := newUnixPaths(c.goos, "amd64").UserData(c.env)
			if (err != nil) != c.err || got != c.want {
				t.Fatalf("got (%q, %v), want (%q, err=%v)", got, err, c.want, c.err)
			}
		})
	}
}

func TestUnixLauncherCandidates(t *testing.T) {
	p := newUnixPaths("linux", "amd64")
	got := p.LauncherCandidates(fakeEnv("/home/u", nil), "/home/u/.hermes", "/home/u/.hermes/hermes-agent")
	want := []string{
		"/home/u/.hermes/hermes-agent/.hermes/bin/hermes",
		"/home/u/.local/bin/hermes",
		"/home/u/.hermes/bin/hermes",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	// Missing pieces are skipped, not turned into relative paths.
	got = p.LauncherCandidates(fakeEnv("", nil), "/h", "")
	if !reflect.DeepEqual(got, []string{"/h/bin/hermes"}) {
		t.Errorf("sparse: %v", got)
	}
}

func TestUnixDesktopAppCandidates(t *testing.T) {
	const co = "/c"
	cases := []struct {
		goos, arch  string
		first, last string
		n           int
	}{
		{"darwin", "arm64", "/c/apps/desktop/release/mac-arm64/Hermes.app/Contents/MacOS/Hermes", "/c/apps/desktop/release/mac/Hermes.app/Contents/MacOS/Hermes", 3},
		{"darwin", "amd64", "/c/apps/desktop/release/mac/Hermes.app/Contents/MacOS/Hermes", "/c/apps/desktop/release/mac-universal/Hermes.app/Contents/MacOS/Hermes", 2},
		{"linux", "amd64", "/c/apps/desktop/release/linux-unpacked/hermes", "/c/apps/desktop/release/linux-unpacked/Hermes", 2},
		{"linux", "arm64", "/c/apps/desktop/release/linux-arm64-unpacked/hermes", "/c/apps/desktop/release/linux-unpacked/Hermes", 4},
	}
	for _, c := range cases {
		t.Run(c.goos+"/"+c.arch, func(t *testing.T) {
			got := newUnixPaths(c.goos, c.arch).DesktopAppCandidates(co)
			if len(got) != c.n || got[0] != c.first || got[len(got)-1] != c.last {
				t.Errorf("got %v", got)
			}
		})
	}
}

func TestUnixMarkersAreLowerCaseSlashes(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		ms := newUnixPaths(goos, "amd64").DesktopProcessMarkers()
		if len(ms) == 0 {
			t.Fatalf("%s: no markers", goos)
		}
		for _, m := range ms {
			if m != strings.ToLower(m) || strings.Contains(m, `\`) {
				t.Errorf("%s: marker %q", goos, m)
			}
		}
	}
	if got := newUnixPaths("darwin", "arm64").DesktopProcessMarkers(); !reflect.DeepEqual(got, []string{"/contents/macos/hermes"}) {
		t.Errorf("darwin markers %v", got)
	}
}

func TestUnixPackagedHints(t *testing.T) {
	mac := newUnixPaths("darwin", "arm64").PackagedInstallHints(fakeEnv("/Users/u", nil))
	var paths []string
	for _, h := range mac {
		paths = append(paths, h.Kind+":"+h.Path)
		if h.Use == "" {
			t.Errorf("hint %+v has no advice", h)
		}
	}
	for _, w := range []string{"macos-app:/Applications/Hermes.app", "macos-app:/Users/u/Applications/Hermes.app", "docker:/.dockerenv", "nix:/nix/store"} {
		if !contains(paths, w) {
			t.Errorf("darwin hints %v lack %s", paths, w)
		}
	}
	lin := newUnixPaths("linux", "amd64").PackagedInstallHints(fakeEnv("/home/u", nil))
	paths = nil
	for _, h := range lin {
		paths = append(paths, h.Kind+":"+h.Path)
	}
	for _, w := range []string{"docker:/.dockerenv", "nix:/nix/store"} {
		if !contains(paths, w) {
			t.Errorf("linux hints %v lack %s", paths, w)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "macos-app") {
			t.Errorf("linux must not hint a macOS app: %s", p)
		}
	}
}

func TestUnixElectronLeafAndGlobs(t *testing.T) {
	if got := newUnixPaths("darwin", "arm64").ElectronLeaf(); got != "Electron.app/Contents/MacOS/Electron" {
		t.Errorf("darwin leaf %q", got)
	}
	if got := newUnixPaths("linux", "arm64").ElectronLeaf(); got != "electron" {
		t.Errorf("linux leaf %q", got)
	}
	g := newUnixPaths("linux", "amd64").ManagedPythonGlobs("/h")
	if len(g) == 0 || !strings.HasPrefix(g[0], "/h/") {
		t.Errorf("python globs %v", g)
	}
	for _, p := range newUnixPaths("linux", "amd64").BundledGitGlobs("/h") {
		if !strings.HasPrefix(p, "/h/") {
			t.Errorf("git glob outside home: %s", p)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
