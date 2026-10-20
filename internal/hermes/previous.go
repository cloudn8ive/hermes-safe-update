package hermes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Signals that the previous update did not finish. A run may carry several;
// they are kept in the order they were found.
const (
	SignalReceiptRunning     = "receipt-running"     // Hermes' receipt says running, its owner is gone
	SignalReceiptInterrupted = "receipt-interrupted" // Hermes' receipt says interrupted
	SignalOwnLog             = "own-log"             // Safe Update's own log: reached hermes update, never finished
	SignalStaleMarker        = "stale-marker"        // the update marker names a dead owner
)

// PreviousUpdate is an update that stopped before it finished. At and Stage
// are what is known about the stop ("" / zero when the signal does not say).
type PreviousUpdate struct {
	Signals []string
	At      time.Time // when it stopped (best known)
	Stage   string    // plain words: what it was doing ("packaging the desktop app")
	// StepKey names the update step it was in, by the keys of the update
	// steps (timings.UpdateSteps; "build" and "tail" stand for several
	// steps). The caller words it, so every signal reads the same. Set by the
	// receipt, which knows no words; Stage wins when both are set.
	StepKey string
	// Keys identify this unfinished update across runs ("receipt:<update id>",
	// "log:<start of the run>"), so a run can tell whether an earlier run
	// already tried to finish it. A stale marker has none.
	Keys []string
}

// Has reports whether the signal was among the ones found.
func (p *PreviousUpdate) Has(signal string) bool {
	for _, s := range p.Signals {
		if s == signal {
			return true
		}
	}
	return false
}

// maxReceiptBytes bounds what is read of a receipt (real ones are a few KB).
const maxReceiptBytes = 4 << 20

// ReceiptFile is one entry of the receipt folder.
type ReceiptFile struct {
	Name    string
	ModTime time.Time
}

// ReceiptReader looks at Hermes' newest update receipt and says whether it
// records an update that did not finish. Read-only; it writes nothing and
// never starts a process.
type ReceiptReader struct {
	Home  string
	Procs platform.Procs
	// Created reads a process's creation time (platform.ProcessCreated); nil
	// = unknown, so a recorded creation time is not compared.
	Created func(pid int) (time.Time, bool)
	// ReadFile defaults to a bounded os read.
	ReadFile func(path string) ([]byte, error)
	// ReadDir lists the receipt folder (default: os.ReadDir with the file times).
	ReadDir func(dir string) ([]ReceiptFile, error)
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxReceiptBytes {
		return nil, fmt.Errorf("receipt is larger than %d bytes", maxReceiptBytes)
	}
	return b, nil
}

func listReceipts(dir string) ([]ReceiptFile, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ReceiptFile
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			out = append(out, ReceiptFile{Name: e.Name(), ModTime: info.ModTime()})
		}
	}
	return out, nil
}

// RootHome is the root Hermes home of a home: Hermes keeps one set of update
// receipts for the whole install, in the root's logs/update_receipts
// (update_receipt.py _receipt_dir -> get_default_hermes_root). A profile's
// home is <root>/profiles/<name>; any other home is its own root. (Hermes
// also treats a home below the platform's default home as part of that
// default root; that case has no profiles segment and is not told apart
// here.)
func RootHome(home string) string {
	if home == "" {
		return ""
	}
	clean := filepath.Clean(home)
	parent := filepath.Dir(clean)
	if filepath.Base(parent) == ReceiptProfilesDir && parent != clean {
		return filepath.Dir(parent)
	}
	return clean
}

// Dir is the receipt folder: the root home's, whatever home Safe Update was
// pointed at (a profile's own folder has only the mirrored latest.json and
// the plugin manager's files, never the run files).
func (r ReceiptReader) Dir() string {
	return filepath.Join(append([]string{RootHome(r.Home)}, ReceiptDirParts...)...)
}

// Path is the copy of the newest receipt, latest.json.
func (r ReceiptReader) Path() string { return filepath.Join(r.Dir(), ReceiptLatestFile) }

// receipt is one update receipt that was read: the record and when its file
// was last written.
type receipt struct {
	rec UpdateReceipt
	mod time.Time
}

func (r ReceiptReader) parse(read func(string) ([]byte, error), path string) (UpdateReceipt, error) {
	var rec UpdateReceipt
	b, err := read(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(b), "\ufeff")), &rec); err != nil {
		return rec, fmt.Errorf("receipt is not readable: %w", err)
	}
	return rec, nil
}

// started is when the run began (its start, else the file's time): the order
// of the update receipts.
func (rc receipt) started() time.Time {
	if t, err := time.Parse(time.RFC3339, rc.rec.StartedAt); err == nil {
		return t
	}
	return rc.mod
}

