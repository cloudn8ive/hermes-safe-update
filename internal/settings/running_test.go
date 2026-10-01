package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func TestUserDataArg(t *testing.T) {
	cases := map[string]string{
		`"C:\a\Hermes.exe" --type=gpu-process --user-data-dir="C:\Users\you\AppData\Roaming\Hermes" --x`: `C:\Users\you\AppData\Roaming\Hermes`,
		`hermes --type=renderer --user-data-dir=/home/you/.config/Hermes --y`:                            `/home/you/.config/Hermes`,
		`"C:\a\Hermes.exe"`: "",
		// a bare value followed by the closing quote of an outer argument
		`x.exe -Command "Start-Sleep 9 # --user-data-dir=C:/sbx/ud3"`: `C:/sbx/ud3`,
	}
	for in, want := range cases {
		if got := userDataArg(in); got != want {
			t.Errorf("userDataArg(%q) = %q, want %q", in, got, want)
		}
	}
}

const liveUD = `C:\Users\you\AppData\Roaming\Hermes`

// exeCmd is the desktop main command line of the env under test (newEnv sets
// it): only a desktop under this install's checkout counts as "the app".
var exeCmd string

func desktopTree(mainPID int, mainCmd string, childUD ...string) []platform.Process {
	t0 := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	ps := []platform.Process{{PID: mainPID, Name: "Hermes.exe", Cmdline: mainCmd, Created: t0}}
	for i, ud := range childUD {
		ps = append(ps, platform.Process{PID: mainPID + 1 + i, PPID: mainPID, Name: "Hermes.exe",
			Cmdline: exeCmd + ` --type=renderer --user-data-dir="` + ud + `"`, Created: t0.Add(time.Second)})
	}
	return ps
}

// The settings commands may target a sandbox userData while the user's app
// runs on the live one; every case where the instance may be ours refuses.
func TestEnsureClosedScopesToUserData(t *testing.T) {
	cases := []struct {
		name   string
		procs  func(ours string) []platform.Process
		refuse bool
	}{
		{"app on another userData (children prove it)", func(string) []platform.Process { return desktopTree(100, exeCmd, liveUD, liveUD) }, false},
		{"app on our userData", func(o string) []platform.Process { return desktopTree(100, exeCmd, o) }, true},
		{"app without helper children (unknown dir)", func(string) []platform.Process { return desktopTree(100, exeCmd) }, true},
		{"children disagree: one is ours", func(o string) []platform.Process { return desktopTree(100, exeCmd, liveUD, o) }, true},
		{"main names our dir", func(o string) []platform.Process {
			return desktopTree(100, exeCmd+` --user-data-dir="`+o+`"`, liveUD)
		}, true},
		{"main names another dir", func(string) []platform.Process {
			return desktopTree(100, exeCmd+` --user-data-dir="`+liveUD+`"`)
		}, false},
		{"stale child older than main (reused PPID) does not prove", func(string) []platform.Process {
			ps := desktopTree(100, exeCmd, liveUD)
			ps[1].Created = ps[0].Created.Add(-time.Hour)
			return ps
		}, true},
		{"two apps, one on ours", func(o string) []platform.Process {
			return append(desktopTree(100, exeCmd, liveUD), desktopTree(200, exeCmd, o)...)
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			for _, p := range c.procs(e.ud) {
				e.procs.Add(p)
			}
			err := e.svc.ensureClosed(context.Background())
			if got := errors.Is(err, apperr.ErrHermesRunning); got != c.refuse {
				t.Fatalf("refuse = %v (err %v), want %v", got, err, c.refuse)
			}
		})
	}
}

// With the live app running on another userData, a sandbox migration runs.
func TestMigrateSandboxWhileOtherAppRuns(t *testing.T) {
	e := newEnv(t)
	for _, p := range desktopTree(100, exeCmd, liveUD) {
		e.procs.Add(p)
	}
	rep, err := e.svc.Migrate(context.Background())
	if err != nil || !rep.Changed {
		t.Fatalf("Migrate = %+v, %v; want changed", rep, err)
	}
}
