package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/cua"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/settings"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// Fixed intervals of the Python updater (python-behaviour §3.1).
const (
	checkTimeout      = 300 * time.Second
	closePoll         = 3 * time.Second
	afterForceWait    = 3 * time.Second
	gatewayPoll       = 5 * time.Second
	lateStartWait     = 20 * time.Second
	watchdogPoll      = 5 * time.Second
	defaultHookTimout = config.DefaultHookTimeoutS * time.Second
)

// Flow returns the stage list for a mode (design.md §3).
func Flow(mode Mode) []Stage {
	if mode == ModeCheck {
		return []Stage{
			{Key: "stepAside", Run: stepAside},
			{Key: "local", Run: stageLocal},
			{Key: "remote", Run: stageRemote},
			{Key: "procs", Run: stageProcs},
			{Key: "sessions", Run: stageSessions},
			{Key: "gc_preview", Run: stageGCPreview, Skip: func(r *Run) bool { return r.GC == nil }},
			{Key: "cua_status", Run: stageCUAStatus, Skip: func(r *Run) bool { return r.CUA == nil }},
			{Key: "summary", Run: stageCheckSummary},
		}
	}
	notVerified := func(r *Run) bool { return !r.Facts.Verified }
	return []Stage{
		{Key: "stepAside", Run: stepAside},
		{Key: "local", Run: stageLocal},
		{Key: "remote", Run: stageRemote},
		{Key: "procs", Run: stageProcs},
		{Key: "wait", Run: stageWait},
		{Key: "close", Run: stageClose},
		{Key: "gateway_stop", Run: stageUpdate},
		{Key: "verify", Run: stageVerify},
		{Key: "settings", Run: stageSettings, Skip: func(r *Run) bool {
			return !r.Facts.Verified || r.Settings == nil || !r.Config.Settings.AutoMigrate
		}},
		{Key: "hooks", Run: stageHooks, Skip: func(r *Run) bool { return r.Hooks == nil }},
		{Key: "relaunch", Run: stageRelaunch, Skip: func(r *Run) bool { return !r.relaunchDue() }},
		{Key: "cleanup", Run: stageCleanup, Skip: func(r *Run) bool { return r.GC == nil }},
		{Key: "cua", Run: stageCUA, Skip: func(r *Run) bool { return notVerified(r) || r.CUA == nil }},
		{Key: "summary", Run: stageUpdateSummary},
	}
}

// ---- 0 stepAside ----

func stepAside(ctx context.Context, r *Run) error {
	if r.Locator == nil {
		return fmt.Errorf("%w: no install locator", apperr.ErrLauncherMissing)
	}
	in, err := r.Locator.Locate(ctx)
	if err != nil {
		if errors.Is(err, apperr.ErrLauncherMissing) {
			r.stop("launcher not found: %v", err)
		}
		return err
	}
	r.Facts.Install = in
	if in.Kind != hermes.KindCheckout {
		r.info("This Hermes install is a %s, not a managed source checkout: %s.", in.Kind, in.StepAside)
		return fmt.Errorf("%w (%s): %s", apperr.ErrNotCheckout, in.Kind, in.StepAside)
	}
	if r.Wire != nil {
		r.Wire(in, &r.Deps)
	}
	return nil
}

// ---- 1 local (§2.2 1-5) ----

func stageLocal(ctx context.Context, r *Run) error {
	r.section("Pre-flight")
	if r.Marker != nil {
		pid, stale := r.Marker.Holder()
		if pid > 0 {
			r.stop("another update holds the marker (pid %d). Wait for it or close it first.", pid)
			return fmt.Errorf("%w: marker held by pid %d", apperr.ErrPreflightStop, pid)
		}
		if stale {
			r.note("stale update marker found (owner dead); it will be replaced")
		}
	}
	need := r.Config.Tunables.MinFreeGB
	if free, err := r.Platform.Disk.Free(r.Facts.Install.Home); err != nil {
		r.warn("disk free: unknown (%v)", err)
	} else {
		gb := float64(free) / 1e9
		if gb < need {
			r.rows(ui.Row{Key: "disk free", Value: fmt.Sprintf("%.1f GB (need %g GB, too little)", gb, need)})
			r.stop("not enough free disk space for the desktop rebuild")
			r.Facts.DiskLow = true
		} else {
			r.rows(ui.Row{Key: "disk free", Value: fmt.Sprintf("%.1f GB (enough)", gb)})
		}
	}
	if r.Repo != nil {
		if n, err := r.Repo.DirtyCount(ctx); err != nil {
			r.warn("checkout status unknown: %v", err)
		} else if n == 0 {
			r.rows(ui.Row{Key: "checkout", Value: "clean"})
		} else {
			r.rows(ui.Row{Key: "checkout", Value: fmt.Sprintf("%d modified tracked file(s) (the updater stashes them and keeps the stash)", n)})
		}
		short, full, err := r.Repo.Head(ctx)
		if err != nil {
			r.warn("current HEAD unknown: %v", err)
		}
		r.Facts.HeadShort = short
		if len(full) == 40 {
			r.Facts.HeadFull = full
		}
	}
	sha := "?"
	if r.Gateway != nil {
		if s := r.Gateway.State().CodeSHA; s != "" {
			sha = s[:min(10, len(s))]
		}
	}
	r.rows(ui.Row{Key: "version", Value: versionValue(r.Facts.HeadShort, sha)})
	r.Facts.VersionBefore = hermes.CurrentVersion(ctx, r.Runner, r.Facts.Install.Launcher)
	r.startEarly(ctx)
	return nil
}

// earlyResult is the process listing (and gate probe) that runs beside the
// update check, as in the Python's side thread.
type earlyResult struct {
	procs  []platform.Process
	err    error
	probed bool
	gate   hermes.GateResult
	info   string
}

func (r *Run) startEarly(ctx context.Context) {
	ch := make(chan earlyResult, 1)
	r.early = ch
	go func() {
		var e earlyResult
		e.procs, e.err = r.Platform.Procs.List(ctx)
		if e.err == nil && r.Gateway != nil && r.count(e.procs)[hermes.ClassGateway] > 0 {
			e.probed = true
			e.gate, e.info = r.Gateway.Probe(ctx)
		}
		ch <- e
	}()
}

