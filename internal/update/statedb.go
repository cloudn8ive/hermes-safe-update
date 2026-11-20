package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/statedb"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// Session databases over Hermes' 1 GiB snapshot limit (v0.9.0).
//
// `hermes update` leaves a state.db over 1 GiB out of its pre-update
// snapshot, so such an install is updated with no copy of its session
// history. Two stages deal with it:
//
// statedb_pre, before "close" (Hermes and its gateway still run):
//
//  1. probe: size of every profile's state.db (the check does only this);
//  2. the one question (attended, Hermes >= 0.23, compaction on): compact?
//  3. when the answer is not Y (N, timeout, unattended, --yes, Hermes older
//     than 0.23, compact off): Safe Update's own verified copy of every
//     database over the limit, NOW. The copy is a read transaction, so with
//     the gateway up it must not block anything: in a journal mode other than
//     WAL the copy is skipped while Hermes has the database open. Doing it
//     here keeps the copy out of the time Hermes is closed.
//
// statedb, after "close" (only after Y):
//
//  4. the gateway is stopped (it holds every database; Hermes refuses while
//     anything does); then per database, smallest first: a verified copy
//     FIRST when the disk allows it, then `hermes sessions optimize-storage`.
//     With a copy made first there is no second copy afterwards: the
//     pre-compaction file is the only state from before the rewrite, and a
//     later copy of the compacted file would replace it. A database that is
//     still over the limit and has no copy from this run gets one at the end.
//
// Why before `hermes update`: Hermes' own snapshot then sees a database that
// may be under the limit again, and the code that runs the compaction is the
// one the gate looked at (>= 0.23). After the update, the compaction would run
// on the new code, which nobody has used yet. Why after "close": the desktop
// app and its backend hold the database until then. The gateway is stopped
// only when the question was answered Y, and it comes back exactly as when
// Safe Update stops it for any other reason (Facts.GateStopped: started at
// verify, or by finally on an abort).
//
// None of this is a reason to skip or fail the update: a refusal, a failure, a
// timeout, a lack of disk or a failed copy are rows and WARNs, and the update
// goes on. Only Ctrl+C ends the run (as everywhere else).

const (
	// compactBase and compactPerGiB: the time Safe Update allows one
	// `optimize-storage` run. A rough guess from the size (nothing here
	// measures it): ten minutes plus twelve per GiB, at most six hours.
	compactBase   = 10 * time.Minute
	compactPerGiB = 12 * time.Minute
	compactMax    = 6 * time.Hour
	// copyBase and copyPerGiB: the same for the copy (a read of the whole file).
	copyBase   = 10 * time.Minute
	copyPerGiB = 6 * time.Minute
	copyMax    = 3 * time.Hour

	compactHeartbeat = 60 * time.Second
	stateDBDir       = "safe-update-statedb"

	// compactAskTimeoutS: how long the compaction question waits. The update
	// is held up while it waits, and a question about a long operation is
	// answered at once or not at all, so it is shorter than the usual confirm
	// timeout (600 s). No answer is "no": Safe Update copies and goes on.
	compactAskTimeoutS = 120

	noCopyBefore = "no copy before compaction"
)

// StateDBFacts is what the stage learned and did, for the rows.
type StateDBFacts struct {
	Probed   bool
	RowDone  bool // the probe row was added (check, or an up-to-date verdict)
	All      []statedb.DB
	Big      []statedb.DB // over the limit at the probe, largest first
	Compact  []CompactResult
	Copies   []CopyResult
	Why      string // why compaction was not offered ("" = it was)
	Declined bool
	Yes      bool                  // the question was answered Y: the second stage compacts
	Skips    []hermes.SnapshotSkip // lines of `hermes update` output
	// Probe holds, per profile over the limit, the read-only answer to "does
	// Hermes have compaction work here?" (raw; see verdictOf for the use).
	Probe map[string]statedb.Compaction
	// Offer is what the question was asked about and the second stage
	// compacts: the databases over the limit where compaction is not known
	// to free nothing.
	Offer []statedb.DB
}

// CompactResult is one `optimize-storage` run (or why it did not run).
type CompactResult struct {
	Profile       string
	Before, After int64
	Note          string // "" = compacted
	Ran           bool
	NoCopyFirst   string // why no copy was made before the compaction ("" = one was, or backup is off)
}

// CopyResult is one copy of a database (or why there is none).
type CopyResult struct {
	Profile string
	Size    int64
	Path    string
	Note    string // "" = made and verified
}

