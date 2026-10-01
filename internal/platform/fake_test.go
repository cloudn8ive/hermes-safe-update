package platform

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeProcsKillTreeRemovesDescendants(t *testing.T) {
	p := NewFakeProcs(
		Process{PID: 10, Name: "Hermes.exe"},
		Process{PID: 11, PPID: 10, Name: "Hermes.exe"},
		Process{PID: 12, PPID: 11, Name: "node.exe"},
		Process{PID: 20, Name: "other.exe"},
	)
	if err := p.KillTree(context.Background(), Process{PID: 10}); err != nil {
		t.Fatal(err)
	}
	list, _ := p.List(context.Background())
	if len(list) != 1 || list[0].PID != 20 {
		t.Errorf("left: %+v", list)
	}
	if p.Alive(11) || !p.Alive(20) {
		t.Error("Alive disagrees with the list")
	}
	if got := p.Killed(); len(got) != 1 || got[0] != 10 {
		t.Errorf("Killed() = %v", got)
	}
}

func TestFakeAppGracefulCloseRemovesProcess(t *testing.T) {
	procs := NewFakeProcs(Process{PID: 5, Name: "Hermes.exe"}, Process{PID: 6, PPID: 5})
	app := &FakeApp{Procs: procs, CloseBehaviour: CloseExits}
	if err := app.RequestClose(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if app.IsRunning(5) || procs.Alive(6) {
		t.Error("graceful close should end the tree")
	}

	procs = NewFakeProcs(Process{PID: 5})
	app = &FakeApp{Procs: procs, CloseBehaviour: CloseIgnored, Blocker: "a dialog was open"}
	_ = app.RequestClose(context.Background(), 5)
	if !app.IsRunning(5) {
		t.Error("CloseIgnored must keep the process")
	}
	if app.CloseBlocker(5) != "a dialog was open" {
		t.Error("blocker text not returned")
	}
	_ = app.ForceClose(context.Background(), Process{PID: 5})
	if app.IsRunning(5) {
		t.Error("ForceClose must end it")
	}
	if got := app.Calls(); len(got) != 2 || got[0] != "close 5" || got[1] != "force 5" {
		t.Errorf("Calls() = %v", got)
	}
}

func TestFakeConsoleScriptedKeysAndTimeout(t *testing.T) {
	c := &FakeConsole{Keys: []Key{{Rune: 'y'}}}
	k, err := c.ReadKey(context.Background())
	if err != nil || k.Rune != 'y' {
		t.Fatalf("got %+v %v", k, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.ReadKey(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no more keys should wait for ctx, got %v", err)
	}
}

func TestFakeProgressRecords(t *testing.T) {
	p := &FakeProgress{}
	p.Set(ProgressNormal, 0.5)
	p.Set(ProgressPaused, 0.5)
	p.Close()
	if len(p.States) != 2 || p.States[1] != ProgressPaused || !p.Closed {
		t.Errorf("%+v", p)
	}
}

func TestNewFakeIsComplete(t *testing.T) {
	f := NewFake()
	if f.Paths == nil || f.App == nil || f.Procs == nil || f.Console == nil ||
		f.Progress == nil || f.Autostart == nil || f.Disk == nil {
		t.Fatalf("NewFake left a backend nil: %+v", f)
	}
	if f.OS != "fake" {
		t.Errorf("OS = %q", f.OS)
	}
}

func TestFakeProcsKillTreeRefusesChangedIdentity(t *testing.T) {
	t0 := time.Unix(1790000000, 0)
	p := NewFakeProcs(Process{PID: 10, Name: "Hermes.exe", Created: t0.Add(time.Hour)})
	err := p.KillTree(context.Background(), Process{PID: 10, Created: t0})
	if !errors.Is(err, ErrProcessChanged) || len(p.Killed()) != 0 || !p.Alive(10) {
		t.Errorf("err %v killed %v", err, p.Killed())
	}
	if err := p.KillTree(context.Background(), Process{PID: 10, Created: t0.Add(time.Hour)}); err != nil || p.Alive(10) {
		t.Errorf("unchanged identity must be killed: err %v", err)
	}
}
