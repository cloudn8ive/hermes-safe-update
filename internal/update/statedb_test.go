package update

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/statedb"
)

const gib = int64(1) << 30

// fakeSD is a statedb.Service with scripted sizes.
type fakeSD struct {
	mu       sync.Mutex
	dbs      []statedb.DB
	backups  []statedb.BackupReq
	backupFn func(statedb.BackupReq) (statedb.BackupResult, error)
	ignored  []string
	// work scripts the compaction probe per profile; a profile not listed
	// has work (the old behaviour: offered).
	work   map[string]statedb.Compaction
	probed []string
}

func (f *fakeSD) CompactionWork(_ context.Context, path string) statedb.Compaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probed = append(f.probed, path)
	for _, d := range f.dbs {
		if d.Path == path {
			if c, ok := f.work[d.Profile]; ok {
				return c
			}
		}
	}
	return statedb.Compaction{Verdict: statedb.Work, Reason: "scripted"}
}

func (f *fakeSD) Find(string) []statedb.DB {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statedb.DB(nil), f.dbs...)
}

func (f *fakeSD) Ignored(string) []string { return f.ignored }

func (f *fakeSD) Backup(_ context.Context, req statedb.BackupReq) (statedb.BackupResult, error) {
	f.mu.Lock()
	f.backups = append(f.backups, req)
	fn := f.backupFn
	f.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return statedb.BackupResult{Path: req.Dir + `\` + req.Src.Profile + "-" + req.Stamp + ".db", Size: req.Src.Size / 2}, nil
}

func (f *fakeSD) resize(profile string, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.dbs {
		if f.dbs[i].Profile == profile {
			f.dbs[i].Size = size
		}
	}
}

func (f *fakeSD) backedUp() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, b := range f.backups {
		out = append(out, b.Src.Profile)
	}
	return out
}

const (
	v0231 = "Hermes Agent v0.23.1 (2026.11.5)\nInstall directory: X\nInstall method: git\n"
	v0227 = "Hermes Agent v0.22.7 (2026.11.1)\nInstall directory: X\nInstall method: git\n"
)

var optHelp = execx.Result{Output: "usage: hermes sessions optimize-storage [-h] [--yes]\n"}

// sdHarness: attended console, Hermes version v, two big databases.
func sdHarness(t *testing.T, version string, noCommand ...bool) (*harness, *fakeSD) {
	t.Helper()
	h := newHarness(t)
	h.con.Terminal = true
	h.answer(execx.Result{Output: version}, "--version")
	if len(noCommand) > 0 && noCommand[0] {
		h.answer(execx.Result{Code: 2, Output: "invalid choice"}, "sessions", "optimize-storage", "--help")
	} else {
		h.answer(optHelp, "sessions", "optimize-storage", "--help")
	}
	sd := &fakeSD{dbs: []statedb.DB{
		{Profile: "default", Path: tHome + `\state.db`, Size: gib + gib/4},
		{Profile: "work", Path: tHome + `\profiles\work\state.db`, Size: 4*gib + gib/2},
	}}
	h.sd = sd
	h.gw.onStop = func() { h.procs.Remove(200) } // a stopped gateway is gone from the process table
	return h, sd
}

func (h *harness) optimizeCalls() [][]string {
	var out [][]string
	for _, c := range h.runner.Calls() {
		if len(c.Argv) > 3 && c.Argv[len(c.Argv)-3] == "sessions" && c.Argv[len(c.Argv)-2] == "optimize-storage" && c.Argv[len(c.Argv)-1] == "--yes" {
			out = append(out, c.Argv[1:])
		}
	}
	return out
}

func joined(a [][]string) string {
	var p []string
	for _, x := range a {
		p = append(p, strings.Join(x, " "))
	}
	return strings.Join(p, " | ")
}

// compactOK scripts optimize-storage: both databases shrink.
func (h *harness) compactOK(sd *fakeSD) {
	h.runner.OnFunc([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		sd.resize("default", gib/2)
		return execx.Result{Output: "Optimizing search-index storage…\n✓ Search index optimized."}
	})
	h.runner.OnFunc([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		sd.resize("work", 2*gib)
		return execx.Result{Output: "Optimizing search-index storage…\r 40%\r100%\n✓ Search index optimized."}
	})
}

// ---- version gate ----

func TestStateDBCompactionGatePerVersion(t *testing.T) {
	cases := []struct {
		name, version, help string
		offered             bool
	}{
		{"0.22.7", v0227, "x optimize-storage", false},
		{"0.23.0", "Hermes Agent v0.23.0 (2026.11.1)\n", "x optimize-storage", true},
		{"0.23.1", v0231, "x optimize-storage", true},
		{"derived from 0.22.7", "Hermes Agent v0.22.7+300.g1234567 (2026.11.1)\n", "x optimize-storage", false},
		{"unknown version", "no version here", "x optimize-storage", false},
		{"0.23 without the command", v0231, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, sd := sdHarness(t, c.version, c.help == "")
			h.compactOK(sd)
			h.con.Keys = []platform.Key{key('y'), key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			got := len(h.optimizeCalls()) > 0
			if got != c.offered {
				t.Errorf("compaction ran = %v, want %v (%s)\n%s", got, c.offered, joined(h.optimizeCalls()), h.output())
			}
			if h.updates != 1 {
				t.Errorf("updates = %d: the update must run whatever happens to the compaction", h.updates)
			}
			if !c.offered {
				// no question, an own copy of both
				if strings.Contains(h.output(), "Compact the session database") {
					t.Errorf("a question was asked\n%s", h.output())
				}
				if len(sd.backedUp()) != 2 {
					t.Errorf("backups = %v", sd.backedUp())
				}
			}
		})
	}
}

func TestStateDBOldHermesRowSaysSo(t *testing.T) {
	h, sd := sdHarness(t, v0227)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("state.db", "Safe Update kept its own verified copy", "compaction needs Hermes 0.23 or newer; this is 0.22.7", "work 4.5 GB, default 1.2 GB")
	if got := sd.backedUp(); len(got) != 2 || got[0] != "work" {
		t.Errorf("backups = %v (largest first)", got)
	}
}

// ---- compaction ----

func TestStateDBCompactionHappyPath(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	var run *Run
	h.onRun = func(r *Run) { run = r }
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if !run.Facts.GateStopped {
		t.Error("the gateway was stopped for the compaction: the run must know (verify and finally restart it by that fact)")
	}
	got := joined(h.optimizeCalls())
	if got != "sessions optimize-storage --yes | -p work sessions optimize-storage --yes" {
		t.Errorf("optimize calls (smallest first, default without -p) = %q", got)
	}
	for _, c := range h.runner.Calls() {
		for _, a := range c.Argv {
			if a == "--force" {
				t.Errorf("--force was passed: %v", c.Argv)
			}
		}
	}
	if h.gw.fullStops != 1 {
		t.Errorf("gateway stops = %d, want 1 (for the compaction)", h.gw.fullStops)
	}
	if h.gw.starts != 1 {
		t.Errorf("gateway starts = %d, want exactly one restart after the update", h.gw.starts)
	}
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
	// the disk allows a copy first for both: one each, made before that database's compaction
	// (TestStateDBCopyComesBeforeEachCompaction); none afterwards: work is still over
	// 1 GiB at 2.0 GB, but its pre-compaction copy is the newest good one and is kept
	if b := sd.backedUp(); len(b) != 2 || b[0] != "default" || b[1] != "work" {
		t.Errorf("backups = %v, want default then work, once each", b)
	}
	h.mustContain("db compaction", "work 4.5 GB -> 2.0 GB", "default 1.2 GB -> 512.0 MB", "freed")
	// the compaction ran before the update, with the stdin-less, bounded runner
	order := ""
	for _, c := range h.runner.Calls() {
		switch {
		case len(c.Argv) > 3 && c.Argv[len(c.Argv)-2] == "optimize-storage" && c.Argv[len(c.Argv)-1] == "--yes":
			order += "O"
			if c.Timeout <= 0 {
				t.Errorf("compaction without a timeout: %+v", c)
			}
		case len(c.Argv) > 1 && c.Argv[1] == "update" && c.Argv[2] == "--yes":
			order += "U"
		}
	}
	if order != "OOU" {
		t.Errorf("order = %q, want compactions then the update", order)
	}
}

func TestStateDBDeclinedKeepsOwnCopy(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	h.con.Keys = []platform.Key{key('y'), key('n')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(h.optimizeCalls()) != 0 || h.gw.fullStops != 0 {
		t.Errorf("declined, yet: %s, gateway stops %d", joined(h.optimizeCalls()), h.gw.fullStops)
	}
	if len(sd.backedUp()) != 2 || h.updates != 1 {
		t.Errorf("backups = %v updates = %d", sd.backedUp(), h.updates)
	}
}

func TestStateDBHolderRefusalIsReportedAndUpdateGoesOn(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	refusal := "Refusing `hermes sessions optimize-storage`: another process is using C:\\x\\state.db.\n  pid 4242 python.exe -m hermes_cli.main gateway run"
	h.runner.OnPrefix([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, execx.Result{Code: 1, Output: refusal})
	h.runner.OnPrefix([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, execx.Result{Code: 1, Output: refusal})
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("Hermes refused to compact", "another process is using", "pid 4242")
	if h.updates != 1 || len(sd.backedUp()) != 2 {
		t.Errorf("updates = %d backups = %v", h.updates, sd.backedUp())
	}
	h.mustContain("RESULT: OK")
}

func TestStateDBNotEnoughDiskIsSkipped(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	// 14 GB free: the 1.25 GiB one fits above the 10 GB floor, the 4.5 GiB one does not
	h.plat.Disk.(*platform.FakeDisk).FreeBytes = 14e9
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if got := joined(h.optimizeCalls()); got != "sessions optimize-storage --yes" {
		t.Errorf("optimize calls = %q", got)
	}
	h.mustContain("compaction of work skipped: only")
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
}

func TestStateDBHermesOwnDiskRefusalAndAlreadyCompactAndFailureAndTimeout(t *testing.T) {
	for name, c := range map[string]struct {
		res  execx.Result
		want string
	}{
		"hermes disk": {execx.Result{Code: 1, Output: "⚠ Not enough free disk to complete safely."}, "Hermes says there is not enough free disk"},
		"compact":     {execx.Result{Output: "Search index is already on the compact layout — nothing to do."}, "already compact"},
		"failure":     {execx.Result{Code: 1, Output: "Error: optimization failed: boom\nNo data was lost. Re-run to resume."}, "failed (exit 1); no data was lost"},
		"timeout":     {execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "Optimizing search-index storage…"}, "too slow"},
	} {
		t.Run(name, func(t *testing.T) {
			h, sd := sdHarness(t, v0231)
			h.runner.OnPrefix([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, c.res)
			h.runner.OnPrefix([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, c.res)
			h.con.Keys = []platform.Key{key('y'), key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			if h.updates != 1 || len(sd.backedUp()) != 2 {
				t.Errorf("updates = %d backups = %v", h.updates, sd.backedUp())
			}
			h.mustContain("RESULT: OK", "db compaction", c.want)
		})
	}
}

func TestStateDBCtrlCDuringCompactionEndsTheRunWithoutTheUpdate(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	h.runner.OnFunc([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		cancel()
		return execx.Result{Code: 130, Output: "Interrupted — no data was lost; re-run to resume."}
	})
	h.con.Keys = []platform.Key{key('y'), key('y')}
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 0 || len(sd.backedUp()) != 1 {
		t.Errorf("updates = %d backups = %v (the copy made before the compaction stays)", h.updates, sd.backedUp())
	}
	if h.gw.starts != 1 {
		t.Errorf("the gateway must be restarted after the abort: starts = %d", h.gw.starts)
	}
	h.mustContain("No data was lost")
}

func TestStateDBGatewayStopFailureSkipsOnlyTheCompaction(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	h.gw.stopErr = errors.New("stuck")
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(h.optimizeCalls()) != 0 || h.updates != 1 || len(sd.backedUp()) != 2 {
		t.Errorf("optimize %s updates %d backups %v", joined(h.optimizeCalls()), h.updates, sd.backedUp())
	}
}

func TestStateDBUnattendedYesAndOffNeverCompactButCopy(t *testing.T) {
	for _, mode := range []string{"unattended", "yes", "off"} {
		t.Run(mode, func(t *testing.T) {
			h, sd := sdHarness(t, v0231)
			h.compactOK(sd)
			h.con.Keys = []platform.Key{key('y')}
			var flags []string
			if mode == "off" {
				h.cfg.StateDB.Compact = config.StateDBCompactOff
			} else {
				flags = []string{mode}
			}
			h.sessions.script = nil
			if err := h.run(flags...); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			if len(h.optimizeCalls()) != 0 || h.gw.fullStops != 0 {
				t.Errorf("compaction ran: %s stops %d", joined(h.optimizeCalls()), h.gw.fullStops)
			}
			if len(sd.backedUp()) != 2 {
				t.Errorf("a copy is a backup, not a change: backups = %v", sd.backedUp())
			}
			if h.updates != 1 {
				t.Errorf("updates = %d", h.updates)
			}
		})
	}
}

// ---- own copy ----

func TestStateDBBackupFailuresAreRowsNotStops(t *testing.T) {
	cases := map[string]func(statedb.BackupReq) (statedb.BackupResult, error){
		"disk": func(statedb.BackupReq) (statedb.BackupResult, error) {
			return statedb.BackupResult{Skipped: "only 3.0 GB free; a copy needs 4.5 GB and 10.0 GB must stay free"}, nil
		},
		"quickcheck": func(statedb.BackupReq) (statedb.BackupResult, error) {
			return statedb.BackupResult{}, errors.New("the copy did not pass its check (quick_check: bad page)")
		},
		"other error": func(statedb.BackupReq) (statedb.BackupResult, error) {
			return statedb.BackupResult{}, errors.New("disk I/O error")
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			h, sd := sdHarness(t, v0227)
			sd.backupFn = fn
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			h.mustContain("no copy of work's state.db", "NOT made")
			if h.updates != 1 {
				t.Errorf("updates = %d: a failed copy must not stop the update", h.updates)
			}
		})
	}
}

func TestStateDBBackupOffMakesNoCopyAndSaysSo(t *testing.T) {
	h, sd := sdHarness(t, v0227)
	h.cfg.StateDB.Backup = config.StateDBBackupOff
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(sd.backedUp()) != 0 {
		t.Errorf("backups = %v", sd.backedUp())
	}
	h.mustContain("statedb.backup is off")
}

func TestStateDBBackupRequestCarriesTheFloorAndTheDir(t *testing.T) {
	h, sd := sdHarness(t, v0227)
	h.cfg.StateDB.MinFreeGB = 12
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	b := sd.backups[0]
	if b.MinFree != 12e9 || !strings.HasSuffix(b.Dir, `backups\safe-update-statedb`) && !strings.HasSuffix(b.Dir, `backups/safe-update-statedb`) {
		t.Errorf("request = %+v", b)
	}
	if b.Stamp != "20261001-100000" && len(b.Stamp) != 15 {
		t.Errorf("stamp = %q", b.Stamp)
	}
}

func TestStateDBNothingOverTheLimitDoesNothing(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	sd.dbs[0].Size, sd.dbs[1].Size = gib/2, gib
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(sd.backedUp()) != 0 || len(h.optimizeCalls()) != 0 || strings.Contains(h.output(), "state.db") {
		t.Errorf("something happened\n%s", h.output())
	}
}

func TestStateDBHermesSkipLinesWithoutACopyGetARow(t *testing.T) {
	h, sd := sdHarness(t, v0227)
	h.cfg.StateDB.Backup = config.StateDBBackupOff
	_ = sd
	h.onUpdate(0, "→ Fetching updates\n  ⚠ Snapshot: skipping state.db (1.2 GB exceeds 1.0 GB limit)\n  ⚠ Skipping snapshot prune: DB file(s) skipped for size: state.db\nUpdate complete! (v1.0 → v1.1)", true)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("db snapshot", "Hermes skipped 1 file(s)", "holds 0 copy")
}

// ---- check ----

func TestStateDBCheckRowAndNothingChanged(t *testing.T) {
	for _, c := range []struct{ name, version, want string }{
		{"old", v0227, "Safe Update keeps its own copy when it updates (compaction needs Hermes 0.23 or newer; this is 0.22.7)"},
		{"new", v0231, "an attended update offers to compact it first"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, sd := sdHarness(t, c.version)
			h.mode = ModeCheck
			if err := h.run(); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			h.mustContain("state.db", "work 4.5 GB, default 1.2 GB over Hermes' 1 GiB snapshot limit", c.want)
			if len(sd.backedUp()) != 0 || len(h.optimizeCalls()) != 0 || h.gw.stops != 0 {
				t.Errorf("check changed something: %v %s %d", sd.backedUp(), joined(h.optimizeCalls()), h.gw.stops)
			}
		})
	}
}

func TestStateDBCheckUnattendedSaysNoCompaction(t *testing.T) {
	h, _ := sdHarness(t, v0231)
	h.mode = ModeCheck
	if err := h.run("yes"); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("an unattended or --yes run never compacts")
}

// An "up to date" verdict ends the flow before the stage: the sizes still show.
func TestStateDBRowShowsWhenHermesIsUpToDate(t *testing.T) {
	for _, mode := range []Mode{ModeCheck, ModeUpdate} {
		h, sd := sdHarness(t, v0231)
		h.mode = mode
		h.mainCheck = "Already up to date."
		h.con.Keys = []platform.Key{key('n')} // the repair offer of an attended up-to-date screen
		h.cfg.Tunables.ConfirmTimeoutS = 3
		if err := h.run(); err != nil {
			t.Fatalf("mode %v: err = %v\n%s", mode, err, h.output())
		}
		h.mustContain("Hermes is up to date", "work 4.5 GB, default 1.2 GB over Hermes' 1 GiB snapshot limit")
		if len(sd.backedUp()) != 0 || len(h.optimizeCalls()) != 0 || h.updates != 0 {
			t.Errorf("mode %v: nothing is changed when there is nothing to update: %v %s %d", mode, sd.backedUp(), joined(h.optimizeCalls()), h.updates)
		}
	}
}

func TestStateDBCtrlCDuringTheCopyEndsTheRunWithoutTheUpdate(t *testing.T) {
	h, sd := sdHarness(t, v0227)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	sd.backupFn = func(statedb.BackupReq) (statedb.BackupResult, error) {
		cancel()
		return statedb.BackupResult{}, context.Canceled
	}
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 0 {
		t.Errorf("updates = %d", h.updates)
	}
	h.mustContain("Ctrl+C during the copy")
}

// ---- the compaction probe (v0.9.1) ----

var compactNoWork = statedb.Compaction{Verdict: statedb.NoWork, Reason: "search index already on the compact layout"}

func TestStateDBCompactIndexAsksNothingStopsNothingAndKeepsOwnCopy(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	sd.work = map[string]statedb.Compaction{"default": compactNoWork, "work": compactNoWork}
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if strings.Contains(h.output(), "Compact the session database") {
		t.Errorf("a question was asked about databases that are already compact\n%s", h.output())
	}
	if len(h.optimizeCalls()) != 0 || h.gw.fullStops != 0 {
		t.Errorf("compacted %s, gateway stops %d", joined(h.optimizeCalls()), h.gw.fullStops)
	}
	if b := sd.backedUp(); len(b) != 2 {
		t.Errorf("backups = %v: the own copy is unchanged", b)
	}
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
	h.mustContain("search index already compact", "Safe Update kept its own verified copy")
}

func TestStateDBOnlyTheDatabaseWithWorkIsOfferedAndCompacted(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	sd.work = map[string]statedb.Compaction{"work": compactNoWork} // default has work
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if got := joined(h.optimizeCalls()); got != "sessions optimize-storage --yes" {
		t.Errorf("optimize calls = %q: only default has work", got)
	}
	h.mustContain("Compact the session database(s) before updating? default 1.2 GB", "work: search index already compact, left alone")
	// work is not compacted but still gets its own copy (it is over the limit)
	if b := sd.backedUp(); len(b) != 2 {
		t.Errorf("backups = %v", b)
	}
}

func TestStateDBUnknownIsOfferedAndSaysSo(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	sd.work = map[string]statedb.Compaction{
		"default": {Verdict: statedb.Unknown, Reason: "cannot read the schema: database is locked"},
		"work":    {Verdict: statedb.Unknown, Reason: "x"},
	}
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("Could not tell whether compaction helps for work, default")
	if got := joined(h.optimizeCalls()); got != "sessions optimize-storage --yes | -p work sessions optimize-storage --yes" {
		t.Errorf("optimize calls = %q: unknown keeps the old behaviour", got)
	}
}

func TestStateDBNoWorkIsNotTrustedForAnotherHermesLine(t *testing.T) {
	h, sd := sdHarness(t, "Hermes Agent v0.24.0 (2027.1.1)\nInstall directory: X\n")
	h.compactOK(sd)
	sd.work = map[string]statedb.Compaction{"default": compactNoWork, "work": compactNoWork}
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("Compact the session database", "Could not tell whether compaction helps")
	if len(h.optimizeCalls()) != 2 {
		t.Errorf("optimize calls = %s", joined(h.optimizeCalls()))
	}
}

func TestStateDBProbeOnlyLooksAtDatabasesOverTheLimit(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	sd.dbs[0].Size = gib / 2
	h.con.Keys = []platform.Key{key('y'), key('n')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(sd.probed) != 1 || !strings.Contains(sd.probed[0], "work") {
		t.Errorf("probed %v", sd.probed)
	}
}

func TestStateDBCheckRowSaysCompactForTheOnesThatAre(t *testing.T) {
	for _, c := range []struct {
		name string
		work map[string]statedb.Compaction
		want []string
		not  []string
	}{
		{"both compact", map[string]statedb.Compaction{"default": compactNoWork, "work": compactNoWork},
			[]string{"work 4.5 GB, default 1.2 GB over Hermes' 1 GiB snapshot limit (search index already compact; Safe Update keeps its own copy)"},
			[]string{"offers to compact"}},
		{"one compact", map[string]statedb.Compaction{"work": compactNoWork},
			[]string{"offers to compact default first (work: search index already compact; Safe Update keeps its own copy)"}, nil},
		{"unknown", map[string]statedb.Compaction{"default": {Verdict: statedb.Unknown, Reason: "x"}},
			[]string{"an attended update offers to compact it first", "could not tell whether compaction helps for default"}, nil},
		{"work", nil, []string{"an attended update offers to compact it first"}, []string{"could not tell", "already compact"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, sd := sdHarness(t, v0231)
			sd.work = c.work
			h.mode = ModeCheck
			if err := h.run(); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			h.mustContain(c.want...)
			for _, n := range c.not {
				if strings.Contains(h.output(), n) {
					t.Errorf("output has %q\n%s", n, h.output())
				}
			}
			if len(sd.backedUp()) != 0 || len(h.optimizeCalls()) != 0 || h.gw.stops != 0 {
				t.Errorf("check changed something")
			}
		})
	}
}

func TestStateDBAnUnprobedDatabaseIsUnknownNotCompact(t *testing.T) {
	r := &Run{}
	r.Facts.VersionBefore = "v0.23.1"
	if c := r.verdictOf(statedb.DB{Profile: "default"}); c.Verdict != statedb.Unknown {
		t.Errorf("verdict = %v", c.Verdict)
	}
}