// noBigStateDB is the Skip of the stage: no database over the limit (or no
// service) means no step, no frame and no row. The probe is a stat per file.
func noBigStateDB(r *Run) bool {
	r.probeStateDB()
	return r.StateDB == nil || len(r.Facts.SD.Big) == 0
}

// noStateDBCompaction skips the second stage unless the question got a Y.
func noStateDBCompaction(r *Run) bool { return !r.Facts.SD.Yes }

func (r *Run) sdLimit() int64 { return hermes.SnapshotMaxFileSize }

// probeStateDB lists the databases and the ones over the limit (once).
func (r *Run) probeStateDB() {
	if r.StateDB == nil || r.Facts.Install.Home == "" || r.Facts.SD.Probed {
		return
	}
	dbs := r.StateDB.Find(r.Facts.Install.Home)
	big := statedb.Over(dbs, r.sdLimit())
	sort.SliceStable(big, func(i, j int) bool { return big[i].Size > big[j].Size })
	r.Facts.SD.Probed, r.Facts.SD.All, r.Facts.SD.Big = true, dbs, big
	r.Facts.SD.Probe = map[string]statedb.Compaction{}
	for _, d := range big {
		c := r.StateDB.CompactionWork(context.Background(), d.Path)
		r.Facts.SD.Probe[d.Profile] = c
		r.Log.Info("state.db compaction probe (read-only, mirrors Hermes' fts_optimize_available)", slog.String("profile", d.Profile), slog.String("verdict", c.Verdict.String()), slog.String("reason", c.Reason))
	}
	for _, d := range dbs {
		r.Log.Info("state.db", slog.String("profile", d.Profile), slog.Int64("bytes", d.Size), slog.Int64("wal_bytes", d.WAL), slog.Bool("over_limit", d.Size > r.sdLimit()))
	}
	for _, name := range r.StateDB.Ignored(r.Facts.Install.Home) {
		r.Log.Info("state.db: profile folder ignored (its name is not a Hermes profile id [a-z0-9_-])", slog.String("folder", name))
	}
}

func sizeList(dbs []statedb.DB) string {
	var p []string
	for _, d := range dbs {
		p = append(p, d.Profile+" "+statedb.FormatSize(d.Size))
	}
	return strings.Join(p, ", ")
}

func profileNames(dbs []statedb.DB) string {
	var p []string
	for _, d := range dbs {
		p = append(p, d.Profile)
	}
	return strings.Join(p, ", ")
}

// verdictOf is the probe's answer for one database as the flow uses it. "No
// work" is trusted only for a Hermes of the line the check was read from
// (0.23); for another version, or when nothing was probed, it is Unknown, and
// Unknown is handled as before the probe existed: the compaction is offered.
func (r *Run) verdictOf(d statedb.DB) statedb.Compaction {
	c, ok := r.Facts.SD.Probe[d.Profile]
	if !ok {
		return statedb.Compaction{Verdict: statedb.Unknown, Reason: "not probed"}
	}
	if c.Verdict == statedb.NoWork && !hermes.ProbeVerified(r.Facts.VersionBefore) {
		return statedb.Compaction{Verdict: statedb.Unknown, Reason: "this Hermes version is not one the check was read from"}
	}
	return c
}

// splitByWork separates the databases where compaction is known to free
// nothing (compact) from the ones where it can help or where that is not
// known (offer; unsure is the subset of offer that is not known).
func (r *Run) splitByWork(big []statedb.DB) (offer, compact, unsure []statedb.DB) {
	for _, d := range big {
		switch c := r.verdictOf(d); c.Verdict {
		case statedb.NoWork:
			compact = append(compact, d)
		case statedb.Unknown:
			unsure = append(unsure, d)
			offer = append(offer, d)
		default:
			offer = append(offer, d)
		}
	}
	return offer, compact, unsure
}

// stageStateDBCheck is the check's probe: one row, nothing else.
func stageStateDBCheck(ctx context.Context, r *Run) error {
	r.stateDBProbeRow(ctx)
	return nil
}

