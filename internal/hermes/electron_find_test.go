package hermes

import (
	"path/filepath"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// electronPkg is the electron package folder under a node_modules parent
// (apps/desktop for older Hermes, the checkout root since Hermes pinned
// Electron 44.9.3 and npm hoisted it).
func electronPkg(checkout string, desktop bool) string {
	if desktop {
		return filepath.Join(checkout, "apps", "desktop", "node_modules", "electron")
	}
	return filepath.Join(checkout, "node_modules", "electron")
}

func TestElectronPackageDirsOrder(t *testing.T) {
	co := filepath.Join("x", "hermes-agent")
	got := ElectronPackageDirs(co)
	if len(got) != 2 || got[0] != electronPkg(co, true) || got[1] != electronPkg(co, false) {
		t.Errorf("dirs = %v, want desktop-local first, then the checkout root", got)
	}
}

func TestFindElectronLayouts(t *testing.T) {
	fp := &platform.FakePaths{Electron: "electron.exe"}
	cases := []struct {
		name         string
		desktop, top bool   // the electron.exe exists there
		want         string // "desktop", "root" or ""
	}{
		{"older Hermes: desktop-local only", true, false, "desktop"},
		{"new layout: hoisted to the root only", false, true, "root"},
		{"both: desktop-local wins", true, true, "desktop"},
		{"neither", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			co := filepath.Join(t.TempDir(), "hermes-agent")
			var d, r string
			if tc.desktop {
				d = touch(t, filepath.Join(electronPkg(co, true), "dist", "electron.exe"), "")
			}
			if tc.top {
				r = touch(t, filepath.Join(electronPkg(co, false), "dist", "electron.exe"), "")
			}
			want := map[string]string{"desktop": d, "root": r, "": ""}[tc.want]
			if got := FindElectron(fp, co); got != want {
				t.Errorf("FindElectron = %q, want %q", got, want)
			}
		})
	}
}

func TestFindElectronPathTxtInTheFolderThatHoldsThePackage(t *testing.T) {
	fp := &platform.FakePaths{Electron: "electron.exe"}
	co := filepath.Join(t.TempDir(), "hermes-agent")
	touch(t, filepath.Join(electronPkg(co, false), "path.txt"), "Electron.app/Contents/MacOS/Electron\n")
	want := touch(t, filepath.Join(electronPkg(co, false), "dist", "Electron.app", "Contents", "MacOS", "Electron"), "")
	if got := FindElectron(fp, co); got != want {
		t.Errorf("FindElectron = %q, want %q (path.txt of the root package)", got, want)
	}
}

func TestFindElectronDesktopFolderWithoutBinaryDoesNotHideTheRootOne(t *testing.T) {
	fp := &platform.FakePaths{Electron: "electron.exe"}
	co := filepath.Join(t.TempDir(), "hermes-agent")
	// a leftover package folder: path.txt, no dist
	touch(t, filepath.Join(electronPkg(co, true), "path.txt"), "electron.exe")
	want := touch(t, filepath.Join(electronPkg(co, false), "dist", "electron.exe"), "")
	if got := FindElectron(fp, co); got != want {
		t.Errorf("FindElectron = %q, want %q", got, want)
	}
}

func TestFindElectronEmptyInputs(t *testing.T) {
	if FindElectron(nil, "x") != "" || FindElectron(&platform.FakePaths{Electron: "e"}, "") != "" {
		t.Error("nil paths or empty checkout must find nothing")
	}
}

func TestLocateFindsHoistedElectron(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	l := touch(t, filepath.Join(f.home, "bin", "hermes"), "")
	f.paths.Launchers = []string{l}
	f.run.On([]string{l, "--version"}, versionOut(f.checkout))
	f.paths.Electron = "electron.exe"
	el := touch(t, filepath.Join(electronPkg(f.checkout, false), "dist", "electron.exe"), "")
	got, err := f.loc.Locate(t.Context())
	if err != nil || got.Electron != el {
		t.Errorf("Electron = %q (%v), want %q", got.Electron, err, el)
	}
}

func TestFindElectronEmptyCheckoutDoesNotLookRelativeToTheWorkingFolder(t *testing.T) {
	fp := &platform.FakePaths{Electron: "electron.exe"}
	wd := t.TempDir()
	touch(t, filepath.Join(electronPkg(wd, true), "dist", "electron.exe"), "")
	touch(t, filepath.Join(electronPkg(wd, false), "dist", "electron.exe"), "")
	t.Chdir(wd)
	if got := FindElectron(fp, ""); got != "" {
		t.Errorf("FindElectron with no checkout = %q, want nothing", got)
	}
}

// An empty path.txt says nothing: the platform's usual leaf is used.
func TestFindElectronEmptyPathTxtKeepsThePlatformLeaf(t *testing.T) {
	fp := &platform.FakePaths{Electron: "electron.exe"}
	co := filepath.Join(t.TempDir(), "hermes-agent")
	touch(t, filepath.Join(electronPkg(co, true), "path.txt"), " \n")
	want := touch(t, filepath.Join(electronPkg(co, true), "dist", "electron.exe"), "")
	if got := FindElectron(fp, co); got != want {
		t.Errorf("FindElectron = %q, want %q", got, want)
	}
}

// A platform with no known leaf finds nothing from the folder itself, but a
// package's own path.txt still names the file, in whichever folder it is.
func TestFindElectronWithoutAPlatformLeaf(t *testing.T) {
	fp := &platform.FakePaths{}
	t.Run("no path.txt anywhere", func(t *testing.T) {
		co := filepath.Join(t.TempDir(), "hermes-agent")
		touch(t, filepath.Join(electronPkg(co, true), "dist", "keep"), "")
		if got := FindElectron(fp, co); got != "" {
			t.Errorf("FindElectron = %q, want nothing", got)
		}
	})
	t.Run("path.txt only in the root package", func(t *testing.T) {
		co := filepath.Join(t.TempDir(), "hermes-agent")
		touch(t, filepath.Join(electronPkg(co, false), "path.txt"), "electron.exe")
		want := touch(t, filepath.Join(electronPkg(co, false), "dist", "electron.exe"), "")
		if got := FindElectron(fp, co); got != want {
			t.Errorf("FindElectron = %q, want %q", got, want)
		}
	})
}