// runs reads the update receipts there are: the last ReceiptKeep run files
// (update_*.json, the way reconcile_interrupted_runs looks), or latest.json
// alone for a folder with no run file. Hermes' latest.json is no guide to the
// newest update: pm sync and plugin-check receipts overwrite it
// (pm/receipt.py), so after any Hermes launch it no longer names an update.
// found=false: there is no receipt at all (an older Hermes). One unreadable
// file does not hide the others; err is only set when nothing could be read.
func (r ReceiptReader) runs() (out []receipt, found bool, err error) {
	read := r.ReadFile
	if read == nil {
		read = readBounded
	}
	list := r.ReadDir
	if list == nil {
		list = listReceipts
	}
	files, err := list(r.Dir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	var runs []ReceiptFile
	for _, f := range files {
		if ok, _ := path.Match(ReceiptRunGlob, f.Name); ok {
			runs = append(runs, f)
		}
	}
	if len(runs) == 0 {
		rec, err := r.parse(read, r.Path())
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, false, nil
			}
			return nil, false, err
		}
		return []receipt{{rec: rec}}, true, nil
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].ModTime.Before(runs[j].ModTime) })
	if len(runs) > ReceiptKeep {
		runs = runs[len(runs)-ReceiptKeep:]
	}
	var firstErr error
	for _, f := range runs {
		rec, err := r.parse(read, filepath.Join(r.Dir(), f.Name))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // one unreadable file does not hide the others
		}
		out = append(out, receipt{rec: rec, mod: f.ModTime})
	}
	if len(out) == 0 {
		return nil, false, firstErr
	}
	return out, true, nil
}

// newest finds the receipt of the newest update: the one that started last
// (ties: the one written last).
func (r ReceiptReader) newest() (rec UpdateReceipt, found bool, err error) {
	all, found, err := r.runs()
	if err != nil || !found {
		return rec, false, err
	}
	best := all[0]
	for _, c := range all[1:] {
		if !c.started().Before(best.started()) {
			best = c
		}
	}
	return best.rec, true, nil
}

// NewestFinished is when the newest update that reached a terminal outcome
// (success or partial) began; the zero time = none is known (no
// receipt, or the receipts cannot be read: err says which). A failed update
// does not count: it is unfinished work itself, and counting it would hide
// the stopped run it came after. A run of Safe
// Update that stopped inside `hermes update` is not unfinished any more when
// a finished update began after it got there (the user ran `hermes update`
// by hand, as Hermes tells them to).
func (r ReceiptReader) NewestFinished() (time.Time, error) {
	if r.Home == "" {
		return time.Time{}, nil
	}
	all, found, err := r.runs()
	if err != nil || !found {
		return time.Time{}, err
	}
	var best time.Time
	for _, c := range all {
		switch c.rec.Outcome {
		case ReceiptSuccess, ReceiptPartial:
			if t := c.started(); t.After(best) {
				best = t
			}
		}
	}
	return best, nil
}

// Unfinished returns the unfinished update the newest receipt records, or nil
// when it records none. An error means "could not tell" (unreadable or not a
// receipt): the caller treats that as unknown, which changes nothing. No
// receipt at all (a Hermes without receipts) is nil, nil.
func (r ReceiptReader) Unfinished() (*PreviousUpdate, error) {
	if r.Home == "" {
		return nil, nil
	}
	rec, found, err := r.newest()
	if err != nil || !found {
		return nil, err
	}
	switch rec.Outcome {
	case ReceiptRunning:
		if r.Procs == nil {
			return nil, errors.New("no process lookup to tell whether the receipt's owner is alive")
		}
		if r.ownerAlive(rec) {
			return nil, nil // an update is running right now: not an unfinished one
		}
		return rec.previous(SignalReceiptRunning), nil
	case ReceiptInterrupted:
		return rec.previous(SignalReceiptInterrupted), nil
	}
	return nil, nil
}

// ownerAlive is Hermes' _owner_alive: the run's pid, or the pid of the process
// that last wrote the record, is alive and (when a creation time was
// recorded and can be read here) the same incarnation. A pid without a
// recorded creation time counts as the owner while it lives, so a reused pid
// can only hide an unfinished update, never invent one.
func (r ReceiptReader) ownerAlive(rec UpdateReceipt) bool {
	m := &Marker{Procs: r.Procs, Created: r.Created}
	ids := map[int]*float64{}
	for _, p := range []struct {
		pid int
		ct  *float64
	}{{rec.PID, rec.PIDCreateTime}, {rec.WriterPID, rec.WriterCreateTime}} {
		if p.pid <= 0 {
			continue
		}
		if old, seen := ids[p.pid]; !seen || old == nil {
			ids[p.pid] = p.ct // a bare duplicate pid never overrides a recorded creation time
		}
	}
	for pid, ct := range ids {
		if m.identityLive(pid, ct) {
			return true
		}
	}
	return false
}

// previous turns a receipt into the answer: when it stopped (the end it
// recorded, else its last stage, else its start) and what it was doing.
func (rec UpdateReceipt) previous(signal string) *PreviousUpdate {
	p := &PreviousUpdate{Signals: []string{signal}}
	last := ""
	if n := len(rec.Stages); n > 0 {
		last = rec.Stages[n-1].Name
	}
	p.StepKey = ReceiptStopStep(last)
	if id := rec.UpdateID; id != "" {
		p.Keys = []string{"receipt:" + id}
	} else if rec.StartedAt != "" {
		p.Keys = []string{"receipt:" + rec.StartedAt}
	}
	for _, s := range []string{rec.FinishedAt, lastStageAt(rec), rec.StartedAt} {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.At = t
			break
		}
	}
	return p
}

func lastStageAt(rec UpdateReceipt) string {
	if n := len(rec.Stages); n > 0 {
		return rec.Stages[n-1].At
	}
	return ""
}