// stateDBProbeRow probes (once) and adds the row of the sizes. It is also
// called when the run ends as "up to date" before the stage was reached, so
// the sizes show in every run, whatever the Hermes verdict was.
func (r *Run) stateDBProbeRow(ctx context.Context) {
	if r.StateDB == nil || r.Facts.SD.RowDone || ctx.Err() != nil {
		return
	}
	r.Facts.SD.RowDone = true
	r.probeStateDB()
	big := r.Facts.SD.Big
	if len(big) == 0 {
		return
	}
	text := sizeList(big) + " over Hermes' 1 GiB snapshot limit"
	why := r.compactWhyNot(ctx, false)
	offer, compact, unsure := r.splitByWork(big)
	keeps := "Safe Update keeps its own copy"
	if !r.backupOn() {
		keeps = "Hermes' snapshot skips it and statedb.backup is off"
	}
	switch {
	case why != "":
		if r.backupOn() {
			text += "; Safe Update keeps its own copy when it updates (" + why + ")"
		} else {
			text += "; Hermes' snapshot skips it (" + why + ")"
		}
	case len(offer) == 0:
		text += " (search index already compact; " + keeps + ")"
	default:
		if len(compact) == 0 {
			text += "; an attended update offers to compact it first"
		} else {
			text += "; an attended update offers to compact " + profileNames(offer) + " first (" + profileNames(compact) + ": search index already compact; " + keeps + ")"
		}
		if len(unsure) > 0 {
			text += "; could not tell whether compaction helps for " + profileNames(unsure)
		}
	}
	r.extraRow("state.db", text, ptr(ui.VerdictAttention))
}

// compactWhyNot says why compaction cannot be offered in this run ("" = it
// can). attended=false is the check: it asks nothing, so the text only
// describes what an update run would do.
func (r *Run) compactWhyNot(ctx context.Context, attended bool) string {
	c := r.Config
	if c.StateDB.Compact != config.StateDBCompactAsk {
		return "statedb.compact is off"
	}
	if !hermes.CompactCapable(r.Facts.VersionBefore) {
		if r.Facts.VersionBefore == "" {
			return "compaction needs Hermes 0.23 or newer; the version could not be read"
		}
		return fmt.Sprintf("compaction needs Hermes 0.23 or newer; this is %s", strings.TrimPrefix(r.Facts.VersionBefore, "v"))
	}
	if attended && !r.extrasCanAsk(ctx) {
		return "an unattended or --yes run never compacts"
	}
	if !attended && (c.Run.Unattended || c.Run.Yes) {
		return "an unattended or --yes run never compacts"
	}
	if r.Runner == nil || r.Facts.Install.Launcher == "" {
		return "the Hermes launcher is not known, so the command cannot be run"
	}
	res, err := r.Runner.Run(ctx, execx.Cmd{Argv: append([]string{r.Facts.Install.Launcher}, hermes.OptimizeStorageHelpArgv()...), Timeout: checkTimeout, Env: utf8Env})
	if err != nil || !hermes.HasOptimizeStorage(res.Code, res.Output) {
		return "this Hermes has no `sessions optimize-storage` command"
	}
	return ""
}

// ---- disk arithmetic ----

func (r *Run) floorBytes() uint64 { return uint64(r.Config.StateDB.MinFreeGB * 1e9) }

func (r *Run) backupOn() bool { return r.Config.StateDB.Backup == config.StateDBBackupOn }

// dbPlan is what the disk allows for one database's compaction.
type dbPlan struct {
	Compact   bool   // the compaction may run
	CopyFirst bool   // a verified copy is made before it
	Why       string // why not (Compact false) or why no copy first (CopyFirst false)
}

// compactPeak is the disk the compaction itself can take at its peak.
// Hermes builds the new full-text index before it drops the old one
// (sessions_cmd.py, _cmd_optimize_storage) and then runs VACUUM
// (hermes_state_search.py), which in WAL mode writes a temporary copy and the
// new file through the WAL: about twice the file, not once. Hermes' own
// preflight asks for one file's worth and is not enough.
func compactPeak(d statedb.DB) uint64 { return 2 * uint64(d.Size) }

// planCompaction decides, for the free space `free`, whether d can be
// compacted and whether a copy fits before it: free - compactPeak (- the
// copy) must stay at or above the floor.
func (r *Run) planCompaction(d statedb.DB, free uint64) dbPlan {
	floor, peak := r.floorBytes(), compactPeak(d)
	fits := func(extra uint64) bool { return free >= peak+extra && free-peak-extra >= floor }
	if !fits(0) {
		return dbPlan{Why: fmt.Sprintf("only %s free; the compaction can take up to about %s (twice the file) and %s must stay free",
			statedb.FormatSize(int64(free)), statedb.FormatSize(int64(peak)), statedb.FormatSize(int64(floor)))}
	}
	if !r.backupOn() {
		return dbPlan{Compact: true, Why: "statedb.backup is off"}
	}
	if !fits(d.CopyNeed()) {
		return dbPlan{Compact: true, Why: fmt.Sprintf("only %s free; a copy first would need about %s more", statedb.FormatSize(int64(free)), statedb.FormatSize(int64(d.CopyNeed())))}
	}
	return dbPlan{Compact: true, CopyFirst: true}
}

