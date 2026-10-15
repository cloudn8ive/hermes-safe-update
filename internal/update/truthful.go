package update

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// Truthful verify (v0.4.0): what the run reports about the gateway and the
// launcher after the update, and the few things it does about them. None of
// the rows below changes Verified; Verified moves only through
// hermes.EvaluateUpdate (a tolerated relaunch exit, a stale gateway that was
// restarted onto the new code).

// GatewayFacts is what the verify and abort paths learned about the gateway.
type GatewayFacts struct {
	// TStop is when Hermes began stopping the gateway ("Stopping Windows
	// gateway"); the downtime of a rescue counts from there.
	TStop time.Time

	// HeldWhy is Hermes' reason when it left the paused gateways stopped on
	// purpose; Safe Update then does not start them.
	HeldWhy string
	// NotStarted is why Safe Update did not start a gateway that was down
	// (Hermes' hold, or a half-updated tree).
	NotStarted string

	// Rescued: the gateway was down and Safe Update started it although it
	// had not stopped it. Via is "gateway start" or "Task Scheduler (name)";
	// Down is how long it was down (0 = unknown); RescueErr is why the start
	// did not work.
	Rescued   bool
	Via       string
	Down      time.Duration
	RescueErr string

	// Stale: the gateway was alive after the update but still on the old
	// code StaleSHA. StaleRestarted: Safe Update restarted it; StaleOK: it
	// then reported the new code; StaleErr: why the restart did not happen;
	// StaleStartErr: it was stopped, but starting it again failed.
	Stale          bool
	StaleSHA       string
	StaleRestarted bool
	StaleOK        bool
	StaleErr       string
	StaleStartErr  string

	// Tolerated is the Hermes line of an exit 1 that counts as OK because
	// only Hermes' own gateway relaunch check failed (#123971).
	Tolerated string
}

// LauncherFacts is what the post-update launcher check found.
type LauncherFacts struct {
	// Wired is the launcher path the run's services were built with; the
	// route maps it to wherever the launcher is now.
	Wired string
	// Was is the launcher that disappeared; Now is where Hermes is run from
	// instead ("" with Direct = through the managed Python).
	Was    string
	Now    string
	Direct bool
	// Lost: nothing could run Hermes after the update (Err says why).
	Lost bool
	Err  string
}

// downText renders a downtime for the Gateway rescue row.
func downText(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	return fmt.Sprintf("about %d min", minutes(d))
}

// updateComplete: the update ran to its end (exit 0, or "Update complete!").
func (r *Run) updateComplete() bool {
	return r.Facts.UpdateRC == 0 || strings.Contains(r.Facts.UpdateOutput, hermes.UpdateDoneMarker)
}

// gatewayStartAllowed decides whether Safe Update may start a gateway that is
// down after `hermes update` ran. Hermes' hold says no: it left the paused
// gateways stopped on purpose and restarts them with its next command. With
// torn set, a half-finished update also says no: the update stopped partway
// after the code moved, so a gateway started now could boot on a half-written
// tree. A failure before HEAD moved is fine either way: the old code is whole.
// The verify stage sets torn only for an update that did not finish (non-zero
// exit and no "Update complete!"); a clean exit is never torn. The abort paths
// and the stale restart always set it. why is a sentence for the summary.
func (r *Run) gatewayStartAllowed(ctx context.Context, torn bool) (ok bool, why string) {
	f := &r.Facts
	if !f.UpdateStarted {
		return true, ""
	}
	if held, isHeld := hermes.ParseGatewayHeld(f.UpdateOutput); isHeld {
		f.GW.HeldWhy = held
		return false, "Hermes left it stopped on purpose: " + held + "; its next hermes command restarts it"
	}
	if !torn || r.updateComplete() {
		return true, ""
	}
	if r.headMoved(ctx) {
		return false, "the update stopped partway after the code changed, so a gateway started now could boot on a half-updated tree; run `hermes update` again, then `hermes gateway start` if it is still down"
	}
	return true, ""
}