// ---- 2 remote (§2.3) ----

func stageRemote(ctx context.Context, r *Run) error {
	c := r.checkForUpdates(ctx)
	r.Facts.Check = c
	r.withEst(func(e *timings.Estimator) {
		e.SetConditions(timings.Conditions{Source: c.Source, Mirror: ptr(c.Source == "mirror"), NPM: c.NPM, PyDeps: c.PyDeps, Commits: c.Commits})
		if c.PyDeps != nil {
			e.ExpectSkip("pydeps", !*c.PyDeps)
		}
	})
	if c.Behind != nil && *c.Behind {
		return nil
	}
	if r.Config.Run.Force {
		r.note("--force: running even though no update was found")
		return nil
	}
	if c.Behind != nil {
		r.info("Nothing to update (use --force to run anyway).")
		r.joinEarly(ctx) // never leave the side job running
		return errUpToDate
	}
	r.stop("could not check for updates (see above); nothing was changed")
	r.joinEarly(ctx)
	return fmt.Errorf("%w: update check failed", apperr.ErrPreflightStop)
}

func kindText(v *bool, changed string) string {
	switch {
	case v == nil:
		return "unknown"
	case *v:
		return changed
	}
	return "unchanged"
}

func (r *Run) reportCheck(c hermes.UpdateCheck, via string) {
	if c.Behind != nil && *c.Behind {
		n := "new commits"
		if c.Commits != nil {
			n = fmt.Sprintf("%d new commit(s)", *c.Commits)
		}
		r.rows(ui.Row{Key: "updates", Value: fmt.Sprintf("%s on main (%s)", n, via)},
			ui.Row{Key: "python deps", Value: kindText(c.PyDeps, "changed, will be reinstalled")},
			ui.Row{Key: "node packages", Value: kindText(c.NPM, "changed, will be reinstalled (slower build)")})
		return
	}
	r.rows(ui.Row{Key: "updates", Value: fmt.Sprintf("none, already up to date (%s)", via)})
}

func (r *Run) checkForUpdates(ctx context.Context) hermes.UpdateCheck {
	in := r.Facts.Install
	if r.Mirror != nil {
		st := r.Mirror.Status()
		r.Facts.MirrorState = &st
		if st.OK && r.Facts.HeadFull != "" {
			res := r.Mirror.Seed(ctx, in.Checkout)
			if res.Used {
				proof := "not proven"
				if res.Complete {
					proof = "yes"
				}
				r.info("mirror: checkout seeded from the local mirror in %s (nothing left to download from GitHub: %s)", fmtDuration(res.Took), proof)
				if res.Commits != nil {
					behind := *res.Commits > 0
					c := hermes.UpdateCheck{Behind: &behind, Commits: res.Commits, Source: "mirror"}
					if behind && r.Repo != nil {
						c.NPM, c.PyDeps = r.Repo.DiffKinds(ctx)
					}
					r.reportCheck(c, "local mirror")
					return c
				}
				r.info("mirror: commit count unavailable; asking GitHub instead")
			} else if res.Reason != "" && res.Reason != "no mirror set up" {
				r.info("mirror: not used (%s); checking GitHub directly", res.Reason)
			}
		}
		if r.Facts.HeadFull != "" {
			if cmp := r.Mirror.APICompare(ctx, r.Facts.HeadFull); cmp != nil {
				n := cmp.Commits
				behind := n > 0
				c := hermes.UpdateCheck{Behind: &behind, Commits: &n, Source: "api"}
				if behind {
					c.NPM, c.PyDeps = hermes.ClassifyChanged(cmp.Files, cmp.FilesComplete)
				}
				r.reportCheck(c, "GitHub API")
				return c
			}
			r.info("GitHub API unavailable; checking with git fetch")
		}
	}
	res, err := r.Runner.Run(ctx, execx.Cmd{Argv: []string{in.Launcher, "update", "--check"}, Timeout: checkTimeout, Env: utf8Env})
	if err != nil {
		return hermes.UpdateCheck{Source: "git", Detail: err.Error()}
	}
	c := hermes.InterpretUpdateCheck(res.Output, res.Code)
	r.info("%s", c.Detail)
	if c.Behind != nil && *c.Behind && r.Repo != nil {
		c.NPM, c.PyDeps = r.Repo.DiffKinds(ctx)
	}
	if c.Behind != nil {
		r.reportCheck(c, "git fetch")
	}
	return c
}

var utf8Env = map[string]string{"PYTHONIOENCODING": "utf-8"}

// ---- 3 procs (§2.2 8-9) ----

func (r *Run) joinEarly(ctx context.Context) (earlyResult, error) {
	if r.early == nil {
		l, err := r.Platform.Procs.List(ctx)
		return earlyResult{procs: l, err: err}, err
	}
	select {
	case e := <-r.early:
		r.early = nil
		return e, e.err
	case <-ctx.Done():
		return earlyResult{}, ctx.Err()
	}
}

func (r *Run) classify(p platform.Process) string {
	if r.Classify == nil {
		return ""
	}
	return r.Classify(p)
}

func (r *Run) count(ps []platform.Process) map[string]int {
	out := map[string]int{}
	for _, p := range ps {
		if c := r.classify(p); c != "" {
			out[c]++
		}
	}
	return out
}

