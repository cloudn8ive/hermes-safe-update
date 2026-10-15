package hermes

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// directFixture is a checkout whose only launcher candidate does not exist,
// with a managed Python and Hermes' _launchers.py in place.
func directFixture(t *testing.T) (*fixture, string, string) {
	t.Helper()
	f := newFixture(t)
	f.makeCheckout(t)
	launcher := filepath.Join(f.home, "bin", "hermes.exe") // never created
	f.paths.Launchers = []string{launcher}
	py := touch(t, filepath.Join(f.home, "tools", "python-3.14.7", "python.exe"), "")
	f.paths.PythonGlobs = []string{filepath.Join(f.home, "tools", "python-*", "python.exe")}
	touch(t, filepath.Join(f.checkout, "hermes_cli", "_launchers.py"), "")
	rh := runtimeHome{home: f.home, checkout: f.checkout, python: py}
	f.run.OnPrefix([]string{py}, execx.Result{Output: rh.fixture(t)})
	return f, launcher, py
}

func TestLocateWithoutLauncherStillFailsByDefault(t *testing.T) {
	f, _, _ := directFixture(t)
	if _, err := f.loc.Locate(context.Background()); !errors.Is(err, apperr.ErrLauncherMissing) {
		t.Fatalf("err = %v, want ErrLauncherMissing (commands that run the launcher directly must keep exiting 2)", err)
	}
	if len(f.run.Calls()) != 0 {
		t.Errorf("ran %v without AllowDirect", f.run.Calls())
	}
}

func TestLocateDirectWhenNoLauncher(t *testing.T) {
	f, launcher, py := directFixture(t)
	f.loc.AllowDirect = true
	in, err := f.loc.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if in.Launcher != launcher {
		t.Errorf("launcher name = %q, want the first candidate %q (callers keep passing it; the route replaces it)", in.Launcher, launcher)
	}
	if len(in.LauncherPrefix) != 4 || in.LauncherPrefix[0] != py || in.LauncherPrefix[1] != "-I" || in.LauncherPrefix[2] != "-c" {
		t.Errorf("prefix = %q", in.LauncherPrefix)
	}
	if in.Python != py {
		t.Errorf("python = %q", in.Python)
	}
}

func TestLocateDirectNeedsAManagedPythonAndTheScript(t *testing.T) {
	f, _, py := directFixture(t)
	f.loc.AllowDirect = true
	f.run = &execx.Fake{} // Hermes answers nothing sensible
	f.loc.Run = f.run
	f.run.OnPrefix([]string{py}, execx.Result{Output: "no command"})
	if _, err := f.loc.Locate(context.Background()); !errors.Is(err, apperr.ErrLauncherMissing) || !strings.Contains(err.Error(), "managed Python") {
		t.Errorf("a refused answer: err = %v", err)
	}
	f.paths.PythonGlobs = nil // no managed Python at all
	if _, err := f.loc.Locate(context.Background()); !errors.Is(err, apperr.ErrLauncherMissing) {
		t.Errorf("no Python: err = %v", err)
	}
}

func TestLocatePrefersARealLauncherOverTheDirectRoute(t *testing.T) {
	f, _, _ := directFixture(t)
	f.loc.AllowDirect = true
	real := touch(t, filepath.Join(f.checkout, ".hermes", "bin", "hermes.exe"), "")
	f.paths.Launchers = append(f.paths.Launchers, real)
	f.run.On([]string{real, "--version"}, versionOut(f.checkout))
	in, err := f.loc.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if in.Launcher != real || in.LauncherPrefix != nil {
		t.Errorf("launcher = %q prefix = %q", in.Launcher, in.LauncherPrefix)
	}
}

func TestResolveLauncherFollowsAReplacedLauncher(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	gone := filepath.Join(f.home, "bin", "hermes.exe") // the update removed it
	moved := touch(t, filepath.Join(f.checkout, ".hermes", "bin", "hermes.exe"), "")
	f.paths.Launchers = []string{gone, moved}
	f.run.On([]string{moved, "--version"}, versionOut(f.checkout))
	launcher, prefix, err := f.loc.ResolveLauncher(context.Background(), Install{Home: f.home, Checkout: f.checkout})
	if err != nil || launcher != moved || prefix != nil {
		t.Errorf("got %q %q %v", launcher, prefix, err)
	}
	if !reflect.DeepEqual(f.run.Calls()[0].Argv, []string{moved, "--version"}) {
		t.Errorf("probed %v", f.run.Calls())
	}
}