// headMoved reports whether HEAD differs from the pre-flight HEAD. Unknown
// (no pre-flight HEAD, git failed) counts as moved: the guard errs on the side
// of not starting.
func (r *Run) headMoved(ctx context.Context) bool {
	if r.Facts.HeadFull == "" || r.Repo == nil {
		return true
	}
	_, full, err := r.Repo.Head(ctx)
	return err != nil || full != r.Facts.HeadFull
}

// startedVia is how the last gateway start worked, when the gateway can say.
func (r *Run) startedVia() string {
	if v, ok := r.Gateway.(interface{ StartedVia() string }); ok {
		return v.StartedVia()
	}
	return ""
}

// restartStale restarts a gateway that is alive but still reports the old
// code after the update (Hermes never relaunched it, or its relaunch was not
// seen), then waits for it to report the new code. Once only. The restart is
// skipped under the same guards as a start.
func (r *Run) restartStale(ctx context.Context, w hermes.GatewayWait, head string, st hermes.GatewayState) (hermes.GatewayState, bool) {
	g := &r.Facts.GW
	g.Stale, g.StaleSHA = true, st.CodeSHA
	alive := true
	if ok, why := r.gatewayStartAllowed(ctx, true); !ok {
		g.StaleErr = why
		r.warn("the gateway is still on the old code %s and was not restarted: %s", short(st.CodeSHA, 10), why)
		return st, alive
	}
	r.info("the gateway is alive but still on the old code %s (expected %s); restarting it once", short(st.CodeSHA, 10), short(head, 10))
	// Only this gateway: `gateway stop --all` would also stop the gateways of
	// other profiles, and a start that returns because "some gateway runs"
	// would then bring back none. StartAfterStale runs the plain `gateway
	// start` whatever else is running, and says so when nothing new came up.
	if err := r.Gateway.StopStale(ctx, st.PID); err != nil {
		g.StaleErr = "it could not be stopped: " + errText(err)
		r.warn("the stale gateway could not be stopped: %v (restart it with: hermes gateway restart)", err)
		return st, alive
	}
	// StopStale returns nil also when it found no gateway Safe Update
	// recognises with that pid, and then it stopped nothing: do not go on
	// (no start, no "stopped" claim) while that process is still alive.
	alivePID := hermes.AliveFunc(r.Platform.Procs)
	if alivePID(st.PID) {
		g.StaleErr = "it could not be stopped: its process is not one Safe Update recognises as the gateway"
		r.warn("the stale gateway is still running after the stop: %s (restart it with: hermes gateway restart)", g.StaleErr)
		return st, alive
	}
	if err := r.Gateway.StartAfterStale(ctx, st.PID); err != nil {
		st = r.Gateway.State()
		alive = alivePID(st.PID)
		if alive && head != "" && st.CodeSHA == head {
			// The start said no, yet the gateway is up on the new code (its
			// process was not recognised as new, or something else brought it
			// back): the row and the result say what is true.
			r.info("gateway start (it was on the old code) reported %v, but the gateway runs the new code", err)
			g.StaleOK = true
			return st, alive
		}
		g.StaleStartErr = errText(err)
		r.warn("the stale gateway was stopped but could not be started again: %v (start it with: hermes gateway start)", err)
		return st, alive
	}
	r.info("gateway start (it was on the old code): ok")
	g.StaleRestarted = true
	st, alive = w.Wait(ctx, head)
	g.StaleOK = alive && head != "" && st.CodeSHA == head
	return st, alive
}

