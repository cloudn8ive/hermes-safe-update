//go:build windows

package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBlockerText(t *testing.T) {
	cases := []struct {
		wins []winInfo
		want string
	}{
		{nil, "no visible window left: the app was likely still shutting down its backend"},
		{[]winInfo{{Title: "Hermes", Class: "Chrome_WidgetWin_1"}}, "windows still open: Hermes [Chrome_WidgetWin_1]"},
		{[]winInfo{{Title: "Hermes", Class: "Chrome_WidgetWin_1"}, {Title: "Quit?", Class: "#32770"}},
			"a dialog was open, most likely the 'still working' quit confirmation: Hermes [Chrome_WidgetWin_1]; Quit? [#32770]"},
		{[]winInfo{{Title: "a"}, {Title: "b"}, {Title: "c"}, {Title: "d"}, {Title: "e"}}, "windows still open: a []; b []; c []; d []"},
	}
	for _, c := range cases {
		if got := blockerText(c.wins); got != c.want {
			t.Errorf("blockerText(%v) = %q, want %q", c.wins, got, c.want)
		}
	}
}

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	c.Env = append(os.Environ(), "HSU_HELPER=sleep")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	return c
}

func TestRequestCloseWithoutWindowsLeavesProcessRunning(t *testing.T) {
	c := startSleeper(t)
	a := newWinApp(winProcs{})
	err := a.RequestClose(context.Background(), c.Process.Pid)
	if err == nil || !strings.Contains(err.Error(), "no window") {
		t.Errorf("RequestClose on a windowless process: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !a.IsRunning(c.Process.Pid) {
		t.Error("graceful close must never kill")
	}
	if b := a.CloseBlocker(c.Process.Pid); !strings.HasPrefix(b, "no visible window left") {
		t.Errorf("blocker %q", b)
	}
}

func TestForceCloseStopsOnlyThatProcess(t *testing.T) {
	c := startSleeper(t)
	other := startSleeper(t)
	a := newWinApp(winProcs{})
	if err := a.ForceClose(context.Background(), Process{PID: c.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Process.Wait()
	if a.IsRunning(c.Process.Pid) || !a.IsRunning(other.Process.Pid) {
		t.Errorf("target alive=%v other alive=%v", a.IsRunning(c.Process.Pid), a.IsRunning(other.Process.Pid))
	}
}

func TestLaunchUsesExplorerDetached(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "Hermes.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newWinApp(winProcs{})
	var got []string
	a.start = func(argv []string) error { got = argv; return nil }
	if ok, why := a.CanLaunch(exe); !ok {
		t.Fatalf("CanLaunch: %s", why)
	}
	if err := a.Launch(context.Background(), exe, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.EqualFold(filepath.Base(got[0]), "explorer.exe") || got[1] != exe {
		t.Errorf("argv %v", got)
	}
	if ok, _ := a.CanLaunch(exe + ".missing"); ok {
		t.Error("CanLaunch on a missing exe")
	}
	if err := a.Launch(context.Background(), exe+".missing", nil); err == nil {
		t.Error("Launch of a missing exe must fail before explorer shows a dialog")
	}
}
