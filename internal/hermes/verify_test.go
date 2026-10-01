package hermes

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func TestVerifyDesktop(t *testing.T) {
	cases := []struct {
		name string
		res  execx.Result
		ok   bool
	}{
		{"rc 0 and empty output", execx.Result{}, true},
		{"rc 0 with whitespace only", execx.Result{Output: " \r\n"}, true},
		{"rc 0 but output", execx.Result{Output: "warning: stale chunk"}, false},
		{"rc 1", execx.Result{Code: 1, Output: "desktop build missing"}, false},
		{"timeout", execx.Result{Code: 124, TimedOut: true, Output: "timed out after 5m0s"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execx.Fake{}
			f.On([]string{`L:\hermes.exe`, "--run-module", "hermes_cli.desktop_update_verify"}, c.res)
			v := &CLIVerifier{Launcher: `L:\hermes.exe`, Run: f}
			ok, out := v.VerifyDesktop(context.Background())
			if ok != c.ok {
				t.Errorf("ok = %v", ok)
			}
			if !c.ok && out == "" {
				t.Error("a failure must carry output to show")
			}
			if f.Calls()[0].Timeout != 300*time.Second {
				t.Errorf("timeout = %v", f.Calls()[0].Timeout)
			}
		})
	}
}

func TestVerifyDesktopWithoutLauncher(t *testing.T) {
	ok, out := (&CLIVerifier{Run: &execx.Fake{}}).VerifyDesktop(context.Background())
	if ok || !strings.Contains(out, "launcher") {
		t.Errorf("ok=%v out=%q", ok, out)
	}
}

func TestVerifyDesktopRunnerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &execx.Fake{}
	f.OnPrefix([]string{"x"}, execx.Result{})
	ok, out := (&CLIVerifier{Launcher: "x", Run: f}).VerifyDesktop(ctx)
	if ok || out == "" {
		t.Errorf("ok=%v out=%q", ok, out)
	}
}

func TestEvaluateUpdate(t *testing.T) {
	const head = "4e7403130ee278bd99c450fcd9f73c6b32135c95"
	fleet := "x\nFleet version check returned no rows\ny"
	cases := []struct {
		name      string
		in        VerifyInput
		wantOK    bool
		wantFalse bool
		wantCode  bool
	}{
		{"clean", VerifyInput{RC: 0, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true, VerifyRC: 0}, true, false, true},
		{"false fleet warning", VerifyInput{RC: 1, Output: fleet, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true, VerifyRC: 0}, true, true, true},
		{"fleet text but rc 2 is not the known case", VerifyInput{RC: 2, Output: fleet, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true}, false, false, true},
		{"fleet text with sha mismatch", VerifyInput{RC: 1, Output: fleet, Head: head, Gateway: GatewayState{CodeSHA: "old", PID: 5}, Alive: true}, false, false, false},
		{"rc 1 without fleet text", VerifyInput{RC: 1, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true}, false, false, true},
		{"sha mismatch", VerifyInput{RC: 0, Head: head, Gateway: GatewayState{CodeSHA: "old", PID: 5}, Alive: true}, false, false, false},
		{"gateway dead", VerifyInput{RC: 0, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: false}, false, false, false},
		{"no sha reported", VerifyInput{RC: 0, Head: head, Alive: true}, false, false, false},
		{"empty head never matches", VerifyInput{RC: 0, Head: "", Gateway: GatewayState{CodeSHA: "", PID: 5}, Alive: true}, false, false, false},
		{"desktop verify failed", VerifyInput{RC: 0, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true, VerifyRC: 1}, false, false, true},
		{"unverifiable desktop (managed python missing, D7) is not ok", VerifyInput{RC: 0, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true, VerifyRC: 1}, false, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := EvaluateUpdate(c.in)
			if r.OK != c.wantOK || r.KnownFalseFleet != c.wantFalse || r.CodeOK != c.wantCode {
				t.Errorf("got %+v", r)
			}
			if r.Line == "" {
				t.Error("no RESULT line")
			}
			if c.wantOK && !strings.HasPrefix(r.Line, "RESULT: OK") {
				t.Errorf("line = %q", r.Line)
			}
			if !c.wantOK && !strings.HasPrefix(r.Line, "RESULT: PROBLEM") {
				t.Errorf("line = %q", r.Line)
			}
		})
	}
}

func TestWaitForGateway(t *testing.T) {
	const head = "4e7403130ee278bd99c450fcd9f73c6b32135c95"
	t.Run("becomes ready on the third poll", func(t *testing.T) {
		n := 0
		var slept time.Duration
		w := GatewayWait{
			State: func() GatewayState {
				n++
				if n < 3 {
					return GatewayState{CodeSHA: "old", PID: 9}
				}
				return GatewayState{CodeSHA: head, PID: 9}
			},
			Alive: func(int) bool { return true },
			Sleep: func(d time.Duration) { slept += d },
			Max:   120 * time.Second, Poll: 5 * time.Second,
		}
		st, alive := w.Wait(context.Background(), head)
		if st.CodeSHA != head || !alive || slept != 10*time.Second {
			t.Errorf("st=%+v alive=%v slept=%v", st, alive, slept)
		}
	})
	t.Run("gives up after the maximum", func(t *testing.T) {
		var slept time.Duration
		w := GatewayWait{
			State: func() GatewayState { return GatewayState{CodeSHA: "old", PID: 9} },
			Alive: func(int) bool { return true },
			Sleep: func(d time.Duration) { slept += d },
			Max:   120 * time.Second, Poll: 5 * time.Second,
		}
		st, _ := w.Wait(context.Background(), head)
		if st.CodeSHA != "old" || slept != 120*time.Second {
			t.Errorf("st=%+v slept=%v", st, slept)
		}
	})
	t.Run("dead pid is not ready even with the right sha", func(t *testing.T) {
		w := GatewayWait{
			State: func() GatewayState { return GatewayState{CodeSHA: head, PID: 9} },
			Alive: func(int) bool { return false },
			Sleep: func(time.Duration) {}, Max: 10 * time.Second, Poll: 5 * time.Second,
		}
		if _, alive := w.Wait(context.Background(), head); alive {
			t.Error("alive")
		}
	})
	t.Run("context cancel stops early", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		w := GatewayWait{
			State: func() GatewayState { return GatewayState{} },
			Alive: func(int) bool { return false },
			Sleep: func(time.Duration) { t.Error("slept") }, Max: time.Minute, Poll: time.Second,
		}
		w.Wait(ctx, head)
	})
}

func TestProcessAliveAdapter(t *testing.T) {
	p := platform.NewFakeProcs(platform.Process{PID: 7})
	if !AliveFunc(p)(7) || AliveFunc(p)(8) || AliveFunc(p)(0) || AliveFunc(p)(-3) {
		t.Error("alive adapter wrong (pid <= 0 must be dead)")
	}
}
