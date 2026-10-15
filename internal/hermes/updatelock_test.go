package hermes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// update_lock.checkout_lock_path: <git common dir>/hermes-update.lock, else
// <root>/.hermes-update.lock.
func TestCheckoutLockPath(t *testing.T) {
	t.Run("plain checkout", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, want := CheckoutLockPath(root), filepath.Join(root, ".git", "hermes-update.lock"); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("no .git (ZIP install)", func(t *testing.T) {
		root := t.TempDir()
		if got, want := CheckoutLockPath(root), filepath.Join(root, ".hermes-update.lock"); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("linked worktree names the shared dir in commondir", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "wt")
		shared := filepath.Join(base, "main", ".git")
		gitdir := filepath.Join(shared, "worktrees", "wt")
		writeFile(t, filepath.Join(root, ".git"), "\ufeffgitdir: "+gitdir+"\n")
		writeFile(t, filepath.Join(gitdir, "commondir"), "../..\n")
		if got, want := CheckoutLockPath(root), filepath.Join(shared, "hermes-update.lock"); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("relative gitdir", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, ".git"), "gitdir: modules/x\n")
		if got, want := CheckoutLockPath(root), filepath.Join(root, "modules", "x", "hermes-update.lock"); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run(".git file that is not a gitdir pointer", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, ".git"), "garbage")
		if got, want := CheckoutLockPath(root), filepath.Join(root, ".hermes-update.lock"); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
}

func TestParseLockRecordFixture(t *testing.T) {
	pid, started, ct, ok := parseLockRecord([]byte(readFixture(t, "update-lock-record.txt")))
	if !ok || pid != 4242 || started.Unix() != 1760000000 || ct == nil || *ct != 1760000000.123 {
		t.Errorf("got pid=%d started=%v ct=%v ok=%v", pid, started, ct, ok)
	}
	// CRLF, BOM and surrounding blanks, as every Hermes reader tolerates
	pid, started, _, ok = parseLockRecord([]byte("\ufeff 4242 \r\n 1760000000 \r\nct:1760000000.123\r\n"))
	if !ok || pid != 4242 || started.Unix() != 1760000000 {
		t.Errorf("CRLF: pid=%d started=%v ok=%v", pid, started, ok)
	}
	if _, _, _, ok := parseLockRecord([]byte("")); ok {
		t.Error("empty record must not name a holder")
	}
	if _, _, _, ok := parseLockRecord([]byte("garbage\n")); ok {
		t.Error("garbage record must not name a holder")
	}
}

var lockNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

const lockRecordText = "4242\n1760000000\nct:1760000000.123\n"

func lockFixture(t *testing.T, info platform.LockInfo, procs ...platform.Process) (CheckoutLock, *platform.FakeLockProbe) {
	t.Helper()
	probe := &platform.FakeLockProbe{Info: info}
	return CheckoutLock{
		Probe: probe, Procs: platform.NewFakeProcs(procs...), Now: func() time.Time { return lockNow },
		Created: func(pid int) (time.Time, bool) {
			for _, p := range procs {
				if p.PID == pid {
					return p.Created, !p.Created.IsZero()
				}
			}
			return time.Time{}, false
		},
	}, probe
}

func TestCheckoutLockFreeAsksTheContractRange(t *testing.T) {
	c, probe := lockFixture(t, platform.LockInfo{State: platform.LockFree})
	root := t.TempDir()
	st := c.Check(context.Background(), root)
	if st.Verdict != LockOK || !st.Probed {
		t.Fatalf("got %+v", st)
	}
	if got := probe.Asked(); len(got) != 1 || got[0] != CheckoutLockPath(root) {
		t.Errorf("probed %v", got)
	}
	// 1 MiB, owner byte + 16 lease bytes: the range marker.ps1 locks
	if got := probe.Ranges(); len(got) != 1 || got[0] != [2]int64{1048576, 17} {
		t.Errorf("range = %v, want 1048576+17", got)
	}
}

func TestCheckoutLockHeldByLiveOwnerNamesIt(t *testing.T) {
	owner := platform.Process{PID: 4242, PPID: 1, Name: "python.exe", Created: time.Unix(1760000000, 123_000_000)}
	c, _ := lockFixture(t, platform.LockInfo{State: platform.LockHeld, Record: []byte(lockRecordText)}, owner)
	st := c.Check(context.Background(), t.TempDir())
	if st.Verdict != LockHeld || !st.HaveRecord || st.OwnerPID != 4242 || !st.OwnerLive || st.OwnerName != "python.exe" {
		t.Fatalf("got %+v", st)
	}
	if st.Age != lockNow.Sub(time.Unix(1760000000, 0)) {
		t.Errorf("age = %v", st.Age)
	}
	if len(st.Candidates) != 0 {
		t.Errorf("a live owner needs no candidate list: %v", st.Candidates)
	}
}

// The owner is gone; what still holds the lock is a process it started.
func TestCheckoutLockHeldByDeadOwnerListsItsDescendants(t *testing.T) {
	started := time.Unix(1760000000, 0)
	procs := []platform.Process{
		{PID: 5000, PPID: 4242, Name: "python.exe", Created: started.Add(30 * time.Second)},        // child of the dead owner
		{PID: 5001, PPID: 5000, Name: "node.exe", Created: started.Add(40 * time.Second)},          // its child
		{PID: 5002, PPID: 4242, Name: "git.exe", Created: started.Add(-time.Hour)},                 // pid reuse: older than the record
		{PID: 6000, PPID: 1, Name: "python.exe", Created: started.Add(10 * time.Second)},           // unrelated
		{PID: 7777, PPID: 4242, Name: "hermes-safe-update.exe", Created: started.Add(time.Minute)}, // us
	}
	c, _ := lockFixture(t, platform.LockInfo{State: platform.LockHeld, Record: []byte(lockRecordText)}, procs...)
	c.SelfPID = 7777
	st := c.Check(context.Background(), t.TempDir())
	if st.Verdict != LockHeld || st.OwnerLive {
		t.Fatalf("owner 4242 is not running: %+v", st)
	}
	var got []int
	for _, p := range st.Candidates {
		got = append(got, p.PID)
	}
	if len(got) != 2 || got[0] != 5000 || got[1] != 5001 {
		t.Errorf("candidates = %v, want [5000 5001]", got)
	}
}

// A pid that is alive but is another incarnation (pid reuse) is not the owner.
func TestCheckoutLockReusedOwnerPidIsNotLive(t *testing.T) {
	reuser := platform.Process{PID: 4242, PPID: 1, Name: "notepad.exe", Created: time.Unix(1760000000, 0).Add(2 * time.Hour)}
	c, _ := lockFixture(t, platform.LockInfo{State: platform.LockHeld, Record: []byte(lockRecordText)}, reuser)
	st := c.Check(context.Background(), t.TempDir())
	if st.Verdict != LockHeld || st.OwnerLive {
		t.Fatalf("a reused pid must not count as the owner: %+v", st)
	}
}

func TestCheckoutLockHeldWithoutReadableRecord(t *testing.T) {
	c, _ := lockFixture(t, platform.LockInfo{State: platform.LockHeld, Record: []byte("not a record")})
	st := c.Check(context.Background(), t.TempDir())
	if st.Verdict != LockHeld || st.HaveRecord || st.OwnerPID != 0 {
		t.Fatalf("got %+v", st)
	}
}

func TestCheckoutLockUnopenable(t *testing.T) {
	c, _ := lockFixture(t, platform.LockInfo{State: platform.LockUnopenable, Detail: "Access is denied."})
	st := c.Check(context.Background(), t.TempDir())
	if st.Verdict != LockUnopenable || st.Detail != "Access is denied." {
		t.Fatalf("got %+v", st)
	}
}

// Not being able to ask is never a stop.
func TestCheckoutLockProbeFailureIsUnknownNotHeld(t *testing.T) {
	c, probe := lockFixture(t, platform.LockInfo{State: platform.LockHeld})
	probe.Err = errors.New("lock probe: boom")
	st := c.Check(context.Background(), t.TempDir())
	if st.Verdict != LockOK || st.Probed || st.Err == nil {
		t.Fatalf("got %+v", st)
	}
	st = CheckoutLock{}.Check(context.Background(), t.TempDir())
	if st.Verdict != LockOK || st.Probed {
		t.Fatalf("no probe: %+v", st)
	}
}