func (r *Run) freeNow() (uint64, error) { return r.Platform.Disk.Free(r.Facts.Install.Home) }

func compactTimeout(size int64) time.Duration {
	d := compactBase + time.Duration(float64(size)/(1<<30)*float64(compactPerGiB))
	return min(d, compactMax)
}

func copyTimeout(size int64) time.Duration {
	d := copyBase + time.Duration(float64(size)/(1<<30)*float64(copyPerGiB))
	return min(d, copyMax)
}

// ---- stage 1: before close ----

// stageStateDBPre is the first stage (see the comment at the top).
func stageStateDBPre(ctx context.Context, r *Run) error {
	r.probeStateDB()
	big := r.Facts.SD.Big
	if len(big) == 0 {
		return nil
	}
	r.info("session database(s) over Hermes' 1 GiB snapshot limit: %s", sizeList(big))

	why := r.compactWhyNot(ctx, true)
	offer, compact, unsure := r.splitByWork(big)
	if why == "" && len(offer) == 0 {
		why = "the search index is already compact, so compaction would free nothing"
	}
	r.Facts.SD.Why = why
	if why != "" {
		r.Log.Info("state.db compaction not offered: " + why)
	} else {
		if len(compact) > 0 {
			r.info("search index already compact (nothing to do): %s", profileNames(compact))
		}
		if len(unsure) > 0 {
			r.info("could not tell whether compaction helps for %s (%s); offering it", profileNames(unsure), probeReasons(r, unsure))
		}
		yes, err := r.askCompaction(ctx, offer, compact, unsure)
		if err != nil {
			return err
		}
		r.Facts.SD.Yes = yes
		r.Facts.SD.Declined = !yes
		if yes {
			r.Facts.SD.Offer = offer
		}
	}
	if r.Facts.SD.Yes {
		return nil // copy and compaction follow the closing of Hermes
	}
	if r.backupOn() {
		if err := r.copyStateDBs(ctx, big); err != nil {
			return err
		}
	}
	r.stateDBRows(big)
	return nil
}

func probeReasons(r *Run, dbs []statedb.DB) string {
	var p []string
	for _, d := range dbs {
		p = append(p, d.Profile+": "+r.verdictOf(d).Reason)
	}
	return strings.Join(p, "; ")
}

// askCompaction asks the one question about the databases where compaction
// can help (or is not known not to). true = Y. A non-nil error is Ctrl+C.
func (r *Run) askCompaction(ctx context.Context, big, compact, unsure []statedb.DB) (bool, error) {
	rough := time.Duration(0)
	for _, d := range big {
		rough += time.Duration(float64(d.Size) / (1 << 30) * float64(compactPerGiB) / 2)
	}
	extra := ""
	if len(compact) > 0 {
		extra += fmt.Sprintf("%s: search index already compact, left alone. ", profileNames(compact))
	}
	if len(unsure) > 0 {
		extra += fmt.Sprintf("Could not tell whether compaction helps for %s. ", profileNames(unsure))
	}
	text := fmt.Sprintf("Compact the session database(s) before updating? %s (over Hermes' 1 GiB snapshot limit, so its own snapshot would skip them). %s"+
		"After Hermes is closed, Safe Update stops the gateway and runs `hermes sessions optimize-storage` per database; roughly %d min in all (a guess from the size, not measured), then the update goes on. %s"+
		"Ctrl+C is safe between Hermes' steps, but its last step (VACUUM) is one statement that cannot be interrupted, so stopping can take a while; Hermes resumes the rest next time. "+
		"Press Y to compact, N to skip (Safe Update copies the database now instead).",
		sizeList(big), extra, max(1, int(rough.Minutes())), r.compactionPlanText(big))
	r.drainInput()
	a, err := r.askSpec(ctx, ui.PromptSpec{Text: text + " (skipping in " + ui.CountdownText(compactAskTimeoutS) + ")", Keys: []rune{'y', 'n'}, Timeout: compactAskTimeoutS * time.Second, CountdownLabel: "skipping in", AbortOnCtrlC: true}, text, true)
	if a.Abort || ctx.Err() != nil || (err != nil && errors.Is(err, context.Canceled)) {
		r.stop("cancelled at the compaction question. Nothing was changed; Hermes will be reopened.")
		return false, fmt.Errorf("%w: cancelled at the database compaction question", apperr.ErrCancelled)
	}
	if err != nil || a.TimedOut || a.Key != 'y' {
		r.info("database compaction: skipped")
		return false, nil
	}
	return true, nil
}