// rescueGateway is the finally-step for a gateway that Safe Update did not
// stop but that is down after an abort: the run closed things, the verify
// stage never ran, and a gateway ran before. It starts it under the same
// guards as the verify stage. The outcome goes to Facts.GW; it never fails the
// run.
func (r *Run) rescueGateway(ctx context.Context) {
	f := &r.Facts
	if r.Gateway == nil || !f.GatewayBefore || !f.Closed || f.VerifyLine != "" || f.GateStopped {
		return
	}
	if gws, err := r.procsOf(ctx, hermes.ClassGateway); err != nil || len(gws) > 0 {
		return
	}
	if hermes.AliveFunc(r.Platform.Procs)(r.Gateway.State().PID) {
		return
	}
	if ok, why := r.gatewayStartAllowed(ctx, true); !ok {
		f.GW.NotStarted = why
		r.warn("the gateway is down and was not started: %s", why)
		return
	}
	r.startRescued(ctx)
}

// startRescued starts the gateway and records that Safe Update, not Hermes,
// brought it back.
func (r *Run) startRescued(ctx context.Context) {
	f := &r.Facts
	err := r.Gateway.Start(ctx)
	if err != nil {
		f.GW.RescueErr = errText(err)
		r.warn("gateway could not be restarted: %v (start it with: hermes gateway start)", err)
		return
	}
	f.GW.Rescued, f.GW.Via = true, r.startedVia()
	start := f.GW.TStop
	if start.IsZero() {
		start = f.TClose
	}
	if !start.IsZero() {
		f.GW.Down = r.Clock.Now().Sub(start)
	}
	r.info("gateway rescue: restarted by Safe Update (%s)", f.GW.Via)
}

// gatewayRows are the summary rows about the gateway beyond the Gateway
// result row. All are informational (Attention); none changes Verified.
func (r *Run) gatewayRows() []ui.Row {
	f := &r.Facts
	g := f.GW
	var rows []ui.Row
	attn := ptr(ui.VerdictAttention)
	if g.Tolerated != "" {
		rows = append(rows, ui.Row{Key: "Gateway check", Verdict: attn,
			Value: "Hermes could not confirm its own relaunch of the gateway (hermes-agent #123971: " + g.Tolerated + "); Safe Update checked it itself: running the new code"})
	}
	if g.Stale {
		v := "was still on the old code " + short(g.StaleSHA, 10) + " after the update"
		switch {
		case g.StaleErr != "":
			v += "; not restarted: " + g.StaleErr
		case g.StaleStartErr != "":
			v += "; Safe Update stopped it but could not start it again: " + short(g.StaleStartErr, 160) + " (start it with: hermes gateway start)"
		case g.StaleOK:
			v += "; Safe Update restarted it and it now runs the new code"
		case g.StaleRestarted:
			v += "; Safe Update restarted it but it did not report the new code"
		}
		rows = append(rows, ui.Row{Key: "Stale gateway", Value: v, Verdict: attn})
	}
	switch {
	case g.Rescued:
		v := "restarted by Safe Update (Hermes left it stopped)"
		if strings.HasPrefix(g.Via, "Task Scheduler") {
			v += " through " + g.Via
		}
		if g.Down > 0 {
			v += "; down " + downText(g.Down)
		}
		rows = append(rows, ui.Row{Key: "Gateway rescue", Value: v, Verdict: attn})
	case g.NotStarted != "":
		rows = append(rows, ui.Row{Key: "Gateway rescue", Value: "not started: " + g.NotStarted, Verdict: attn})
	case g.RescueErr != "":
		rows = append(rows, ui.Row{Key: "Gateway rescue", Value: "could not be restarted: " + short(g.RescueErr, 160) + " (start it with: hermes gateway start)", Verdict: attn})
	}
	if p := hermes.DoubleRelaunchProfiles(f.UpdateOutput); len(p) > 0 {
		v := "Hermes relaunched the gateway for profile " + strings.Join(p, ", ") + " twice (hermes-agent #126821)"
		if len(p) > 1 {
			v = "Hermes relaunched the gateway for profiles " + strings.Join(p, ", ") + " twice (hermes-agent #126821)"
		}
		if f.Verified {
			v += "; the last launch survived and Safe Update verified the gateway"
		}
		rows = append(rows, ui.Row{Key: "Double launch", Value: v, Verdict: attn})
	}
	return rows
}

