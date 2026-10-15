//go:build windows

package platform

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The Hermes update lock: one byte at 1 MiB plus 16 lease bytes (update_lock.py).
const (
	testLockOffset = 1 << 20
	testLockLength = 17
)

const helperEnv = "HSU_LOCK_HELPER"

// TestHelperHoldLock is not a test: the lock tests re-run the test binary on
// this function to get a second process that holds a byte range of a file
// until its stdin closes (a real foreign holder, not a second handle of ours).
func TestHelperHoldLock(t *testing.T) {
	spec := os.Getenv(helperEnv)
	if spec == "" {
		t.Skip("helper process only")
	}
	parts := strings.Split(spec, "|")
	off, _ := strconv.ParseInt(parts[1], 10, 64)
	n, _ := strconv.ParseInt(parts[2], 10, 64)
	f, err := os.OpenFile(parts[0], os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ov := new(windows.Overlapped)
	ov.Offset, ov.OffsetHigh = uint32(uint64(off)), uint32(uint64(off)>>32)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, uint32(n), 0, ov); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\x00') // until the parent closes our stdin
}

// holdRange starts a helper process that holds [off, off+n) of path. The
// returned func releases it by letting the helper exit.
func holdRange(t *testing.T, path string, off, n int64) (release func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$", "-test.v")
	cmd.Env = append(os.Environ(), helperEnv+"="+path+"|"+strconv.FormatInt(off, 10)+"|"+strconv.FormatInt(n, 10))
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(out)
	ready := false
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "locked" {
			ready = true
			break
		}
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		in.Close()
		_ = cmd.Wait()
	}
	t.Cleanup(release)
	if !ready {
		t.Fatal("helper process did not take the lock")
	}
	return release
}

func lockFile(t *testing.T, record string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hermes-update.lock")
	if err := os.WriteFile(path, []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const testRecord = "4242\n1760000000\nct:1760000000.123\n"

func TestLockProbeMissingFileIsFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.lock")
	info, err := winLockProbe{}.ProbeLock(path, testLockOffset, testLockLength)
	if err != nil || info.State != LockFree {
		t.Fatalf("got %+v, %v", info, err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("the probe created the file")
	}
}

func TestLockProbeFreeFileIsFreeAndLeavesNoLock(t *testing.T) {
	path := lockFile(t, testRecord)
	info, err := winLockProbe{}.ProbeLock(path, testLockOffset, testLockLength)
	if err != nil || info.State != LockFree {
		t.Fatalf("got %+v, %v", info, err)
	}
	if string(info.Record) != testRecord {
		t.Errorf("record = %q", info.Record)
	}
	// Nothing may stay locked: a helper must be able to take the same range
	// right away (it fails in the helper, and holdRange fatals, otherwise).
	holdRange(t, path, testLockOffset, testLockLength)
}

func TestLockProbeSeesRangeHeldByAnotherProcess(t *testing.T) {
	path := lockFile(t, testRecord)
	release := holdRange(t, path, testLockOffset, 1) // the owner's byte
	info, err := winLockProbe{}.ProbeLock(path, testLockOffset, testLockLength)
	if err != nil || info.State != LockHeld {
		t.Fatalf("held by another process: got %+v, %v", info, err)
	}
	if string(info.Record) != testRecord {
		t.Errorf("the holder record must stay readable beside the locked range: %q", info.Record)
	}
	release()
	if info, err = (winLockProbe{}).ProbeLock(path, testLockOffset, testLockLength); err != nil || info.State != LockFree {
		t.Fatalf("after the holder exited: got %+v, %v", info, err)
	}
}

// A dead owner's leased child holds only a lease byte (R5b), not the owner's
// byte: the probe must cover the whole 17 bytes to see it.
func TestLockProbeSeesALeaseByteOnly(t *testing.T) {
	path := lockFile(t, testRecord)
	holdRange(t, path, testLockOffset+5, 1)
	info, err := winLockProbe{}.ProbeLock(path, testLockOffset, testLockLength)
	if err != nil || info.State != LockHeld {
		t.Fatalf("lease byte held: got %+v, %v", info, err)
	}
}

// A byte just past the 17 is somebody else's business.
func TestLockProbeIgnoresBytesOutsideTheRange(t *testing.T) {
	path := lockFile(t, testRecord)
	holdRange(t, path, testLockOffset+testLockLength, 1)
	info, err := winLockProbe{}.ProbeLock(path, testLockOffset, testLockLength)
	if err != nil || info.State != LockFree {
		t.Fatalf("outside the range: got %+v, %v", info, err)
	}
}

// Hermes answers "held" for a lock file that exists but cannot be opened.
func TestLockProbeUnopenableFileCountsAsHeld(t *testing.T) {
	path := lockFile(t, testRecord)
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	info, err := winLockProbe{}.ProbeLock(path, testLockOffset, testLockLength)
	if err != nil || info.State != LockUnopenable || info.Detail == "" {
		t.Fatalf("got %+v, %v", info, err)
	}
}

// Hermes takes the lock with Python's msvcrt.locking. Prove that lock and the
// probe's LockFileEx are the same kernel lock: a Python process holds the
// range exactly the way update_lock.py does, and the probe sees it.
func TestLockProbeSeesPythonMsvcrtLock(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH")
	}
	if err := exec.Command(py, "-c", "import msvcrt").Run(); err != nil {
		t.Skip("python cannot import msvcrt (a Store stub?)")
	}
	path := lockFile(t, testRecord)
	script := "import msvcrt, os, sys\n" +
		"fd = os.open(sys.argv[1], os.O_RDWR | os.O_BINARY)\n" +
		"os.lseek(fd, 1 << 20, os.SEEK_SET)\n" +
		"msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)\n" +
		"print('locked', flush=True)\n" +
		"sys.stdin.read()\n"
	cmd := exec.Command(py, "-c", script, path)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Wait() }()
	line, _ := bufio.NewReader(out).ReadString('\n')
	if strings.TrimSpace(line) != "locked" {
		t.Fatalf("python did not take the lock: %q", line)
	}
	info, perr := winLockProbe{}.ProbeLock(path, testLockOffset, testLockLength)
	if perr != nil || info.State != LockHeld {
		t.Fatalf("msvcrt lock not seen: %+v, %v", info, perr)
	}
}
