package hermes

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// This file answers one question before Safe Update closes anything: does
// another process hold Hermes' checkout lock? (update_lock.py: a kernel lock
// on <git common dir>/hermes-update.lock that `hermes update` holds for its
// whole process tree.) A killed updater's orphaned child keeps the lock after
// its owner died, so `hermes update` then refuses ("its owner exited but a
// process it started still holds the checkout", exit 2): after Safe Update
// already closed Hermes. The probe is read-only: a try-lock that is given
// back at once (platform.LockProbe).

// gitCommonDir mirrors update_lock._git_common_dir: `.git` is the dir itself,
// or a `gitdir: <path>` file (linked worktree, submodule) whose target may name
// the shared dir in `commondir`. "" when root is no git checkout. Read from
// disk on purpose: no git process, and the very same answer Hermes computes.
func gitCommonDir(root string) string {
	dot := filepath.Join(root, ".git")
	st, err := os.Stat(dot)
	if err != nil {
		return ""
	}
	var gitdir string
	if st.IsDir() {
		gitdir = dot
	} else {
		b, err := os.ReadFile(dot)
		if err != nil {
			return ""
		}
		text := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
		rest, ok := strings.CutPrefix(text, "gitdir:")
		if !ok {
			return ""
		}
		target := strings.TrimSpace(rest)
		if filepath.IsAbs(target) {
			gitdir = target // an absolute target replaces root
		} else {
			gitdir = filepath.Join(root, target)
		}
	}
	if b, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		if rel := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff")); rel != "" {
			if filepath.IsAbs(rel) {
				gitdir = rel
			} else {
				gitdir = filepath.Join(gitdir, rel)
			}
		}
	}
	return filepath.Clean(gitdir)
}

// CheckoutLockPath is update_lock.checkout_lock_path: the lock file of the
// install at checkout.
func CheckoutLockPath(checkout string) string {
	if common := gitCommonDir(checkout); common != "" {
		return filepath.Join(common, CheckoutLockName)
	}
	return filepath.Join(checkout, CheckoutLockNameNoGit)
}

// LockVerdict is the outcome of CheckoutLock.Check.
type LockVerdict int

const (
	// LockOK: nothing holds the lock (or the probe could not ask: unknown is
	// never a stop).
	LockOK LockVerdict = iota
	// LockHeld: a process holds the checkout lock.
	LockHeld
	// LockUnopenable: the lock file exists but cannot be opened; `hermes
	// update` would refuse too (Hermes answers "held").
	LockUnopenable
)

// LockStatus is what the probe learned.
type LockStatus struct {
	Verdict LockVerdict
	Path    string
	// Probed is false when the probe could not be asked at all (no probe on
	// this OS, or an error): Verdict is then LockOK and Err says why.
	Probed bool
	Err    error
	Detail string // OS error text of LockUnopenable

	// The holder record of the lock file (what the last `hermes update`
	// wrote), when it could be read.
	OwnerPID   int
	OwnerLive  bool   // that pid is alive and is the same incarnation
	OwnerName  string // its executable base name, when live and listed
	Started    time.Time
	Age        time.Duration
	HaveRecord bool

	// Possible holders when the owner is dead: live processes that descend
	// from the dead owner and started at or after the record (at most
	// maxLockCandidates). A hint to look at, never proof.
	Candidates []platform.Process
}

const maxLockCandidates = 5

// CheckoutLock probes Hermes' checkout lock.
type CheckoutLock struct {
	Probe platform.LockProbe
	Procs platform.Procs
	// Created reads a process's creation time (platform.ProcessCreated); nil
	// = unknown, so a recorded creation time is not compared.
	Created func(pid int) (time.Time, bool)
	Now     func() time.Time
	// SelfPID is excluded from the candidate list.
	SelfPID int
}

func (c CheckoutLock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Check probes the lock of the install at checkout. It never takes the lock
// for longer than one try, never creates the file and never stops anything.
func (c CheckoutLock) Check(ctx context.Context, checkout string) LockStatus {
	st := LockStatus{Path: CheckoutLockPath(checkout)}
	if c.Probe == nil || checkout == "" {
		return st
	}
	info, err := c.Probe.ProbeLock(st.Path, CheckoutLockOffset, CheckoutLockLength)
	if err != nil {
		st.Err = err
		return st
	}
	st.Probed = true
	switch info.State {
	case platform.LockHeld:
		st.Verdict = LockHeld
	case platform.LockUnopenable:
		st.Verdict, st.Detail = LockUnopenable, info.Detail
		return st
	default:
		return st
	}
	c.readRecord(ctx, &st, info.Record)
	return st
}

// lockRecord is the holder record: "<pid>\n<started_at>\nct:<creation time>\n"
// (_lock_holder reads it with _parse_marker, the same format as the marker).
func parseLockRecord(b []byte) (pid int, started time.Time, ct *float64, ok bool) {
	info := parseMarker(b)
	if info.pid <= 0 {
		return 0, time.Time{}, nil, false
	}
	text := strings.TrimPrefix(string(b), "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) > 1 {
		if n, err := strconv.ParseInt(strings.Trim(strings.TrimSuffix(lines[1], "\r"), " \t"), 10, 64); err == nil && n > 0 {
			started = time.Unix(n, 0)
		}
	}
	return info.pid, started, info.ct, true
}

func (c CheckoutLock) readRecord(ctx context.Context, st *LockStatus, rec []byte) {
	pid, started, ct, ok := parseLockRecord(rec)
	if !ok {
		return
	}
	st.HaveRecord, st.OwnerPID, st.Started = true, pid, started
	if !started.IsZero() {
		if age := c.now().Sub(started); age > 0 {
			st.Age = age
		}
	}
	if c.Procs == nil {
		return
	}
	m := &Marker{Procs: c.Procs, Created: c.Created}
	st.OwnerLive = m.identityLive(pid, ct)
	procs, err := c.Procs.List(ctx)
	if err != nil {
		return
	}
	if st.OwnerLive {
		for _, p := range procs {
			if p.PID == pid {
				st.OwnerName = p.Name
			}
		}
		return
	}
	st.Candidates = descendantsSince(procs, pid, started, c.SelfPID)
}

// descendantsSince lists live processes that descend from root (by parent
// pid; a dead parent's pid stays in its children) and were created at or
// after since (when both times are known). Pids are lowest first.
func descendantsSince(procs []platform.Process, root int, since time.Time, self int) []platform.Process {
	byParent := map[int][]platform.Process{}
	for _, p := range procs {
		byParent[p.PPID] = append(byParent[p.PPID], p)
	}
	var out []platform.Process
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 && len(out) < maxLockCandidates {
		cur := queue[0]
		queue = queue[1:]
		for _, p := range byParent[cur] {
			if seen[p.PID] || p.PID == self {
				continue
			}
			seen[p.PID] = true
			if !since.IsZero() && !p.Created.IsZero() && p.Created.Before(since.Add(-2*time.Second)) {
				continue // older than the record: pid reuse, not a child of that update
			}
			out = append(out, p)
			queue = append(queue, p.PID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	if len(out) > maxLockCandidates {
		out = out[:maxLockCandidates]
	}
	return out
}