// compactionPlanText says, per database and for the free space now, whether
// a copy comes first (the question must not promise what the disk will not
// allow).
func (r *Run) compactionPlanText(big []statedb.DB) string {
	free, err := r.freeNow()
	if err != nil {
		return "Free disk space could not be read, so the compaction may be skipped. "
	}
	ascending := append([]statedb.DB(nil), big...)
	sort.SliceStable(ascending, func(i, j int) bool { return ascending[i].Size < ascending[j].Size })
	var parts []string
	for _, d := range ascending {
		p := r.planCompaction(d, free)
		switch {
		case !p.Compact:
			parts = append(parts, d.Profile+": will be skipped, "+p.Why)
		case p.CopyFirst:
			parts = append(parts, d.Profile+": a verified copy first, then the compaction")
			free -= min(free, d.CopyNeed())
		default:
			parts = append(parts, d.Profile+": "+noCopyBefore+" ("+p.Why+")")
		}
	}
	return "Disk: " + strings.Join(parts, "; ") + ". "
}

// ---- stage 2: after close, after Y ----

// stageStateDB stops the gateway and compacts, each database behind a copy
// where the disk allows. A non-nil error is Ctrl+C.
func stageStateDB(ctx context.Context, r *Run) error {
	big := r.Facts.SD.Offer
	if len(big) == 0 {
		return nil
	}
	if r.stopGatewayForCompaction(ctx) {
		if err := r.compactStateDBs(ctx, big); err != nil {
			return err
		}
	} else {
		for _, d := range big {
			r.Facts.SD.Compact = append(r.Facts.SD.Compact, CompactResult{Profile: d.Profile, Before: d.Size, After: d.Size,
				Note: "skipped: the gateway could not be stopped, and Hermes refuses while it runs"})
		}
	}
	still := statedb.Over(r.StateDB.Find(r.Facts.Install.Home), r.sdLimit())
	sort.SliceStable(still, func(i, j int) bool { return still[i].Size > still[j].Size })
	if r.backupOn() {
		var need []statedb.DB
		for _, d := range still {
			if !r.copiedThisRun(d.Profile) {
				need = append(need, d)
			}
		}
		if err := r.copyStateDBs(ctx, need); err != nil {
			return err
		}
	}
	r.stateDBRows(still)
	return nil
}

func (r *Run) copiedThisRun(profile string) bool {
	for _, c := range r.Facts.SD.Copies {
		if c.Profile == profile && c.Note == "" {
			return true
		}
	}
	return false
}

// stopGatewayForCompaction stops the gateway (the holder of every database)
// when one runs. false = the compaction must not run (the stop failed).
func (r *Run) stopGatewayForCompaction(ctx context.Context) bool {
	if r.Gateway == nil {
		return true
	}
	gws, perr := r.procsOf(ctx, hermes.ClassGateway)
	if perr != nil || len(gws) == 0 {
		return true
	}
	r.info("stopping the gateway for the compaction")
	if serr := r.Gateway.Stop(ctx); serr != nil {
		r.warn("the gateway could not be stopped (%v); the compaction was skipped", serr)
		r.ensureGatewayRunning(ctx)
		r.Facts.SD.Why = "the gateway could not be stopped"
		return false
	}
	r.Facts.GateStopped = true
	r.Facts.GateLine = "stopped for the database compaction and the update, restarted after"
	return true
}

// compactStateDBs compacts smallest first. A non-nil error is Ctrl+C.
func (r *Run) compactStateDBs(ctx context.Context, big []statedb.DB) error {
	ascending := append([]statedb.DB(nil), big...)
	sort.SliceStable(ascending, func(i, j int) bool { return ascending[i].Size < ascending[j].Size })
	for _, d := range ascending {
		res, err := r.compactOne(ctx, d)
		r.Facts.SD.Compact = append(r.Facts.SD.Compact, res)
		if err != nil {
			return err
		}
	}
	return nil
}