// ---- the launcher after the update ----

// routeInitial points the run's route at the launcher-free command when the
// install was located without a launcher.
func (r *Run) routeInitial(in hermes.Install) {
	r.Facts.Launcher.Wired = in.Launcher
	if len(in.LauncherPrefix) > 0 && r.Route != nil {
		r.Route.Set(in.Launcher, in.LauncherPrefix)
		r.note("no Hermes launcher is installed; Hermes runs through its managed Python")
	}
}

// reresolveLauncher runs after `hermes update`: the update can replace or
// delete the launcher (bin\hermes.exe), and everything after it (the gateway
// start, the desktop verify, the version read, cua) runs through it. If the
// launcher still exists nothing happens. Otherwise it is found again; when it
// is gone for good Hermes is run through its managed Python (the same command
// Hermes' dashboard uses), and the route sends every `hermes ...` command of
// the run there.
func (r *Run) reresolveLauncher(ctx context.Context) {
	f := &r.Facts
	in := f.Install
	if in.Launcher == "" {
		return
	}
	if len(in.LauncherPrefix) == 0 && r.FileExists(in.Launcher) {
		return
	}
	res, ok := r.Locator.(hermes.LauncherResolver)
	if !ok {
		return
	}
	launcher, prefix, err := res.ResolveLauncher(ctx, in)
	if err != nil {
		f.Launcher.Lost, f.Launcher.Err, f.Launcher.Was = true, err.Error(), in.Launcher
		r.warn("no Hermes launcher works after the update: %v", err)
		return
	}
	f.Launcher.Lost, f.Launcher.Err = false, ""
	if len(prefix) > 0 && f.Launcher.Wired != "" {
		// The launcher-free route stands in for the launcher the run was wired
		// with: every runner was built with that path, and the route rewrites
		// exactly that program. The resolver's first candidate may be another
		// (missing) path, which no route entry would match.
		launcher = f.Launcher.Wired
	}
	if launcher == in.Launcher && slices.Equal(prefix, in.LauncherPrefix) {
		return
	}
	f.Launcher.Was = in.Launcher
	f.Install.Launcher, f.Install.LauncherPrefix = launcher, prefix
	if p := hermes.ManagedPython(r.Platform.Paths, in.Home); p != "" {
		f.Install.Python = p
	}
	f.Launcher.Direct = len(prefix) > 0
	f.Launcher.Now = launcher
	if r.Route != nil {
		from := f.Launcher.Wired
		switch {
		case len(prefix) > 0:
			r.Route.Set(from, prefix)
		case launcher == from:
			r.Route.Clear()
		default:
			r.Route.Set(from, []string{launcher})
		}
	}
	if f.Launcher.Direct {
		r.warn("the Hermes launcher %s is gone after the update; Hermes now runs through its managed Python", f.Launcher.Was)
	} else {
		r.warn("the Hermes launcher %s is gone after the update; using %s", f.Launcher.Was, launcher)
	}
}

// launcherRow is the summary row about the launcher ("" = nothing to say).
func (r *Run) launcherRow() (ui.Row, bool) {
	l := r.Facts.Launcher
	attn := ptr(ui.VerdictAttention)
	switch {
	case l.Lost:
		return ui.Row{Key: "Launcher", Verdict: attn,
			Value: "none works after the update (" + short(l.Err, 160) + "); the checks that need it could not run"}, true
	case l.Now != "" && l.Direct:
		return ui.Row{Key: "Launcher", Verdict: attn,
			Value: l.Was + " is gone after the update; Safe Update ran Hermes through its managed Python"}, true
	case l.Now != "":
		return ui.Row{Key: "Launcher", Verdict: attn,
			Value: l.Was + " is gone after the update; Safe Update used " + l.Now}, true
	}
	return ui.Row{}, false
}
