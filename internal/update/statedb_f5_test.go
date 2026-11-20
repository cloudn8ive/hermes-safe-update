package update

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/statedb"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

func optimizeArgv(argv []string) bool {
	return len(argv) > 3 && argv[len(argv)-2] == "optimize-storage" && argv[len(argv)-1] == "--yes"
}

// optProbe wraps the fake runner: it records the compaction's Cmd and what
// the context looked like when it started, and can cancel the run there.
type optProbe struct {
	*execx.Fake
	cancel     func()
	cmds       []execx.Cmd
	ctxErr     []error // ctx.Err() of the compaction call after cancel()
	streamFail error
}

func (p *optProbe) Stream(ctx context.Context, c execx.Cmd, on func(string)) (execx.Result, error) {
	if optimizeArgv(c.Argv) {
		p.cmds = append(p.cmds, c)
		if p.cancel != nil {
			p.cancel()
			time.Sleep(300 * time.Millisecond) // Hermes is still working: the heartbeat goroutine says so meanwhile
		}
		p.ctxErr = append(p.ctxErr, ctx.Err())
		if p.streamFail != nil {
			return execx.Result{Code: execx.CodeNotFound}, p.streamFail
		}
	}
	return p.Fake.Stream(ctx, c, on)
}

// ---- item 1: the copy comes first ----