// compactOne copies (when the disk allows) and compacts one profile. A
// non-nil error is Ctrl+C.
func (r *Run) compactOne(ctx context.Context, d statedb.DB) (CompactResult, error) {
	res := CompactResult{Profile: d.Profile, Before: d.Size, After: d.Size}
	if strings.HasPrefix(d.Profile, "profiles/") {
		res.Note = "skipped: this folder cannot be named to Hermes with -p (Hermes reads \"default\" as the home itself)"
		r.warn("compaction of %s skipped: %s", d.Profile, res.Note)
		return res, nil
	}
	free, ferr := r.freeNow()
	if ferr != nil {
		res.Note = "skipped: free disk space could not be read"
		r.warn("compaction of %s skipped: %s", d.Profile, res.Note)
		return res, nil
	}
	plan := r.planCompaction(d, free)
	r.Log.Info("compaction disk estimate", slog.String("profile", d.Profile), slog.Uint64("free", free), slog.Int64("size", d.Size),
		slog.Uint64("peak", compactPeak(d)), slog.Uint64("copy_need", d.CopyNeed()), slog.Uint64("floor", r.floorBytes()),
		slog.Bool("compact", plan.Compact), slog.Bool("copy_first", plan.CopyFirst))
	if !plan.Compact {
		res.Note = "skipped: " + plan.Why
		r.warn("compaction of %s skipped: %s", d.Profile, plan.Why)
		return res, nil
	}
	if plan.CopyFirst {
		cr, err := r.copyOne(ctx, d, r.Clock.Now().Format("20060102-150405"))
		r.Facts.SD.Copies = append(r.Facts.SD.Copies, cr)
		if err != nil {
			return res, err
		}
		if cr.Note != "" {
			// The copy was promised and failed: compacting now would leave
			// nothing to fall back on, so this database is left alone.
			res.Note = "skipped: the copy made first failed (" + cr.Note + ")"
			r.warn("compaction of %s skipped: no verified copy to fall back on", d.Profile)
			return res, nil
		}
	} else if r.backupOn() {
		res.NoCopyFirst = plan.Why
		r.warn("compacting %s with %s: %s", d.Profile, noCopyBefore, plan.Why)
	}

	args := hermes.OptimizeStorageArgv(d.Profile)
	to := compactTimeout(d.Size)
	cmdline := "hermes " + strings.Join(args, " ")
	r.UI.Command(cmdline)
	r.Log.Info("== Running: "+cmdline, slog.Int64("bytes", d.Size), slog.Int("timeout_min", int(to.Minutes())))
	r.info("compacting %s (%s); allowed up to %d min", d.Profile, statedb.FormatSize(d.Size), int(to.Minutes()))

	var last, started = r.Clock.Now(), r.Clock.Now()
	var lastMu = &r.mu
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(compactHeartbeat)
		defer t.Stop()
		cancelled := ctx.Done()
		for {
			select {
			case <-stop:
				return
			case <-cancelled:
				r.info("Ctrl+C: waiting for Hermes to finish its current step (VACUUM cannot be interrupted)")
				cancelled = nil
			case <-t.C:
				lastMu.Lock()
				quiet := r.Clock.Now().Sub(last)
				lastMu.Unlock()
				r.info("  (still compacting %s: %d min so far, last output %d s ago)", d.Profile, int(r.Clock.Now().Sub(started).Minutes()), int(quiet.Seconds()))
			}
		}
	}()
	out, err := r.Runner.Stream(context.WithoutCancel(ctx), execx.Cmd{
		Argv: append([]string{r.Facts.Install.Launcher}, args...), Dir: r.Facts.Install.Home, Timeout: to, ShowWindow: true, Env: utf8Env,
		KillTree: func(pid int) error {
			return r.Platform.Procs.KillTree(context.WithoutCancel(ctx), platform.Process{PID: pid})
		},
	}, func(line string) {
		lastMu.Lock()
		last = r.Clock.Now()
		lastMu.Unlock()
		if i := strings.LastIndex(line, "\r"); i >= 0 { // progress redraws: show the last state
			line = line[i+1:]
		}
		r.Log.Info("  | " + line)
		if strings.TrimSpace(line) != "" {
			r.UI.Output(line)
		}
	})
	close(stop)
	text := out.Output
	after := r.sizeOf(d)
	res.After = after
	switch {
	case ctx.Err() != nil:
		res.Note = "stopped by Ctrl+C; nothing was lost, run it again to resume"
		r.stop("Ctrl+C during the compaction of %s. No data was lost (it resumes next time). The update was not run; Hermes will be reopened.", d.Profile)
		return res, fmt.Errorf("%w: interrupted during the database compaction", apperr.ErrCancelled)
	case err != nil:
		res.Note = "could not be run: " + oneLine(err.Error())
		r.warn("compaction of %s could not be run: %s", d.Profile, oneLine(err.Error()))
	case out.TimedOut:
		res.Note = fmt.Sprintf("stopped after %d min (too slow); the index work resumes where it stopped, but a VACUUM that was killed starts over: run it again", int(to.Minutes()))
		r.warn("compaction of %s was stopped after %d min; the index work resumes, a killed VACUUM starts over; no data was lost", d.Profile, int(to.Minutes()))
	case hermes.OptimizeRefusal(text) != "":
		ref := hermes.OptimizeRefusal(text)
		res.Note = "Hermes refused: " + oneLine(ref)
		r.warn("Hermes refused to compact %s: %s", d.Profile, oneLine(ref))
	case hermes.OptimizeNoDisk(text):
		res.Note = "Hermes says there is not enough free disk"
		r.warn("compaction of %s: Hermes says there is not enough free disk", d.Profile)
	case out.Code != 0:
		res.Note = fmt.Sprintf("failed (exit %d); no data was lost, run it again to resume", out.Code)
		r.warn("compaction of %s failed (exit %d); no data was lost", d.Profile, out.Code)
	case hermes.OptimizeAlreadyCompact(text):
		res.Note = "already compact (Hermes had nothing to do)"
		r.info("%s: already on the compact layout", d.Profile)
	case hermes.OptimizeVacuumSkipped(text) != "":
		line := hermes.OptimizeVacuumSkipped(text)
		res.Note = "index rebuilt but the VACUUM did not run, so no space was returned: " + oneLine(line)
		r.warn("compaction of %s: Hermes skipped the VACUUM (%s); the file did not shrink", d.Profile, oneLine(line))
	default:
		res.Ran = true
		freed := d.Size - after
		r.info("%s: %s -> %s (%s freed)", d.Profile, statedb.FormatSize(d.Size), statedb.FormatSize(after), statedb.FormatSize(max(freed, 0)))
	}
	return res, nil
}

