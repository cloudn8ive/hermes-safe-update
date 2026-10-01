package update

import (
	"context"
	"slices"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// A hook that times out must be stopped with its whole process tree (a
// PowerShell hook's children would otherwise linger), the same wiring as
// the `hermes update` timeout.
func TestHookTimeoutKillsTheWholeTree(t *testing.T) {
	procs := platform.NewFakeProcs(
		platform.Process{PID: 500, PPID: 1, Name: "powershell.exe"},
		platform.Process{PID: 501, PPID: 500, Name: "sleeper.exe"},
	)
	runner := &execx.Fake{}
	var wired bool
	runner.OnFunc([]string{"hook.exe"}, func(c execx.Cmd) execx.Result {
		if c.KillTree == nil {
			t.Error("hook Cmd has no KillTree: a timeout would leave grandchildren behind")
			return execx.Result{Code: execx.CodeTimeout, TimedOut: true}
		}
		wired = true
		if err := c.KillTree(500); err != nil {
			t.Errorf("KillTree: %v", err)
		}
		return execx.Result{Code: execx.CodeTimeout, TimedOut: true}
	})
	h := &ExecHooks{
		Runner: runner, Procs: procs, Dir: "home",
		Hooks: []config.Hook{{Name: "slow", When: config.HookPostUpdate, Run: []string{"hook.exe"}, TimeoutS: 1}},
	}
	out := h.Run(context.Background(), config.HookPostUpdate, nil)
	if !wired || len(out) != 1 || !out[0].TimedOut {
		t.Fatalf("wired=%v out=%+v", wired, out)
	}
	if got := procs.Killed(); !slices.Equal(got, []int{500}) {
		t.Errorf("KillTree pids = %v, want [500]", got)
	}
	if procs.Alive(501) {
		t.Error("grandchild survived")
	}
}