func TestStateDBCopyComesBeforeEachCompaction(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	order := ""
	sd.backupFn = func(req statedb.BackupReq) (statedb.BackupResult, error) {
		order += "C" + req.Src.Profile[:1] + " "
		if req.HolderRuns {
			t.Errorf("Hermes is closed and the gateway stopped, yet HolderRuns is set for %s", req.Src.Profile)
		}
		if len(h.app.Calls()) == 0 {
			t.Error("the copy for a compaction was made before Hermes was closed")
		}
		return statedb.BackupResult{Path: req.Dir + `\` + req.Src.Profile + ".db", Size: req.Src.Size / 2}, nil
	}
	h.runner.OnFunc([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		order += "Ow "
		sd.resize("work", 2*gib)
		return execx.Result{Output: "✓ Search index optimized."}
	})
	h.runner.OnFunc([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		order += "Od "
		sd.resize("default", gib/2)
		return execx.Result{Output: "✓ Search index optimized."}
	})
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if order != "Cd Od Cw Ow " {
		t.Errorf("order = %q, want each database copied right before its own compaction and no copy after", order)
	}
	h.mustContain("a verified copy first, then the compaction", "Safe Update kept its own verified copy: default") // work is still > 1 GiB: its pre-compaction copy is the row
}

// Free space for the compaction (twice the file) but not for a copy first:
// the question, the log and the row say "no copy before compaction", and the
// copy is made afterwards (work stays over 1 GiB).
func TestStateDBNoCopyBeforeCompactionWhenTheDiskAllowsOnlyTheCompaction(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.plat.Disk.(*platform.FakeDisk).FreeBytes = 22e9 // default: copy first fits; work: peak 9.7 GB + floor 10 GB fits, the extra 4.9 GB copy does not
	order := ""
	sd.backupFn = func(req statedb.BackupReq) (statedb.BackupResult, error) {
		order += "C" + req.Src.Profile[:1] + " "
		return statedb.BackupResult{Path: req.Dir + `\` + req.Src.Profile + ".db", Size: req.Src.Size / 2}, nil
	}
	h.runner.OnFunc([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		order += "Ow "
		sd.resize("work", 2*gib)
		return execx.Result{Output: "✓ Search index optimized."}
	})
	h.runner.OnFunc([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		order += "Od "
		sd.resize("default", gib/2)
		return execx.Result{Output: "✓ Search index optimized."}
	})
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if order != "Cd Od Ow Cw " {
		t.Errorf("order = %q, want default copied first; work compacted with no copy before and copied after", order)
	}
	h.mustContain("work: no copy before compaction", "default: a verified copy first")
	h.mustContain("no copy before compaction: only") // the card row says it too
	h.mustContain("work 4.5 GB -> 2.0 GB (no copy before compaction: only")
}

func TestStateDBNoCopyBeforeCompactionWhenBackupIsOff(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	h.cfg.StateDB.Backup = config.StateDBBackupOff
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(sd.backedUp()) != 0 || len(h.optimizeCalls()) != 2 {
		t.Errorf("backups = %v optimize = %s", sd.backedUp(), joined(h.optimizeCalls()))
	}
	h.mustContain("no copy before compaction (statedb.backup is off)")
}

// A copy that was promised before the compaction and failed leaves nothing to
// fall back on: that database is not compacted, the update goes on.
func TestStateDBFailedCopyFirstMeansNoCompactionOfThatDatabase(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	sd.backupFn = func(req statedb.BackupReq) (statedb.BackupResult, error) {
		if req.Src.Profile == "default" {
			return statedb.BackupResult{}, errors.New("quick_check failed")
		}
		return statedb.BackupResult{Path: req.Dir + `\x.db`, Size: 1}, nil
	}
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if got := joined(h.optimizeCalls()); got != "-p work sessions optimize-storage --yes" {
		t.Errorf("optimize calls = %q, want only work", got)
	}
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
	h.mustContain("no verified copy to fall back on")
}

// ---- item 2: the compaction's disk peak ----

func TestPlanCompactionDiskBoundaries(t *testing.T) {
	cfg := &config.Config{File: config.Defaults()} // floor 10 GB, backup on
	r := &Run{Deps: Deps{Config: cfg}}
	d := statedb.DB{Profile: "p", Size: 3e9, WAL: 5e6}
	floor, peak, cp := uint64(10e9), uint64(6e9), d.CopyNeed()
	cases := []struct {
		name      string
		free      uint64
		compact   bool
		copyFirst bool
	}{
		{"one byte short of peak+floor", floor + peak - 1, false, false},
		{"exactly peak+floor", floor + peak, true, false},
		{"one byte short of peak+copy+floor", floor + peak + cp - 1, true, false},
		{"exactly peak+copy+floor", floor + peak + cp, true, true},
	}
	for _, c := range cases {
		p := r.planCompaction(d, c.free)
		if p.Compact != c.compact || p.CopyFirst != c.copyFirst {
			t.Errorf("%s: %+v, want compact=%v copyFirst=%v", c.name, p, c.compact, c.copyFirst)
		}
	}
	// Hermes' one-file preflight would have allowed this
	if p := r.planCompaction(d, floor+3e9+1); p.Compact {
		t.Error("free = floor + one file: Hermes' own preflight passes it, but the peak is twice the file")
	}
	// backup off: the copy is not part of the need
	cfg.StateDB.Backup = config.StateDBBackupOff
	if p := r.planCompaction(d, floor+peak); !p.Compact || p.CopyFirst {
		t.Errorf("backup off: %+v", p)
	}
}

func TestStateDBLogsTheDiskEstimate(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	var logBuf bytes.Buffer
	h.logSink = &logBuf
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if l := logBuf.String(); !strings.Contains(l, "compaction disk estimate") || !strings.Contains(l, "peak=") {
		t.Errorf("no estimate in the log:\n%s", l)
	}
}

// ---- item 3: a skipped VACUUM is not a success ----

func TestStateDBSkippedVacuumIsNotReportedAsSuccess(t *testing.T) {
	for name, line := range map[string]string{
		"in use": "  (VACUUM skipped: another process is using the database. Re-run `hermes sessions optimize` with it closed to reclaim freed space.)",
		"failed": "  (VACUUM was skipped or failed — run `hermes sessions optimize` later to reclaim freed space.)",
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := sdHarness(t, v0231)
			out := execx.Result{Output: "✓ Search index optimized.\n  Database size: 1200.0 MB → 1199.0 MB\n" + line}
			h.runner.OnPrefix([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, out)
			h.runner.OnPrefix([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, out)
			cards := &[]ui.Card{}
			h.cards = cards
			h.con.Keys = []platform.Key{key('y'), key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			h.mustContain("the VACUUM did not run, so no space was returned", "Hermes skipped the VACUUM")
			found := false
			for _, c := range *cards {
				for _, r := range c.Rows {
					if r.Key == "db compaction" {
						found = true
						if r.Verdict == nil || *r.Verdict != ui.VerdictAttention || !strings.Contains(r.Value, "no space was returned") {
							t.Errorf("row = %+v, want attention and the reason", r)
						}
					}
				}
			}
			if !found {
				t.Errorf("no db compaction row in %+v", *cards)
			}
			if h.updates != 1 {
				t.Errorf("updates = %d", h.updates)
			}
		})
	}
}

// ---- item 4 and 7: the copy before close, with the gateway up ----

func TestStateDBCopyBeforeCloseOnNoUnattendedOldVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		keys    []platform.Key
		flags   []string
	}{
		{"N", v0231, []platform.Key{key('y'), key('n')}, nil},
		{"unattended", v0231, nil, []string{"unattended"}},
		{"yes", v0231, []platform.Key{key('y')}, []string{"yes"}},
		{"old Hermes", v0227, []platform.Key{key('y')}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, sd := sdHarness(t, c.version)
			h.compactOK(sd)
			h.sessions.script = nil
			h.con.Keys = c.keys
			sd.backupFn = func(req statedb.BackupReq) (statedb.BackupResult, error) {
				if n := len(h.app.Calls()); n != 0 {
					t.Errorf("%s copied after Hermes was closed (app calls %v): downtime grows by the copy", req.Src.Profile, h.app.Calls())
				}
				if !req.HolderRuns {
					t.Errorf("%s: Hermes is still running, the copy must be told so (non-WAL guard)", req.Src.Profile)
				}
				return statedb.BackupResult{Path: req.Dir + `\x.db`, Size: 1}, nil
			}
			if err := h.run(c.flags...); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			if len(sd.backedUp()) != 2 || h.updates != 1 {
				t.Errorf("backups = %v updates = %d", sd.backedUp(), h.updates)
			}
		})
	}
}

func TestStateDBNonWALCopySkippedRowAndTheUpdateGoesOn(t *testing.T) {
	h, sd := sdHarness(t, v0227)
	sd.backupFn = func(req statedb.BackupReq) (statedb.BackupResult, error) {
		return statedb.BackupResult{Skipped: "the database is open in another process and its journal mode is delete, not wal: a copy would block Hermes' writes while it runs"}, nil
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("journal mode is delete", "RESULT: OK")
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
}

// ---- item 8: honest Ctrl+C wording ----

func TestStateDBQuestionDoesNotPromiseAnInstantStop(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	h.con.Keys = []platform.Key{key('y'), key('n')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	out := h.output()
	if strings.Contains(out, "with no data lost") || strings.Contains(out, "stops it with") || strings.Contains(out, "nothing is lost") {
		t.Errorf("the old promise is back:\n%s", out)
	}
	h.mustContain("cannot be interrupted", "skipping in 2 min")
}

func TestStateDBCtrlCDuringCompactionPinsTheCommand(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	probe := &optProbe{Fake: h.runner, cancel: cancel}
	h.wrap = probe
	h.runner.OnFunc([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, func(execx.Cmd) execx.Result {
		return execx.Result{Code: 130, Output: "Interrupted — no data was lost; re-run to resume."}
	})
	h.con.Keys = []platform.Key{key('y'), key('y')}
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(probe.cmds) != 1 {
		t.Fatalf("compaction calls = %d", len(probe.cmds))
	}
	c := probe.cmds[0]
	if probe.ctxErr[0] != nil {
		t.Error("the compaction got the cancellable context: a Ctrl+C would hard-kill Hermes in the middle of a VACUUM (must be context.WithoutCancel)")
	}
	if !c.ShowWindow || c.KillTree == nil || c.Timeout <= 0 {
		t.Errorf("Cmd = ShowWindow %v KillTree-set %v Timeout %v", c.ShowWindow, c.KillTree != nil, c.Timeout)
	}
	if len(sd.backedUp()) != 1 {
		t.Errorf("backups = %v", sd.backedUp())
	}
	h.mustContain("Ctrl+C during the compaction of default", "waiting for Hermes to finish its current step (VACUUM cannot be interrupted)")
}

func TestStateDBTimeoutPinsTheCommandAndSaysWhatResumes(t *testing.T) {
	h, _ := sdHarness(t, v0231)
	probe := &optProbe{Fake: h.runner}
	h.wrap = probe
	res := execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "Optimizing search-index storage…"}
	h.runner.OnPrefix([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, res)
	h.runner.OnPrefix([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, res)
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	for _, c := range probe.cmds {
		if !c.ShowWindow || c.KillTree == nil || c.Timeout <= 0 {
			t.Errorf("Cmd = %+v", c)
		}
	}
	h.mustContain("the index work resumes where it stopped", "a VACUUM that was killed starts over")
}

// ---- item 9 ----

func TestStateDBHolderRefusalStopsAndRestartsTheGatewayOnce(t *testing.T) {
	h, _ := sdHarness(t, v0231)
	refusal := "Refusing `hermes sessions optimize-storage`: another process is using C:\\x\\state.db.\n  pid 4242 python.exe"
	h.runner.OnPrefix([]string{tLauncher, "sessions", "optimize-storage", "--yes"}, execx.Result{Code: 1, Output: refusal})
	h.runner.OnPrefix([]string{tLauncher, "-p", "work", "sessions", "optimize-storage", "--yes"}, execx.Result{Code: 1, Output: refusal})
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.gw.fullStops != 1 || h.gw.starts != 1 {
		t.Errorf("gateway stops = %d starts = %d, want one each", h.gw.fullStops, h.gw.starts)
	}
}

func TestStateDBGatewayStopFailureRestartsTheGatewayAndSaysSoOnTheCard(t *testing.T) {
	h, _ := sdHarness(t, v0231)
	h.gw.stopErr = errors.New("stuck")
	cards := &[]ui.Card{}
	h.cards = cards
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.gw.starts < 1 {
		t.Errorf("the gateway was not restarted after the failed stop: starts = %d", h.gw.starts)
	}
	found := false
	for _, c := range *cards {
		for _, r := range c.Rows {
			if r.Key == "db compaction" && strings.Contains(r.Value, "the gateway could not be stopped") && r.Verdict != nil && *r.Verdict == ui.VerdictAttention {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no attention row about the gateway stop failure in %+v", *cards)
	}
}

// ---- item 10 ----

func TestStateDBLegacyProfilesDefaultIsNotCompactedAsTheRoot(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	h.compactOK(sd)
	sd.dbs = []statedb.DB{{Profile: statedb.LegacyDefaultProfile, Path: tHome + `\profiles\default\state.db`, Size: 2 * gib}}
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if len(h.optimizeCalls()) != 0 {
		t.Errorf("optimize ran for profiles/default: %s (it would have compacted the root's database)", joined(h.optimizeCalls()))
	}
	h.mustContain("profiles/default", "cannot be named to Hermes with -p")
}

// ---- item 12 ----

func TestStateDBIgnoredProfileFoldersAreLogged(t *testing.T) {
	h, sd := sdHarness(t, v0231)
	sd.ignored = []string{"Bad Name"}
	var logBuf bytes.Buffer
	h.logSink = &logBuf
	h.mode = ModeCheck
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logBuf.String(), "profile folder ignored") || !strings.Contains(logBuf.String(), "Bad Name") {
		t.Errorf("log:\n%s", logBuf.String())
	}
}

// ---- item 13 ----

func TestStateDBLaunchFailureIsNotCtrlC(t *testing.T) {
	h, _ := sdHarness(t, v0231)
	probe := &optProbe{Fake: h.runner, streamFail: errors.New("cannot start the process")}
	h.wrap = probe
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("a launch failure ended the run: %v\n%s", err, h.output())
	}
	h.mustContain("could not be run: cannot start the process", "RESULT: OK")
	if strings.Contains(h.output(), "Ctrl+C during the compaction") {
		t.Error("a launch failure was reported as Ctrl+C")
	}
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
}

func TestStateDBNoLauncherNoCompactionOffer(t *testing.T) {
	r := &Run{Deps: Deps{Config: &config.Config{File: config.Defaults()}, Runner: &execx.Fake{}}}
	r.Facts.VersionBefore = "v0.23.1"
	if why := r.compactWhyNot(context.Background(), false); !strings.Contains(why, "launcher") {
		t.Errorf("why = %q, want the missing launcher", why)
	}
}

// ---- item 11 ----

func TestStateDBQuestionTimeoutIsShorterThanTheConfirmTimeout(t *testing.T) {
	d := config.Defaults()
	if compactAskTimeoutS >= d.Tunables.ConfirmTimeoutS || compactAskTimeoutS != 120 {
		t.Errorf("compactAskTimeoutS = %d, confirm = %d", compactAskTimeoutS, d.Tunables.ConfirmTimeoutS)
	}
}
