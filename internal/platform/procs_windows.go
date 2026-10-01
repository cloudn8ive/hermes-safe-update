//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winProcs implements Procs with native APIs (D15): a Toolhelp snapshot for
// pid/ppid/name, QueryFullProcessImageName for the exe, and
// NtQueryInformationProcess(ProcessCommandLineInformation) for the full
// command line (Windows 8.1+). No PowerShell, no WMI.
type winProcs struct{}

var _ Procs = winProcs{}

// List snapshots every process. Processes whose details cannot be read
// (other users', protected) keep pid/ppid/name with an empty command line.
func (winProcs) List(ctx context.Context) ([]Process, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	var out []Process
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p := Process{PID: int(e.ProcessID), PPID: int(e.ParentProcessID), Name: windows.UTF16ToString(e.ExeFile[:])}
		if p.PID != 0 {
			fillDetails(&p)
			out = append(out, p)
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("process snapshot: %w", err)
		}
	}
	return out, nil
}

func fillDetails(p *Process) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.PID))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		p.Exe = windows.UTF16ToString(buf[:n])
	}
	var c, x, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &x, &k, &u) == nil {
		p.Created = time.Unix(0, c.Nanoseconds())
	}
	p.Cmdline = commandLine(h)
}

// commandLine reads the process command line; "" when not permitted.
func commandLine(h windows.Handle) string {
	size := uint32(4096)
	for range 4 {
		buf := make([]byte, size)
		var ret uint32
		err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), size, &ret)
		if err == nil {
			us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
			if us.Buffer == nil || us.Length == 0 {
				return ""
			}
			return windows.UTF16ToString(unsafe.Slice(us.Buffer, us.Length/2))
		}
		if ret <= size {
			return ""
		}
		size = ret
	}
	return ""
}

// Alive reports whether pid is a running process (pid <= 0 is dead).
func (winProcs) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// access denied means it exists (another user's / elevated process)
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// KillTree terminates pid and every descendant (children first), like
// `taskkill /T /F`. Descendants are found from one snapshot by ppid, and a
// child is only counted when it was created after its parent (pids are
// reused on Windows).
func (w winProcs) KillTree(ctx context.Context, p Process) error {
	pid := p.PID
	if pid <= 0 {
		return fmt.Errorf("kill tree: invalid pid %d", pid)
	}
	all, err := w.List(ctx)
	if err != nil {
		return err
	}
	if err := CheckIdentity(all, p); err != nil {
		return fmt.Errorf("kill tree: refusing to stop pid %d: %w", pid, err)
	}
	if isSelfOrAncestor(all, pid, os.Getpid()) {
		return fmt.Errorf("kill tree: refusing to stop pid %d: it is this tool or one of its parents", pid)
	}
	tree := winDescendants(all, pid)
	for _, c := range tree {
		if c == os.Getpid() {
			return fmt.Errorf("kill tree: refusing to stop pid %d: this tool runs inside that tree", pid)
		}
	}
	var errs []error
	for i := len(tree) - 1; i >= 0; i-- { // deepest first
		if err := terminate(tree[i]); err != nil {
			errs = append(errs, err)
		}
	}
	if err := terminate(pid); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// winDescendants returns the pids below root in breadth-first order.
func winDescendants(all []Process, root int) []int {
	byPID := map[int]Process{}
	kids := map[int][]Process{}
	for _, p := range all {
		byPID[p.PID] = p
		kids[p.PPID] = append(kids[p.PPID], p)
	}
	var out []int
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		pp := byPID[parent]
		for _, c := range kids[parent] {
			if seen[c.PID] {
				continue
			}
			if !pp.Created.IsZero() && !c.Created.IsZero() && c.Created.Before(pp.Created) {
				continue // stale ppid: the real parent died and its pid was reused
			}
			seen[c.PID] = true
			out = append(out, c.PID)
			queue = append(queue, c.PID)
		}
	}
	return out
}

// isSelfOrAncestor reports whether pid is self or an ancestor of self.
func isSelfOrAncestor(all []Process, pid, self int) bool {
	parent := map[int]int{}
	for _, p := range all {
		parent[p.PID] = p.PPID
	}
	for cur, hops := self, 0; cur > 0 && hops < 64; hops++ {
		if cur == pid {
			return true
		}
		next, ok := parent[cur]
		if !ok || next == cur {
			break
		}
		cur = next
	}
	return false
}

func terminate(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil // already gone
		}
		return fmt.Errorf("open pid %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		if ev, _ := windows.WaitForSingleObject(h, 0); ev == windows.WAIT_OBJECT_0 {
			return nil // exited meanwhile
		}
		return fmt.Errorf("terminate pid %d: %w", pid, err)
	}
	_, _ = windows.WaitForSingleObject(h, 5000)
	return nil
}
