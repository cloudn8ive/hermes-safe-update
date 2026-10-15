package hermes

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// An elevated `hermes update` (or install) leaves entries in <home>\tools
// owned by BUILTIN\Administrators with no ACE the filtered, normal token can
// use. A later normal update fails on them with [WinError 5] (hermes-agent
// #126860). This file finds such entries BEFORE anything is closed, with the
// only test that cannot be wrong about it: really opening each entry with
// the token this process runs with (platform.Access). It never reads ACLs
// and never judges by owner or by a missing user ACE: a healthy entry can
// have exactly the ACL a damaged one has.
//
// Verdicts (spec "Group 4", #126860):
//   - read denied on an entry under tools\  -> Stop (the update is predicted
//     to hit WinError 5; two independent reports);
//   - read denied elsewhere, or modify denied anywhere -> Warn only (Defender's
//     Controlled Folder Access also denies writes, and a few folders are
//     legitimately not ours);
//   - a sharing violation, a missing path, a symlink/junction, or an open
//     that failed for another reason is never a finding.

const (
	defaultACLMaxOpens = 400
	defaultACLBudget   = 2 * time.Second
	// defaultACLRecheck is the pause before a refused tools\ entry is opened
	// once more: a file in the delete-pending state, or one an antivirus
	// scanner holds, is refused with the same code and clears by itself.
	defaultACLRecheck = 400 * time.Millisecond
)

// ACLFinding is one entry the current user cannot open.
type ACLFinding struct {
	Path    string
	Mask    platform.AccessMask // the access that was refused
	InTools bool                // under <home>\tools (the tools folder itself included)
}

// ACLReport is the outcome of ACLChecker.Run.
type ACLReport struct {
	Stop       []ACLFinding // read denied under tools\
	Warn       []ACLFinding // everything else that was refused
	Opens      int          // opens made
	Incomplete bool         // the open cap or the time budget ended the walk early
}

// Findings is every finding, Stop first.
func (r ACLReport) Findings() []ACLFinding {
	return append(append([]ACLFinding(nil), r.Stop...), r.Warn...)
}

// ACLChecker walks the places an update writes to.
type ACLChecker struct {
	Access   platform.Access
	Home     string
	Checkout string
	// MaxOpens and Budget bound the walk (0 = 400 opens, 2 s). The walk is
	// shallow on purpose: deep node_modules damage is missed, not slow.
	MaxOpens int
	Budget   time.Duration
	Now      func() time.Time
	// Sleep waits before the second look at refused tools\ entries (nil =
	// a real, cancellable 400 ms wait). It returns the context's error.
	Sleep func(ctx context.Context, d time.Duration) error
}

type aclWalk struct {
	ctx   context.Context
	c     ACLChecker
	rep   ACLReport
	start time.Time
	stop  bool // cap or budget reached
	seen  map[string]bool
}