func (r *Run) sizeOf(d statedb.DB) int64 {
	for _, n := range r.StateDB.Find(r.Facts.Install.Home) {
		if n.Path == d.Path {
			return n.Size
		}
	}
	return d.Size
}

func oneLine(s string) string {
	var p []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			p = append(p, l)
		}
	}
	out := strings.Join(p, " | ")
	if len(out) > 300 {
		out = out[:300] + "..."
	}
	return out
}

// ---- copies ----

// hermesHolds reports whether a Hermes process has the databases open now
// (true when that cannot be told).
func (r *Run) hermesHolds(ctx context.Context) bool {
	if r.Platform == nil || r.Platform.Procs == nil {
		return false
	}
	ps, err := r.procsOf(ctx, hermes.ClassDesktop, hermes.ClassBackend, hermes.ClassKernel, hermes.ClassGateway)
	return err != nil || len(ps) > 0
}

// copyStateDBs makes the own copies. A non-nil error is Ctrl+C.
func (r *Run) copyStateDBs(ctx context.Context, dbs []statedb.DB) error {
	stamp := r.Clock.Now().Format("20060102-150405")
	for _, d := range dbs {
		cr, err := r.copyOne(ctx, d, stamp)
		r.Facts.SD.Copies = append(r.Facts.SD.Copies, cr)
		if err != nil {
			return err
		}
	}
	return nil
}

// copyOne makes one copy. The error is Ctrl+C only; every other outcome is in
// CopyResult.Note.
func (r *Run) copyOne(ctx context.Context, d statedb.DB, stamp string) (CopyResult, error) {
	dir := filepath.Join(r.Facts.Install.Home, "backups", stateDBDir)
	cr := CopyResult{Profile: d.Profile, Size: d.Size}
	to := copyTimeout(d.Size)
	holds := r.hermesHolds(ctx)
	r.info("copying %s's state.db (%s) to %s; allowed up to %d min", d.Profile, statedb.FormatSize(d.Size), dir, int(to.Minutes()))
	cctx, cancel := context.WithTimeout(ctx, to)
	res, err := r.StateDB.Backup(cctx, statedb.BackupReq{Src: d, Dir: dir, Stamp: stamp, MinFree: r.floorBytes(), HolderRuns: holds,
		Free: func(string) (uint64, error) { return r.Platform.Disk.Free(r.Facts.Install.Home) }})
	cancel()
	switch {
	case ctx.Err() != nil:
		r.stop("Ctrl+C during the copy of %s's state.db; the partial copy was removed. The update was not run; Hermes will be reopened.", d.Profile)
		return cr, fmt.Errorf("%w: interrupted during the database copy", apperr.ErrCancelled)
	case err != nil:
		cr.Note = "NOT made: " + oneLine(err.Error())
		r.warn("no copy of %s's state.db: %s", d.Profile, oneLine(err.Error()))
	case res.Skipped != "":
		cr.Note = "NOT made: " + res.Skipped
		r.warn("no copy of %s's state.db: %s", d.Profile, res.Skipped)
	default:
		cr.Path, cr.Size = res.Path, res.Size
		r.Log.Info("state.db copy verified", slog.String("profile", d.Profile), slog.String("path", res.Path), slog.Int64("bytes", res.Size),
			slog.Int("took_s", int(res.Took.Seconds())), slog.Any("replaced", res.Replaced))
		r.info("copy of %s verified (%s, quick_check ok, %d older copy(ies) removed)", d.Profile, statedb.FormatSize(res.Size), len(res.Replaced))
	}
	return cr, nil
}