// procsOf lists the processes of one class (fresh snapshot).
func (r *Run) procsOf(ctx context.Context, class ...string) ([]platform.Process, error) {
	l, err := r.Platform.Procs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process listing failed: %w", err)
	}
	var out []platform.Process
	for _, p := range l {
		c := r.classify(p)
		for _, want := range class {
			if c == want {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func runningText(counts map[string]int) string {
	var parts []string
	for _, c := range []string{hermes.ClassDesktop, hermes.ClassGateway, hermes.ClassBackend, hermes.ClassKernel} {
		switch n := counts[c]; {
		case n == 1:
			parts = append(parts, c)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%s ×%d", c, n))
		}
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// chatAncestor walks our parent chain in the snapshot and returns the first
// ancestor that is a gateway, backend or kernel of this install (the updater
// is then running inside a Hermes chat and stopping Hermes would kill it). A
// missing parent, a cycle or a parent that started after its child (pid
// reuse) ends the walk quietly: that is not an error.
func (r *Run) chatAncestor(procs []platform.Process) (platform.Process, bool) {
	self := r.SelfPID
	if self <= 0 {
		self = os.Getpid()
	}
	byPID := make(map[int]platform.Process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	cur, ok := byPID[self]
	for hops := 0; ok && hops < 64; hops++ {
		parent, found := byPID[cur.PPID]
		if !found || parent.PID == cur.PID || parent.PID <= 0 {
			break
		}
		if !parent.Created.IsZero() && !cur.Created.IsZero() && parent.Created.After(cur.Created) {
			break
		}
		switch r.classify(parent) {
		case hermes.ClassGateway, hermes.ClassBackend, hermes.ClassKernel:
			return parent, true
		}
		cur = parent
	}
	return platform.Process{}, false
}

const insideChatMessage = "Run this from a normal terminal or the Start menu, not from inside a Hermes chat: updating would stop the process that runs it."

// chatEnv is the second inside-a-chat signal, for when the parent chain is
// broken (an intermediate process such as `start` or an MSYS shell already
// exited). Hermes sets HERMES_SESSION_ID and HERMES_AGENT=true in every agent
// terminal child; the Start menu and the desktop palette rows (explorer.exe
// opens the shim, so the child has explorer's environment) carry neither.
// HERMES_DESKTOP alone is deliberately not a signal. Returns the variable
// name that matched, or "".
func (r *Run) chatEnv() string {
	if r.Getenv == nil {
		return ""
	}
	if strings.TrimSpace(r.Getenv("HERMES_SESSION_ID")) != "" {
		return "HERMES_SESSION_ID"
	}
	if strings.EqualFold(strings.TrimSpace(r.Getenv("HERMES_AGENT")), "true") {
		return "HERMES_AGENT"
	}
	return ""
}

func stageProcs(ctx context.Context, r *Run) error {
	e, err := r.joinEarly(ctx)
	if err != nil {
		return fmt.Errorf("process listing failed: %w", err)
	}
	if r.Mode == ModeUpdate {
		if a, ok := r.chatAncestor(e.procs); ok {
			r.Log.Info("updater started from inside Hermes", slog.String("signal", "ancestor"), slog.Int("ancestor_pid", a.PID), slog.String("class", r.classify(a)))
			r.stop(insideChatMessage)
			return fmt.Errorf("%w: started from inside a Hermes chat (ancestor pid %d)", apperr.ErrPreflightStop, a.PID)
		}
		if name := r.chatEnv(); name != "" {
			r.Log.Info("updater started from inside Hermes", slog.String("signal", "environment"), slog.String("variable", name))
			r.stop(insideChatMessage)
			return fmt.Errorf("%w: started from inside a Hermes chat (%s is set)", apperr.ErrPreflightStop, name)
		}
	}
	r.Facts.Procs = r.count(e.procs)
	r.rows(ui.Row{Key: "running", Value: runningText(r.Facts.Procs)})
	if r.Facts.Procs[hermes.ClassGateway] > 0 && r.Gateway != nil {
		if !e.probed {
			e.gate, e.info = r.Gateway.Probe(ctx)
		}
		r.Facts.GateProbed, r.Facts.Gate, r.Facts.GateInfo = true, e.gate, e.info
		switch e.gate {
		case hermes.GateOK:
			r.info("gateway pause check: OK (%s)", e.info)
		case hermes.GateBlocked:
			r.info("gateway pause check: BLOCKED (%s). The update will stop the gateway first and start it again afterwards.", e.info)
		default:
			r.info("gateway pause check: unknown (%s)", e.info)
		}
	}
	if r.Facts.DiskLow {
		return fmt.Errorf("%w: not enough free disk space", apperr.ErrPreflightStop)
	}
	return nil
}

// ---- 4 wait (§2.7 1-3, D2, D3) ----

const sessionsUnreadableID = "sessions unreadable"

func sessionLines(busy []hermes.Session) string {
	var b strings.Builder
	for _, s := range busy {
		fmt.Fprintf(&b, "\n%s  %s  (last activity %ds ago)", s.ID, s.Title, int(s.Idle.Seconds()))
	}
	return b.String()
}

// sessionLogLines is the log's version of sessionLines: no id, no title,
// only a running number and the idle time. A session-store read error shows
// as "unreadable" (its text is not a session).
func sessionLogLines(busy []hermes.Session) string {
	var b strings.Builder
	for i, s := range busy {
		if s.ID == sessionsUnreadableID {
			fmt.Fprintf(&b, "\nsession %d (state unreadable)", i+1)
			continue
		}
		fmt.Fprintf(&b, "\nsession %d (busy, last activity %ds ago)", i+1, int(s.Idle.Seconds()))
	}
	return b.String()
}

func (r *Run) busySessions(ctx context.Context) []hermes.Session {
	if r.Sessions == nil {
		return nil
	}
	busy, err := r.Sessions.Busy(ctx, r.Config.Tunables.IdleWindow())
	if err != nil {
		// never guess idle (rule 1)
		busy = append(busy, hermes.Session{Profile: "hermes", ID: sessionsUnreadableID, Title: err.Error()})
	}
	return busy
}

func stageWait(ctx context.Context, r *Run) error {
	l, err := r.Platform.Procs.List(ctx)
	if err != nil {
		return fmt.Errorf("process listing failed: %w", err)
	}
	counts := r.count(l)
	r.Facts.DesktopWas = counts[hermes.ClassDesktop] > 0
	if len(counts) == 0 {
		r.info("desktop app: not running, nothing to close")
		return nil
	}
	if !r.Facts.DesktopWas {
		r.info("desktop app: not running; %s will still be stopped", runningText(counts))
	}
	tun := r.Config.Tunables
	start := r.Clock.Now()
	forced := false
	for !forced {
		busy := r.busySessions(ctx)
		r.Facts.Busy = busy
		if len(busy) == 0 {
			break
		}
		head := fmt.Sprintf("%d active in the last %d min", len(busy), tun.IdleWindowS/60)
		r.rowsLog(ui.Row{Key: "sessions", Value: head + sessionLines(busy)}, head+sessionLogLines(busy))
		if r.Clock.Now().Sub(start) >= tun.IdleWaitMax() {
			r.stop("sessions still active after %d min; nothing was changed. Re-run when idle.", tun.IdleWaitMaxS/60)
			return fmt.Errorf("%w: sessions still active", apperr.ErrCancelled)
		}
		if r.Config.Run.Unattended {
			if err := r.Sleep(ctx, tun.IdlePoll()); err != nil {
				return err
			}
			continue
		}
		a, err := r.ask(ctx, "Waiting for them to finish. Press A to abort, F to close Hermes anyway (running turns will be cut off).",
			[]rune{'a', 'f', 'q', 'n', 0x1b}, tun.IdlePoll(), "checking again in")
		if err != nil {
			return err
		}
		switch {
		case a.Abort || a.Key == 'a' || a.Key == 'q' || a.Key == 'n' || a.Key == 0x1b:
			r.info("aborted by user; nothing was changed")
			return fmt.Errorf("%w: aborted by user", apperr.ErrCancelled)
		case a.Key == 'f':
			r.info("user chose to close Hermes despite active sessions")
			forced = true
		}
	}
	if r.Config.Run.Unattended || r.Config.Run.Yes {
		return nil
	}
	total := r.plannedTotal("close")
	text := fmt.Sprintf("Hermes will close for the update. The update takes %s. Press Y to continue now, any other key to abort (auto-abort in %ds).",
		withRough(fmt.Sprintf("about %d min", minutes(total)), r.roughNote("close")), tun.ConfirmTimeoutS)
	a, err := r.ask(ctx, text, []rune{'y', 'n', 'a', 'q', '\r', 0x1b}, tun.ConfirmTimeout(), "auto-cancel in")
	if err != nil {
		return err
	}
	if a.Key != 'y' {
		r.info("aborted (no confirmation); nothing was changed")
		return fmt.Errorf("%w: not confirmed", apperr.ErrCancelled)
	}
	return nil
}

// ---- 5 close (§2.7 4-6, §2.8 marker) ----

func stageClose(ctx context.Context, r *Run) error {
	r.runHooks(ctx, config.HookPreClose, "unknown")
	desk, err := r.procsOf(ctx, hermes.ClassDesktop)
	if err != nil {
		return err
	}
	if len(desk) > 0 {
		r.section("Close")
	}
	for _, p := range desk {
		r.info("closing desktop app (pid %d)", p.PID)
		if err := r.Platform.App.RequestClose(ctx, p.PID); err != nil {
			r.Log.Warn("close request failed", slog.Int("pid", p.PID), slog.String("error", err.Error()))
		}
	}
	deadline := r.Clock.Now().Add(r.Config.Tunables.GracefulClose())
	for len(desk) > 0 && r.Clock.Now().Before(deadline) {
		if err := r.Sleep(ctx, closePoll); err != nil {
			return err
		}
		if desk, err = r.procsOf(ctx, hermes.ClassDesktop); err != nil {
			return err
		}
	}
	if len(desk) > 0 {
		for _, p := range desk {
			r.info("desktop pid %d did not close gracefully; forcing", p.PID)
			if b := r.Platform.App.CloseBlocker(p.PID); b != "" {
				r.note("%s", b)
			}
			if err := r.Platform.App.ForceClose(ctx, p); err != nil {
				r.Log.Warn("force close failed", slog.Int("pid", p.PID), slog.String("error", err.Error()))
			}
		}
		if err := r.Sleep(ctx, afterForceWait); err != nil {
			return err
		}
		if desk, err = r.procsOf(ctx, hermes.ClassDesktop); err != nil {
			return err
		}
		if len(desk) > 0 {
			r.stop("desktop app did not exit")
			return fmt.Errorf("%w: the desktop app did not exit", apperr.ErrCancelled)
		}
	}
	left, err := r.procsOf(ctx, hermes.ClassBackend, hermes.ClassKernel)
	if err != nil {
		return err
	}
	for _, p := range left {
		r.info("stopping leftover %s pid %d", r.classify(p), p.PID)
		if err := r.Platform.Procs.KillTree(ctx, p); err != nil {
			r.warn("could not stop pid %d: %v", p.PID, err)
		}
	}
	r.Facts.Closed = true
	r.Facts.Estimate = r.plannedTotal("close")
	r.Facts.TClose = r.Clock.Now()
	r.claimMarker()
	return nil
}

// claimMarker writes the marker only now (rule 5) and keeps it fresh.
func (r *Run) claimMarker() {
	if r.Marker == nil {
		return
	}
	if err := r.Marker.Claim(); err != nil {
		r.warn("could not write the update marker: %v", err)
		return
	}
	r.claimed = true
	every := r.Config.Tunables.MarkerRefresh()
	if every <= 0 {
		return
	}
	done := make(chan struct{})
	t := time.NewTicker(every)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = r.Marker.Refresh()
			}
		}
	}()
	r.stopRefresh = func() { t.Stop(); close(done) }
}

// ---- 6 hermes update (§2.8) ----

func stageUpdate(ctx context.Context, r *Run) error {
	blocked := false
	if r.Facts.Gate == hermes.GateBlocked {
		ok := r.clearPauseGate(ctx, "blocked in pre-flight; stopping the gateway first")
		blocked = !ok
		r.Facts.GateStopped = ok
		if ok {
			r.Facts.GateLine = "stopped before the update (its lock could not be mapped), restarted after"
		} else {
			r.Facts.GateLine = "blocked: the gateway could not be stopped cleanly; nothing changed"
		}
	}
	rc, out := 1, ""
	if blocked {
		r.info("aborted before `hermes update` ran; nothing was changed")
	} else {
		var err error
		rc, out, err = r.runUpdate(ctx)
		if err != nil {
			r.Facts.UpdateRC, r.Facts.UpdateOutput = rc, out
			r.warn("interrupted by user during the update; the updater's own rollback/receipt applies")
			return fmt.Errorf("%w: interrupted during the update (%v)", apperr.ErrUpdateProblem, err)
		}
		r.info("hermes update exit code: %d", rc)
	}
	if rc != 0 && !r.Facts.GateStopped {
		if m := hermes.PauseGateRe.FindStringSubmatch(out); m != nil && r.nothingChanged(ctx, out) {
			r.info("`hermes update` stopped at its gateway pause before changing anything: %s", m[1])
			if r.clearPauseGate(ctx, "retrying once with the gateway stopped") {
				r.Facts.GateStopped = true
				r.Facts.GateLine = "update retried with the gateway stopped (its lock could not be mapped)"
				var err error
				rc, out, err = r.runUpdate(ctx)
				if err != nil {
					r.Facts.UpdateRC, r.Facts.UpdateOutput = rc, out
					return fmt.Errorf("%w: interrupted during the update (%v)", apperr.ErrUpdateProblem, err)
				}
				r.info("hermes update exit code (retry): %d", rc)
			} else {
				r.Facts.GateLine = "blocked: the gateway could not be stopped cleanly; nothing changed"
			}
		}
	}
	r.Facts.UpdateRC, r.Facts.UpdateOutput = rc, out
	if m := hermes.UpdateCompleteRe.FindStringSubmatch(out); m != nil {
		r.Facts.Versions = [2]string{m[1], m[2]}
	}
	return nil
}

// nothingChanged: HEAD is still the pre-flight HEAD and the updater never
// got to fetching.
func (r *Run) nothingChanged(ctx context.Context, out string) bool {
	if r.Facts.HeadFull == "" || r.Repo == nil || strings.Contains(out, hermes.FetchingMarker) {
		return false
	}
	_, full, err := r.Repo.Head(ctx)
	return err == nil && full == r.Facts.HeadFull
}

// displayLine filters `hermes update` output for the screen ("" = hide).
func displayLine(line string) string {
	switch {
	case strings.Contains(line, hermes.CuaDeferred):
		return "→ cua-driver (Computer Use) refresh: handled by the safe updater after verification"
	case strings.Contains(line, hermes.CuaManualHint):
		return ""
	}
	for _, h := range hermes.HiddenUpdateLines {
		if strings.Contains(line, h) {
			return ""
		}
	}
	return line
}

func (r *Run) runUpdate(ctx context.Context) (int, string, error) {
	argv := append([]string{r.Facts.Install.Launcher}, hermes.UpdateArgv...)
	cmdline := "hermes " + strings.Join(hermes.UpdateArgv, " ")
	r.UI.Command(cmdline)
	r.Log.Info("== Running: " + cmdline)

	last := r.Clock.Now()
	var lastMu = &r.mu
	stopWatch := make(chan struct{})
	go func() { // silence watchdog: one note per silent stretch
		t := time.NewTicker(watchdogPoll)
		defer t.Stop()
		warned := false
		for {
			select {
			case <-stopWatch:
				return
			case <-t.C:
				lastMu.Lock()
				quiet := r.Clock.Now().Sub(last)
				lastMu.Unlock()
				if quiet > r.Config.Tunables.SilentWarn() && !warned {
					warned = true
					r.info("  (no output for %ds: builds can be silent for a long time; still waiting)", int(quiet.Seconds()))
				} else if quiet < r.Config.Tunables.SilentWarn() {
					warned = false
				}
			}
		}
	}()
	defer close(stopWatch)

	// The child shares this console, so a Ctrl+C reaches `hermes update`
	// directly and its own rollback/receipt applies (as in the Python). Our
	// context must not hard-kill it mid-pull: only the timeout may, and then
	// the whole tree goes (Python kill_tree), not just the launcher.
	res, err := r.Runner.Stream(context.WithoutCancel(ctx), execx.Cmd{
		Argv: argv, Dir: r.Facts.Install.Home, Timeout: r.Config.Tunables.UpdateTimeout(), ShowWindow: true, Env: utf8Env,
		KillTree: func(pid int) error {
			return r.Platform.Procs.KillTree(context.WithoutCancel(ctx), platform.Process{PID: pid})
		},
	}, func(line string) {
		lastMu.Lock()
		last = r.Clock.Now()
		lastMu.Unlock()
		if step := hermes.TriggerFor(line); step != "" {
			r.stage(step)
		}
		r.Log.Info("  | " + line)
		if shown := displayLine(line); shown != "" || line == "" {
			if strings.TrimSpace(shown) != "" {
				r.UI.Output(shown)
			}
		}
	})
	if res.TimedOut {
		r.stop("update exceeded %d min; it was stopped", r.Config.Tunables.UpdateTimeoutS/60)
	}
	code := res.Code
	if code == 0 && res.TimedOut {
		code = execx.CodeTimeout
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err() // Ctrl+C arrived while it ran
	}
	return code, res.Output, err
}

// clearPauseGate stops the gateway so `hermes update` can map its lock
// (§2.5); on failure the gateway is started again.
func (r *Run) clearPauseGate(ctx context.Context, reason string) bool {
	r.info("gateway pause gate: %s", reason)
	if r.Gateway == nil {
		return false
	}
	if err := r.Gateway.Stop(ctx); err != nil {
		r.stop("gateway could not be stopped: %v", err)
		r.ensureGatewayRunning(ctx)
		return false
	}
	gate, info := r.Gateway.Probe(ctx)
	if gate == hermes.GateBlocked {
		r.stop("the update would still be blocked after stopping the gateway: %s", info)
		r.ensureGatewayRunning(ctx)
		return false
	}
	if gate == hermes.GateUnknown {
		r.info("gateway pause gate: clear (probe unavailable: %s; the updater decides)", info)
	} else {
		r.info("gateway pause gate: clear")
	}
	return true
}

// ensureGatewayRunning starts the gateway when none runs (abort paths).
func (r *Run) ensureGatewayRunning(ctx context.Context) {
	if r.Gateway == nil {
		return
	}
	if gws, err := r.procsOf(ctx, hermes.ClassGateway); err == nil && len(gws) > 0 {
		return
	}
	if err := r.Gateway.Start(ctx); err != nil {
		r.warn("gateway could not be restarted: %v (run: hermes gateway start --all)", err)
		return
	}
	r.info("gateway restarted")
}

// ---- 7 verify (§2.9, D17) ----

func stageVerify(ctx context.Context, r *Run) error {
	r.section("Verify")
	var head string
	if r.Repo != nil {
		_, head, _ = r.Repo.Head(ctx)
	}
	if r.Facts.GateStopped && r.Gateway != nil {
		if gws, err := r.procsOf(ctx, hermes.ClassGateway); err == nil && len(gws) == 0 {
			err := r.Gateway.Start(ctx)
			r.info("gateway start (it was stopped for the update): %s", errText(err))
		}
	}
	var st hermes.GatewayState
	alive := false
	if r.Gateway != nil {
		w := hermes.GatewayWait{
			State: r.Gateway.State, Alive: hermes.AliveFunc(r.Platform.Procs),
			Sleep: func(d time.Duration) { _ = r.Sleep(ctx, d) },
			Max:   r.Config.Tunables.VerifyWait(), Poll: gatewayPoll,
		}
		st, alive = w.Wait(ctx, head)
		if !alive {
			r.info("gateway not running; starting it")
			err := r.Gateway.Start(ctx)
			r.info("gateway start: %s", errText(err))
			_ = r.Sleep(ctx, lateStartWait)
			st = r.Gateway.State()
			alive = hermes.AliveFunc(r.Platform.Procs)(st.PID)
		}
	}
	r.rows(ui.Row{Key: "gateway", Value: gatewayText(head, st, alive)})
	vok, vout := false, "no verifier"
	if r.Verifier != nil {
		vok, vout = r.Verifier.VerifyDesktop(ctx)
	}
	vrc := 0
	if vok {
		r.rows(ui.Row{Key: "desktop app", Value: "build verified"})
	} else {
		vrc = 1
		r.rows(ui.Row{Key: "desktop app", Value: "not verified: " + execx.Tail(vout, 300)})
	}
	res := hermes.EvaluateUpdate(hermes.VerifyInput{
		RC: r.Facts.UpdateRC, Output: r.Facts.UpdateOutput, Head: head, Gateway: st, Alive: alive, VerifyRC: vrc,
	})
	r.Facts.Verified = res.OK
	r.Facts.VerifyLine = res.Line
	if res.OK {
		r.say(ui.LevelOK, "", "%s", res.Line)
	} else {
		r.say(ui.LevelResult, "", "%s. Full log: %s", res.Line, r.LogPath)
	}
	return nil
}

func errText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func short(s string, n int) string {
	if s == "" {
		return "?"
	}
	return s[:min(n, len(s))]
}

func gatewayText(head string, st hermes.GatewayState, alive bool) string {
	if head != "" && st.CodeSHA == head && alive {
		return "running the new code " + short(head, 10)
	}
	return fmt.Sprintf("code %s, expected %s, alive %t", short(st.CodeSHA, 10), short(head, 10), alive)
}

// ---- 8 settings ----

func stageSettings(ctx context.Context, r *Run) error {
	r.section("Settings")
	after := hermes.CurrentVersion(ctx, r.Runner, r.Facts.Install.Launcher)
	if after == "" {
		after = r.Facts.Versions[1] // "Update complete! (a → b)"
	}
	r.Settings.SetHermesVersions(r.Facts.VersionBefore, after)
	rep, err := r.Settings.Migrate(ctx)
	if err != nil {
		var oe *settings.OriginError
		if errors.As(err, &oe) {
			// the error text already names the backup and the exact command
			r.warn("desktop settings migration failed: %v", err)
			return nil
		}
		r.warn("desktop settings migration failed: %v (the update itself is fine; run: hermes-safe-update migrate-settings)", err)
		return nil
	}
	r.Facts.Settings = &rep
	if rep.Line != "" {
		r.rows(ui.Row{Key: "settings", Value: rep.Line})
	}
	return nil
}

// ---- 9 relaunch (§2.10) ----

func (r *Run) relaunchDue() bool {
	return r.Facts.Closed && r.Facts.DesktopWas && !r.Config.Run.NoRelaunch && !r.relaunchTried
}

func stageRelaunch(ctx context.Context, r *Run) error {
	r.relaunch(ctx)
	if r.Facts.Relaunched {
		r.runHooks(ctx, config.HookPostRelaunch, resultWord(r))
	}
	return nil
}

func (r *Run) relaunch(ctx context.Context) {
	r.relaunchTried = true
	defer func() { r.Facts.TReopen = r.Clock.Now() }()
	exe := r.Facts.Install.Desktop
	if exe == "" || !r.FileExists(exe) {
		r.warn("desktop exe missing: %s (run: hermes desktop --force-build)", exe)
		return
	}
	if ok, why := r.Platform.App.CanLaunch(exe); !ok {
		r.note("desktop app not reopened: %s", why)
		return
	}
	if err := r.Platform.App.Launch(ctx, exe, nil); err != nil {
		r.warn("could not reopen the desktop app: %v", err)
		return
	}
	r.Facts.Relaunched = true
	r.info("relaunched Hermes desktop")
}

// ---- 10 cleanup, 11 cua (§2.11) ----

func stageCleanup(ctx context.Context, r *Run) error {
	if !r.Facts.Verified {
		r.info("post-update cleanup and cua-driver refresh skipped: update not verified")
		return nil
	}
	r.section("Dependency generation cleanup")
	res, err := r.GC.Run(ctx, false)
	switch {
	case err != nil:
		r.warn("generation cleanup failed: %v (the update itself is unaffected)", err)
	case res.Skipped != "":
		r.note("generation cleanup skipped: %s", res.Skipped)
	}
	if err == nil {
		r.Facts.GC = &res
		if res.Line != "" {
			r.rows(ui.Row{Key: "removed", Value: res.Line})
		}
	}
	return nil
}

func stageCUA(ctx context.Context, r *Run) error {
	res := r.CUA.Refresh(ctx, cua.Options{
		UpdateOutput: r.Facts.UpdateOutput, Unattended: r.Config.Run.Unattended, NoElevate: r.Config.Run.NoElevate,
	})
	if !res.Ran {
		return nil
	}
	r.Facts.CUA = &res
	if res.OK {
		r.rows(ui.Row{Key: "cua-driver", Value: res.Line})
	} else {
		r.warn("cua-driver: %s", res.Line)
	}
	return nil
}

// ---- 12 hooks ----

func resultWord(r *Run) string {
	if r.Facts.Verified {
		return "verified"
	}
	return "problem"
}

// stageHooks runs the post-update hooks: after a verified update, with
// Hermes still closed, before the relaunch stage. post-relaunch hooks run
// at the end of stageRelaunch.
func stageHooks(ctx context.Context, r *Run) error {
	if r.Facts.Verified {
		r.runHooks(ctx, config.HookPostUpdate, resultWord(r))
	}
	return nil
}

// runHooks runs one point's hooks; problems are WARN rows only.
func (r *Run) runHooks(ctx context.Context, when config.HookWhen, result string) {
	if r.Hooks == nil {
		return
	}
	outs := r.Hooks.Run(ctx, when, map[string]string{
		"HERMES_SAFE_UPDATE_STAGE": string(when), "HERMES_SAFE_UPDATE_RESULT": result,
	})
	for _, o := range outs {
		switch {
		case o.TimedOut:
			r.warn("hook %s timed out after %s", o.Name, fmtDuration(o.Took))
		case o.ExitCode != 0:
			r.warn("hook %s failed (exit %d): %s", o.Name, o.ExitCode, o.LastLine)
		}
		val := o.LastLine
		if val == "" {
			val = "done"
		}
		if o.TimedOut {
			val = "timed out"
		}
		row := ui.Row{Key: o.Name, Value: val}
		r.rows(row)
		r.Facts.HookRows = append(r.Facts.HookRows, row)
	}
}

// ---- 13 summary (§2.12) ----

func (r *Run) sourceLine() string {
	switch r.Facts.Check.Source {
	case "mirror":
		age := "never refreshed"
		if st := r.Facts.MirrorState; st != nil && !st.LastOK.IsZero() {
			age = ageText(r.Clock.Now().Sub(st.LastOK))
		}
		return "local mirror, " + age
	case "api":
		return "GitHub (checked via the API)"
	case "git":
		return "GitHub (git fetch)"
	}
	return ""
}

func ageText(d time.Duration) string {
	switch m := int(d.Minutes()); {
	case m < 1:
		return "refreshed just now"
	case m < 120:
		return fmt.Sprintf("refreshed %d min ago", m)
	default:
		return fmt.Sprintf("refreshed %d h ago", m/60)
	}
}

func addRow(rows []ui.Row, k, v string) []ui.Row {
	if v == "" {
		return rows
	}
	return append(rows, ui.Row{Key: k, Value: v})
}

func stageUpdateSummary(ctx context.Context, r *Run) error {
	f := r.Facts
	if r.Facts.TReopen.IsZero() {
		r.Facts.TReopen = r.Clock.Now()
	}
	r.complete(f.Verified)
	headline := "Hermes is updated and verified."
	if !f.Verified {
		headline = "The update needs attention: see RESULT above."
	}
	var rows []ui.Row
	if f.Versions[0] != "" {
		v := f.Versions[0] + " → " + f.Versions[1]
		if f.Check.Commits != nil {
			v += fmt.Sprintf("  (%d commits)", *f.Check.Commits)
		}
		rows = addRow(rows, "Version", v)
	}
	if !f.TClose.IsZero() {
		took := r.Facts.TReopen.Sub(f.TClose)
		t := fmtDuration(took) + " for the update"
		if f.Relaunched {
			t = fmtDuration(took) + " until Hermes reopened"
		}
		if f.Estimate > 0 {
			t += fmt.Sprintf("  (estimate was %s)", fmtDuration(f.Estimate))
		}
		rows = addRow(rows, "Time", t)
	}
	rows = addRow(rows, "Source", r.sourceLine())
	if f.Settings != nil {
		rows = addRow(rows, "Settings", f.Settings.Line)
	}
	if f.GC != nil {
		rows = addRow(rows, "Cleanup", f.GC.Line)
	}
	if f.CUA != nil {
		rows = addRow(rows, "cua-driver", f.CUA.Line)
	}
	rows = addRow(rows, "Gateway", f.GateLine)
	rows = append(rows, f.HookRows...)
	if f.Warnings > 0 {
		rows = addRow(rows, "Warnings", fmt.Sprintf("%d (see above)", f.Warnings))
	}
	rows = addRow(rows, "Log", r.LogPath)
	r.summary(ui.Card{OK: ptr(f.Verified), Headline: headline, Rows: rows})
	r.offerMirror(ctx)
	if !f.Verified {
		return fmt.Errorf("%w: %s", apperr.ErrUpdateProblem, f.VerifyLine)
	}
	return nil
}

func (r *Run) summary(c ui.Card) {
	r.summaryShown = true
	r.Log.Info("SUMMARY " + c.Headline)
	for _, row := range c.Rows {
		v := row.Value
		if lv, ok := r.logRows[row.Key]; ok {
			v = lv
		}
		r.Log.Info("SUMMARY   " + row.Key + ": " + v)
	}
	r.UI.Summary(c)
}

// ---- check-only stages (§2.4) ----

func stageSessions(ctx context.Context, r *Run) error {
	busy := r.busySessions(ctx)
	r.Facts.Busy = busy
	if len(busy) == 0 {
		r.rows(ui.Row{Key: "sessions", Value: "none active"})
	} else {
		head := fmt.Sprintf("%d active", len(busy))
		r.rowsLog(ui.Row{Key: "sessions", Value: head + sessionLines(busy)}, head+sessionLogLines(busy))
	}
	return nil
}

// versionValue is the head, plus a gateway note only when the running
// gateway is on a different commit (first 8 characters, like the Python UI).
func versionValue(head, gatewaySHA string) string {
	if head[:min(8, len(head))] == gatewaySHA[:min(8, len(gatewaySHA))] {
		return head
	}
	return fmt.Sprintf("%s (gateway on %s)", head, gatewaySHA)
}

func stageGCPreview(ctx context.Context, r *Run) error {
	r.section("Dependency generation cleanup (dry run)")
	res, err := r.GC.Run(ctx, true)
	if err != nil {
		r.warn("generation cleanup preview failed: %v", err)
		return nil
	}
	r.Facts.GCPreview = &res
	for _, l := range res.Lines {
		r.info("  %s", strings.TrimSpace(l))
	}
	return nil
}

func stageCUAStatus(ctx context.Context, r *Run) error {
	st := r.CUA.Status(ctx)
	r.Facts.CUAStatus = &st
	r.rows(ui.Row{Key: "cua-driver", Value: "logon task " + st.Text})
	return nil
}

func stageCheckSummary(ctx context.Context, r *Run) error {
	f := r.Facts
	r.info("Pre-flight passed; check mode, Hermes was not touched.")
	r.complete(true)
	var rows []ui.Row
	avail := "yes"
	if f.Check.Commits != nil {
		avail = fmt.Sprintf("%d new commit(s)", *f.Check.Commits)
	}
	rows = addRow(rows, "Available", avail)
	rows = addRow(rows, "Python deps", kindText(f.Check.PyDeps, "changed, will be reinstalled"))
	rows = addRow(rows, "Node packages", kindText(f.Check.NPM, "changed, will be reinstalled (slower build)"))
	rows = addRow(rows, "Checked via", r.sourceLine())
	var hist []timings.Record
	r.withEst(func(e *timings.Estimator) { hist = e.History })
	plan := timings.NewEstimator(timings.KindUpdate, timings.UpdateSteps(r.Config.Tunables.StepSeconds), hist, r.Config.Tunables, r.Clock)
	plan.SetConditions(timings.Conditions{Source: f.Check.Source, Mirror: ptr(f.Check.Source == "mirror"), NPM: f.Check.NPM, PyDeps: f.Check.PyDeps, Commits: f.Check.Commits})
	if f.Check.PyDeps != nil {
		plan.ExpectSkip("pydeps", !*f.Check.PyDeps)
	}
	rows = addRow(rows, "Update time", withRough(fmt.Sprintf("about %d min once Hermes closes", minutes(plan.PlannedTotal("close"))), plan.RoughNote("close")))
	rows = addRow(rows, "Running", runningText(f.Procs))
	if f.GateProbed {
		switch f.Gate {
		case hermes.GateOK:
			rows = addRow(rows, "Gateway", "update can pause it")
		case hermes.GateBlocked:
			rows = addRow(rows, "Gateway", "lock can't be mapped: the update will stop it first")
		default:
			rows = addRow(rows, "Gateway", "pause check unavailable (see log)")
		}
	}
	s := fmt.Sprintf("%d active in the last %d min", len(f.Busy), r.Config.Tunables.IdleWindowS/60)
	if len(f.Busy) > 0 {
		s += " (the update waits for them)"
	}
	rows = addRow(rows, "Sessions", s)
	if g := f.GCPreview; g != nil {
		switch {
		case g.Skipped != "":
			rows = addRow(rows, "Cleanup", "skipped: "+g.Skipped)
		case g.Count > 0:
			rows = addRow(rows, "Cleanup", fmt.Sprintf("%d old dependency set(s), ~%d MB to free", g.Count, g.MB))
		default:
			rows = addRow(rows, "Cleanup", "nothing to remove yet")
		}
	}
	if f.CUAStatus != nil {
		rows = addRow(rows, "cua-driver", f.CUAStatus.Text)
	}
	if st := f.MirrorState; st != nil {
		if st.OK {
			var tail string
			if !st.LastOK.IsZero() {
				tail += ", " + ageText(r.Clock.Now().Sub(st.LastOK))
			}
			if !st.JobActive {
				tail += "; no background refresh job"
			}
			rows = addRow(rows, "Mirror", st.ConfiguredPath+tail)
			// the folder is the user's choice and may be personal
			r.logRows = map[string]string{"Mirror": "set up" + tail}
		} else {
			rows = addRow(rows, "Mirror", "none (optional; updates check GitHub directly)")
		}
	}
	r.summary(ui.Card{OK: nil, Headline: "Update available. Hermes was not touched.", Rows: rows})
	r.offerMirror(ctx)
	return nil
}

// ---- mirror offer (§2.12) ----

func (r *Run) offerMirror(ctx context.Context) {
	if r.Mirror == nil || !r.Config.Mirror.Offer || ctx.Err() != nil {
		return
	}
	st := r.Mirror.Status()
	if st.Complete() || st.Declined {
		return
	}
	if r.Config.Run.Unattended || !r.Platform.Console.IsTerminal() {
		r.note("no local update mirror here; run `hermes-safe-update mirror setup` to speed up updates")
		return
	}
	var text, logText string
	if st.OK {
		const f = "Keep the local update mirror current in the background (a script-only Hermes job every 4 hours, no agent, nothing uploaded; mirror at %s)?"
		text = fmt.Sprintf(f, st.ConfiguredPath)
		logText = fmt.Sprintf(f, "<mirror folder>")
	} else {
		path, gb, why := r.Mirror.Location(ctx)
		if path == "" {
			r.note("a local update mirror would make updates faster, but %s; not offered", why)
			return
		}
		text = fmt.Sprintf("Optional: make future updates faster with a local mirror of the Hermes source (%.1f GB at %s; %s). "+
			"One-time 10-20 min download in its own window, then a script-only job keeps it current every 4 hours; "+
			"nothing is uploaded, and updates work the same without it.", gb, path, why)
		logText = fmt.Sprintf("Optional: make future updates faster with a local mirror of the Hermes source (%.1f GB at <mirror folder>; %s).", gb, why)
	}
	const keysText = " Press S to set it up, N to never ask again, any other key for not now."
	text += keysText
	logText += keysText
	a, err := r.askLog(ctx, text, logText, []rune{'s', 'n', 'y', 'q', '\r', 0x1b}, time.Duration(r.Config.Mirror.OfferTimeoutS)*time.Second, "skipping in")
	if err != nil {
		return
	}
	switch a.Key {
	case 's':
		if r.SpawnMirrorSetup == nil {
			r.info("run `hermes-safe-update mirror setup` to set it up")
			return
		}
		if err := r.SpawnMirrorSetup(ctx); err != nil {
			r.warn("mirror setup could not start: %v", err)
			return
		}
		r.info("mirror setup started in its own window")
	case 'n':
		if err := r.Mirror.Decline(); err != nil {
			r.warn("could not save the answer: %v", err)
			return
		}
		r.info("mirror setup: won't ask again (run `hermes-safe-update mirror setup` any time)")
	default:
		r.info("mirror setup: skipped for now")
	}
}
