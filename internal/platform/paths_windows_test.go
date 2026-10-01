//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string, home string) Env {
	return Env{Getenv: func(k string) string { return m[k] }, UserHome: home}
}

func TestWinHermesHome(t *testing.T) {
	known := func(id string) (string, error) { return `C:\Known\` + id, nil }
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"localappdata", map[string]string{"LOCALAPPDATA": `C:\Users\you\AppData\Local`}, `C:\Users\you\AppData\Local\hermes`},
		{"suffix appended literally", map[string]string{"LOCALAPPDATA": `C:\L`, "HERMES_DATA_DIR_SUFFIX": "-dev"}, `C:\L\hermes-dev`},
		// Python ignores HERMES_HOME (children always see the default profile).
		{"HERMES_HOME ignored", map[string]string{"LOCALAPPDATA": `C:\L`, "HERMES_HOME": `C:\L\hermes\profiles\work`}, `C:\L\hermes`},
		{"empty LOCALAPPDATA uses known folder", map[string]string{}, `C:\Known\local\hermes`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := winPaths{goarch: "amd64", knownFolder: known}
			got, err := p.HermesHome(envOf(c.env, `C:\Users\you`))
			if err != nil || got != c.want {
				t.Errorf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestWinHermesHomeFallsBackToUserProfile(t *testing.T) {
	p := winPaths{knownFolder: func(string) (string, error) { return "", errNoKnown }}
	got, err := p.HermesHome(envOf(nil, `C:\Users\you`))
	if err != nil || got != `C:\Users\you\AppData\Local\hermes` {
		t.Errorf("got %q %v", got, err)
	}
	if _, err := p.HermesHome(envOf(nil, "")); err == nil {
		t.Error("no env, no known folder, no home: want an error")
	}
}

func TestWinUserData(t *testing.T) {
	p := winPaths{knownFolder: func(id string) (string, error) { return `C:\Known\` + id, nil }}
	got, _ := p.UserData(envOf(map[string]string{"APPDATA": `C:\Users\you\AppData\Roaming`}, ""))
	if got != `C:\Users\you\AppData\Roaming\Hermes` {
		t.Errorf("got %q", got)
	}
	got, _ = p.UserData(envOf(map[string]string{"APPDATA": `C:\R`, "HERMES_DATA_DIR_SUFFIX": "X"}, ""))
	if got != `C:\R\HermesX` {
		t.Errorf("suffix: %q", got)
	}
	got, _ = p.UserData(envOf(map[string]string{"HERMES_DESKTOP_USER_DATA_DIR": `D:\ud\`}, ""))
	if got != `D:\ud` {
		t.Errorf("override: %q", got)
	}
	got, _ = p.UserData(envOf(nil, ""))
	if got != `C:\Known\roaming\Hermes` {
		t.Errorf("known folder: %q", got)
	}
}

func TestWinCandidates(t *testing.T) {
	home, ck := `C:\h`, `C:\h\hermes-agent`
	p := winPaths{goarch: "arm64"}
	l := p.LauncherCandidates(Env{}, home, ck)
	if len(l) != 2 || l[0] != `C:\h\bin\hermes.exe` || l[1] != `C:\h\hermes-agent\.hermes\bin\hermes.exe` {
		t.Errorf("launchers %v", l)
	}
	d := p.DesktopAppCandidates(ck)
	if !strings.Contains(d[0], "win-arm64-unpacked") || len(d) != 3 {
		t.Errorf("arm64 desktop order %v", d)
	}
	d = winPaths{goarch: "amd64"}.DesktopAppCandidates(ck)
	if d[0] != filepath.Join(ck, `apps\desktop\release\win-unpacked\Hermes.exe`) {
		t.Errorf("amd64 desktop order %v", d)
	}
	if g := p.ManagedPythonGlobs(home); len(g) != 1 || g[0] != `C:\h\tools\python-*\python.exe` {
		t.Errorf("python %v", g)
	}
	if g := p.BundledGitGlobs(home); g[0] != `C:\h\tools\git-*\mingw64\bin\git.exe` {
		t.Errorf("git %v", g)
	}
	if p.ElectronLeaf() != "electron.exe" {
		t.Error("electron leaf")
	}
	m := p.DesktopProcessMarkers()
	for _, want := range []string{"win-unpacked", "win-arm64-unpacked"} {
		found := false
		for _, x := range m {
			found = found || x == want
		}
		if !found {
			t.Errorf("markers %v lack %s", m, want)
		}
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestWinPackagedHintsFindMSIX(t *testing.T) {
	lad := t.TempDir()
	p := winPaths{}
	if h := p.PackagedInstallHints(envOf(map[string]string{"LOCALAPPDATA": lad}, "")); len(h) != 0 {
		t.Errorf("no package: %v", h)
	}
	pkg := filepath.Join(lad, "Packages", "NousResearch.Hermes_abc123")
	mkdir(t, pkg)
	h := p.PackagedInstallHints(envOf(map[string]string{"LOCALAPPDATA": lad}, ""))
	if len(h) != 1 || h[0].Kind != "msix" || h[0].Path != pkg || !strings.Contains(h[0].Use, "App Installer") {
		t.Errorf("hints %v", h)
	}
}
