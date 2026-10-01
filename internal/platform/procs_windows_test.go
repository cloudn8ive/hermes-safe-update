//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// TestHelperProcess is re-executed by the tests below (never Hermes).
// HSU_HELPER=sleep: sleep; HSU_HELPER=parent: start a sleeping grandchild,
// print its pid, then sleep.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("HSU_HELPER") {
	case "":
		return
	case "parent", "parent-pipe":
		c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
		c.Env = append(os.Environ(), "HSU_HELPER=sleep")
		if os.Getenv("HSU_HELPER") == "parent-pipe" { // grandchild holds our stdout pipe
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
		}
		if err := c.Start(); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString(strconv.Itoa(c.Process.Pid) + "\n")
		time.Sleep(time.Minute)
	default:
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func TestSnapshotSeesSelfWithCommandLine(t *testing.T) {
	procs, err := winProcs{}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	for _, p := range procs {
		if p.PID == me {
			if p.PPID != os.Getppid() {
				t.Errorf("ppid %d, want %d", p.PPID, os.Getppid())
			}
			if !strings.EqualFold(p.Name, "platform.test.exe") && !strings.HasSuffix(strings.ToLower(p.Name), ".test.exe") {
				t.Errorf("name %q", p.Name)
			}
			if !strings.Contains(p.Cmdline, "-test.") {
				t.Errorf("command line %q lacks the test flags", p.Cmdline)
			}
			if p.Exe == "" || p.Created.IsZero() {
				t.Errorf("exe=%q created=%v", p.Exe, p.Created)
			}
			return
		}
	}
	t.Fatalf("own pid %d not in a snapshot of %d processes", me, len(procs))
}

func TestAliveAndKillTreeOnOwnHelper(t *testing.T) {
	c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	c.Env = append(os.Environ(), "HSU_HELPER=parent")
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	grand, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatalf("helper said %q", buf[:n])
	}
	p := winProcs{}
	if !p.Alive(c.Process.Pid) || !p.Alive(grand) {
		t.Fatal("helpers should be alive")
	}
	if p.Alive(0) || p.Alive(-1) {
		t.Error("pid <= 0 must be dead")
	}
	if err := p.KillTree(context.Background(), Process{PID: c.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (p.Alive(c.Process.Pid) || p.Alive(grand)) {
		time.Sleep(50 * time.Millisecond)
	}
	if p.Alive(c.Process.Pid) || p.Alive(grand) {
		t.Errorf("tree not killed: parent alive=%v grandchild alive=%v", p.Alive(c.Process.Pid), p.Alive(grand))
	}
}

func TestKillTreeRefusesOwnProcessAndAncestors(t *testing.T) {
	p := winProcs{}
	for _, pid := range []int{os.Getpid(), os.Getppid()} {
		if err := p.KillTree(context.Background(), Process{PID: pid}); err == nil {
			t.Errorf("KillTree(%d) must refuse: it is this tool or its parent", pid)
		}
	}
	if !p.Alive(os.Getpid()) {
		t.Fatal("killed itself")
	}
}

// The reviewer's reproduction: execx.Stream times out on a child whose
// grandchild holds the output pipe. With KillTree wired as the cancel, the
// grandchild must be gone too (hermes update's python/git/npm children).
func TestStreamTimeoutWithKillTreeStopsGrandchild(t *testing.T) {
	p := winProcs{}
	var grand int
	c := execx.Cmd{
		Argv:    []string{os.Args[0], "-test.run=TestHelperProcess"},
		Env:     map[string]string{"HSU_HELPER": "parent-pipe"},
		Timeout: 2 * time.Second,
		KillTree: func(pid int) error {
			return p.KillTree(context.Background(), Process{PID: pid})
		},
	}
	res, err := execx.New().Stream(context.Background(), c, func(line string) {
		if n, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			grand = n
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("res = %+v", res)
	}
	if grand == 0 {
		t.Fatalf("helper never reported its grandchild: %q", res.Output)
	}
	defer func() { _ = terminate(grand) }() // never leak a helper if the assertion fails
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && p.Alive(grand) {
		time.Sleep(50 * time.Millisecond)
	}
	if p.Alive(grand) {
		t.Errorf("grandchild %d survived the timeout", grand)
	}
}

func TestKillTreeRefusesReusedPidOnRealProcess(t *testing.T) {
	c := startSleeper(t)
	p := winProcs{}
	all, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var real Process
	for _, q := range all {
		if q.PID == c.Process.Pid {
			real = q
		}
	}
	if real.Created.IsZero() {
		t.Skip("start time not readable")
	}
	stale := real
	stale.Created = real.Created.Add(-time.Hour)
	if err := p.KillTree(context.Background(), stale); !errors.Is(err, ErrProcessChanged) {
		t.Fatalf("err = %v, want ErrProcessChanged", err)
	}
	if !p.Alive(c.Process.Pid) {
		t.Fatal("a process with a different start time was killed")
	}
	if err := p.KillTree(context.Background(), real); err != nil {
		t.Fatal(err)
	}
}
