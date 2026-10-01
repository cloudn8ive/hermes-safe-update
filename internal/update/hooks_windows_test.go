//go:build windows

package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// TestHookHelperProcess is re-executed as the hook (never a real tool).
// HSU_HOOK_HELPER=parent: start a sleeping grandchild that holds our
// stdout pipe, write its pid to HSU_HOOK_PIDFILE, then sleep; =sleep: just sleep.
func TestHookHelperProcess(t *testing.T) {
	switch os.Getenv("HSU_HOOK_HELPER") {
	case "":
		return
	case "parent":
		c := exec.Command(os.Args[0], "-test.run=TestHookHelperProcess")
		c.Env = append(os.Environ(), "HSU_HOOK_HELPER=sleep")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Start(); err != nil {
			os.Exit(3)
		}
		_ = os.WriteFile(os.Getenv("HSU_HOOK_PIDFILE"), []byte(strconv.Itoa(c.Process.Pid)), 0o644)
		time.Sleep(time.Minute)
	default:
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

// A hook that spawns a grandchild sleeping past the timeout: with the real
// runner and the real process table both must be gone.
func TestHookTimeoutKillsGrandchildForReal(t *testing.T) {
	procs := platform.New().Procs
	h := &ExecHooks{
		Runner: execx.New(), Procs: procs, Dir: t.TempDir(),
		Hooks: []config.Hook{{Name: "slow", When: config.HookPostUpdate,
			Run: []string{os.Args[0], "-test.run=TestHookHelperProcess"}, TimeoutS: 2}},
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("HSU_HOOK_HELPER", "parent")
	t.Setenv("HSU_HOOK_PIDFILE", pidFile)
	start := time.Now()
	out := h.Run(context.Background(), config.HookPostUpdate, nil)
	if len(out) != 1 || !out[0].TimedOut {
		t.Fatalf("out = %+v (after %v)", out, time.Since(start))
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("helper never reported its grandchild: %v", err)
	}
	grand, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file %q: %v", b, err)
	}
	defer func() { _ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(grand)).Run() }() // never leak
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && procs.Alive(grand) {
		time.Sleep(50 * time.Millisecond)
	}
	if procs.Alive(grand) {
		t.Errorf("grandchild %d survived the hook timeout", grand)
	}
}