func (c ACLChecker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Run probes the tools folder, its entries and their first-level children,
// then the other places the update writes to. Absent paths are skipped. No
// new open is started once ctx is done or the cap or budget is spent (one
// open that already blocks, on a dead network share, cannot be interrupted).
// Entries under tools\ that were refused are opened once more after a short
// pause, and only those that are refused again are reported.
func (c ACLChecker) Run(ctx context.Context) ACLReport {
	if c.Access == nil || (c.Home == "" && c.Checkout == "") {
		return ACLReport{}
	}
	if c.MaxOpens <= 0 {
		c.MaxOpens = defaultACLMaxOpens
	}
	if c.Budget <= 0 {
		c.Budget = defaultACLBudget
	}
	w := &aclWalk{ctx: ctx, c: c, start: c.now(), seen: map[string]bool{}}
	if c.Home != "" {
		w.tools()
	}
	w.others()
	if w.stop {
		w.rep.Incomplete = true
	}
	w.recheckStops()
	sort.Slice(w.rep.Stop, func(i, j int) bool { return w.rep.Stop[i].Path < w.rep.Stop[j].Path })
	sort.Slice(w.rep.Warn, func(i, j int) bool { return w.rep.Warn[i].Path < w.rep.Warn[j].Path })
	return w.rep
}

func (w *aclWalk) tools() {
	tools := filepath.Join(w.c.Home, "tools")
	if !w.entry(tools, true, true) {
		return
	}
	names, err := w.c.Access.Children(tools)
	if err != nil {
		return
	}
	sort.Strings(names)
	var folders []string
	for _, n := range names {
		p := filepath.Join(tools, n)
		if w.entry(p, true, true) {
			folders = append(folders, p)
		}
	}
	// First-level children of each folder entry: read only (a read refusal
	// is what breaks the update; modify noise down there is not worth the opens).
	for _, dir := range folders {
		kids, err := w.c.Access.Children(dir)
		if err != nil {
			continue
		}
		sort.Strings(kids)
		for _, n := range kids {
			w.entry(filepath.Join(dir, n), true, false)
		}
	}
}

func (w *aclWalk) others() {
	var paths []string
	if h := w.c.Home; h != "" {
		paths = append(paths, h)
		for _, n := range []string{"bin", "environments", "runtime", "node"} {
			paths = append(paths, filepath.Join(h, n))
		}
	}
	if co := w.c.Checkout; co != "" {
		paths = append(paths, co, filepath.Join(co, ".git"), filepath.Join(co, "node_modules"),
			filepath.Join(co, "apps", "desktop", "node_modules"), filepath.Join(co, "apps", "desktop", "release"))
	}
	for _, p := range paths {
		w.entry(p, false, true)
	}
}

// open spends one open; false = the cap or the budget is reached.
func (w *aclWalk) open() bool {
	if w.stop {
		return false
	}
	if w.ctx.Err() != nil || w.rep.Opens >= w.c.MaxOpens || w.c.now().Sub(w.start) > w.c.Budget {
		w.stop = true
		return false
	}
	w.rep.Opens++
	return true
}

// recheckStops opens every refused tools\ entry once more, after one shared
// pause, so a refusal that was only a passing state (delete pending, a scanner
// holding the file) is not reported. Only the entries refused again stay.
func (w *aclWalk) recheckStops() {
	if len(w.rep.Stop) == 0 || w.ctx.Err() != nil {
		return
	}
	sleep := w.c.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if err := sleep(w.ctx, defaultACLRecheck); err != nil {
		return // cancelled: the caller sees ctx.Err()
	}
	kept := w.rep.Stop[:0:0]
	for _, f := range w.rep.Stop {
		if w.ctx.Err() != nil {
			kept = append(kept, f) // no new open: keep what was seen
			continue
		}
		w.rep.Opens++
		if w.c.Access.Probe(f.Path, f.Mask).Denied {
			kept = append(kept, f)
		}
	}
	w.rep.Stop = kept
}

// entry probes one path (read, then modify when asked). It returns true when
// the entry exists, is not a link, and could be read: a folder worth listing.
func (w *aclWalk) entry(path string, inTools, modify bool) bool {
	key := strings.ToLower(filepath.Clean(path))
	if w.seen[key] {
		return false
	}
	w.seen[key] = true
	if !w.open() {
		return false
	}
	r := w.c.Access.Probe(path, platform.AccessRead)
	switch {
	case r.Missing, r.Reparse:
		return false
	case r.Denied:
		f := ACLFinding{Path: path, Mask: platform.AccessRead, InTools: inTools}
		if inTools {
			w.rep.Stop = append(w.rep.Stop, f)
		} else {
			w.rep.Warn = append(w.rep.Warn, f)
		}
		return false
	}
	readable := !r.Busy && r.Err == nil
	if modify && w.open() {
		m := w.c.Access.Probe(path, platform.AccessModify)
		if m.Denied {
			w.rep.Warn = append(w.rep.Warn, ACLFinding{Path: path, Mask: platform.AccessModify, InTools: inTools})
		}
	}
	return readable && r.Dir
}

// RepairCommands are the two commands the issue's reporters used to give a
// damaged entry back to the user (hermes-agent #126860): take ownership, then
// reset the ACL. They must run in an elevated terminal. /d answers the "do
// you want to continue?" question with the yes letter of the Windows
// language (Y; J on German).
func RepairCommands(path string) []string {
	return []string{
		`takeown /f "` + path + `" /r /d y`,
		`icacls "` + path + `" /reset /t /c /q`,
	}
}