// stateDBRows puts the rows of what happened on the end card.
func (r *Run) stateDBRows(still []statedb.DB) {
	sd := &r.Facts.SD
	var good, bad []string
	for _, c := range sd.Copies {
		if c.Note == "" {
			good = append(good, fmt.Sprintf("%s (%s)", c.Profile, statedb.FormatSize(c.Size)))
		} else {
			bad = append(bad, c.Profile+" "+c.Note)
		}
	}
	if len(sd.Compact) > 0 {
		var parts []string
		var freed int64
		v := ptr(ui.VerdictGood)
		for _, c := range sd.Compact {
			switch {
			case c.Ran:
				freed += max(c.Before-c.After, 0)
				p := fmt.Sprintf("%s %s -> %s", c.Profile, statedb.FormatSize(c.Before), statedb.FormatSize(c.After))
				if c.NoCopyFirst != "" {
					p += " (" + noCopyBefore + ": " + c.NoCopyFirst + ")"
					v = ptr(ui.VerdictAttention)
				}
				parts = append(parts, p)
			default:
				parts = append(parts, c.Profile+" "+c.Note)
				if !strings.HasPrefix(c.Note, "already") {
					v = ptr(ui.VerdictAttention)
				}
			}
		}
		r.extraRow("db compaction", fmt.Sprintf("%s (%s freed)", strings.Join(parts, "; "), statedb.FormatSize(freed)), v)
	}
	if len(still) == 0 {
		if len(sd.Compact) > 0 {
			text := "now under Hermes' 1 GiB snapshot limit; its own snapshot covers it"
			if len(good) > 0 {
				text += "; Safe Update also kept its copy from before the compaction: " + strings.Join(good, ", ")
			}
			r.extraRow("state.db", text, ptr(ui.VerdictGood))
		}
		return
	}
	var text string
	v := ptr(ui.VerdictAttention)
	switch {
	case len(good) > 0 && len(bad) == 0:
		text = "too big for Hermes' pre-update snapshot (" + sizeList(still) + "); Safe Update kept its own verified copy: " + strings.Join(good, ", ") + " in backups\\" + stateDBDir
		v = ptr(ui.VerdictGood)
	case len(good) > 0:
		text = "Safe Update kept a copy of " + strings.Join(good, ", ") + "; " + strings.Join(bad, "; ")
	case len(bad) > 0:
		text = "too big for Hermes' pre-update snapshot (" + sizeList(still) + "); " + strings.Join(bad, "; ")
	case !r.backupOn():
		text = "too big for Hermes' pre-update snapshot (" + sizeList(still) + "); statedb.backup is off, so this update had no copy of it"
	default:
		text = "too big for Hermes' pre-update snapshot (" + sizeList(still) + "); no copy was made"
	}
	if _, compact, _ := r.splitByWork(still); len(compact) > 0 {
		text += "; search index already compact for " + profileNames(compact) + " (compaction would free nothing)"
	}
	if sd.Why != "" && strings.Contains(sd.Why, "0.23") {
		text += "; " + sd.Why
	}
	r.extraRow("state.db", text, v)
}

// noteSnapshotSkips reads Hermes' own skip lines after the update. They are a
// second signal: when Hermes skipped a database that Safe Update has no copy
// of (a limit that changed, a profile the probe did not see), the card says so.
func (r *Run) noteSnapshotSkips(out string) {
	skips, prune := hermes.ParseSnapshotSkips(out)
	if len(skips) == 0 && !prune {
		return
	}
	r.Facts.SD.Skips = skips
	r.Log.Info("hermes update: snapshot skipped a database", slog.Int("files", len(skips)), slog.Bool("prune_skipped", prune))
	made := 0
	for _, c := range r.Facts.SD.Copies {
		if c.Note == "" {
			made++
		}
	}
	if len(skips) > made {
		var what []string
		for _, s := range skips {
			what = append(what, fmt.Sprintf("%s (%s over %s)", s.File, s.Size, s.Limit))
		}
		r.extraRow("db snapshot", fmt.Sprintf("Hermes skipped %d file(s) in its snapshot (%s) and Safe Update holds %d copy(ies); turn statedb.backup on or compact with a Hermes 0.23 or newer", len(skips), strings.Join(what, ", "), made), ptr(ui.VerdictAttention))
	}
}
