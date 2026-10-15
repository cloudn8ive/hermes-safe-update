package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// Pre-flight stops of v0.4.0. Each one answers "is this update certain, or
// very likely, to fail?" with a read-only probe BEFORE the close stage, so a
// stop (exit 4) leaves Hermes running and nothing changed:
//
//   - running elevated (hermes-agent #126860): the update would create
//     admin-owned files in tools\ that the normal account cannot replace later;
//   - the checkout lock is held (#137186): `hermes update` would refuse after
//     Safe Update already closed Hermes;
//   - tools\ entries the current account cannot open (#126860): the update
//     would hit [WinError 5];
//   - ARM64 build tools missing (#134121, in the remote stage): the update
//     would start a Visual Studio installer in the middle of the dependency step.

// ---- elevation ----

// elevatedMessage explains the stop; the same words (as a WARN) are used by
// check, which does not stop.
const elevatedMessage = "Safe Update is running as administrator. hermes update would create admin-owned files in Hermes' tools folder that your normal account cannot update later. Run it from a normal terminal"

func (r *Run) checkElevation() error {
	if r.Platform == nil || r.Platform.Elevation == nil {
		return nil
	}
	elevated, err := r.Platform.Elevation.Elevated()
	if err != nil {
		r.Log.Info("elevation check unavailable", "error", err.Error())
		return nil // unknown is never a stop
	}
	if !elevated {
		return nil
	}
	switch {
	case r.Config.Run.AllowElevated:
		r.note("running as administrator (--allow-elevated)")
		return nil
	case r.Mode == ModeCheck:
		r.warn("%s. A real run stops here unless you pass --allow-elevated.", elevatedMessage)
		return nil
	}
	r.stop("%s, or pass --allow-elevated to go on anyway. Nothing was changed.", elevatedMessage)
	return fmt.Errorf("%w: running as administrator", apperr.ErrPreflightStop)
}

// ---- checkout lock ----

func (r *Run) checkCheckoutLock(ctx context.Context, staleMarker bool) error {
	if r.Platform == nil || r.Platform.Locks == nil {
		return nil
	}
	self := r.SelfPID
	if self <= 0 {
		self = os.Getpid()
	}
	lock := hermes.CheckoutLock{Probe: r.Platform.Locks, Procs: r.Platform.Procs,
		Created: platform.ProcessCreated, Now: r.Clock.Now, SelfPID: self}
	st := lock.Check(ctx, r.Facts.Install.Checkout)
	if st.Err == nil {
		r.lockProbedAt = r.Clock.Now()
	}
	switch {
	case st.Err != nil:
		// No answer (macOS/Linux have no probe; a real probe can fail): said
		// once per run, the later looks stay quiet.
		if !r.lockNotProbedLogged {
			r.lockNotProbedLogged = true
			r.Log.Info("checkout lock: not probed", "error", st.Err.Error())
		}
		return nil
	case st.Verdict == hermes.LockOK:
		r.Log.Info("checkout lock: free", "path", st.Path)
		return nil
	}
	head, more := lockStopText(st, staleMarker)
	r.Log.Info("checkout lock held", "path", st.Path, "owner_pid", st.OwnerPID, "owner_live", st.OwnerLive)
	r.stop("%s", head)
	for _, l := range more {
		r.info("%s", l)
	}
	return fmt.Errorf("%w: checkout lock held", apperr.ErrPreflightStop)
}

// lockStopText words the stop: a headline and detail lines.
func lockStopText(st hermes.LockStatus, staleMarker bool) (string, []string) {
	switch {
	case st.Verdict == hermes.LockUnopenable:
		return fmt.Sprintf("Hermes' checkout lock file cannot be opened (%s), so hermes update would refuse to start. Nothing was closed.", strings.TrimRight(st.Detail, ". ")),
			[]string{"Fix its permissions, or delete it when no update is running: " + st.Path}
	case st.HaveRecord && st.OwnerLive:
		who := fmt.Sprintf("pid %d", st.OwnerPID)
		if st.OwnerName != "" {
			who += ", " + st.OwnerName
		}
		return fmt.Sprintf("An update is running (%s, started %s ago): it holds Hermes' checkout lock. Wait for it to finish, then run Safe Update again. Nothing was closed.", who, lockAgeText(st.Age)), nil
	case st.HaveRecord:
		head := fmt.Sprintf("Hermes' checkout lock is still held: the update that took it (pid %d) has exited, but a process it started still holds it, so hermes update would refuse to start. Nothing was closed.", st.OwnerPID)
		if staleMarker {
			head += " The update marker is stale too."
		}
		more := []string{}
		if len(st.Candidates) > 0 {
			var parts []string
			for _, p := range st.Candidates {
				parts = append(parts, fmt.Sprintf("%s (pid %d)", p.Name, p.PID))
			}
			more = append(more, "Started by that update and still running: "+strings.Join(parts, ", "),
				"If they are leftovers, end them (Task Manager, or `taskkill /PID <pid> /T /F`), or wait until they exit; then run Safe Update again. Safe Update does not stop them for you.")
		} else {
			more = append(more, "Could not tell which process holds it. Look for python.exe, node.exe or git.exe left by an earlier update (Task Manager, Details tab, Command line column); restarting Windows also frees it. Then run Safe Update again.")
		}
		return head, more
	}
	return "Another update, or a process left behind by one, holds Hermes' checkout lock (its holder record is unreadable), so hermes update would refuse to start. Nothing was closed.",
		[]string{"Wait for it to finish or end the leftover process, then run Safe Update again. Lock file: " + st.Path}
}

// lockAgeText renders a duration as "3m 4s" (Hermes' own wording).
func lockAgeText(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	if s >= 60 {
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%ds", s)
}

// ---- ACL probe ----

// aclRepairMax is how many entries get a repair block (the issue's reporters
// had a handful; a long list would bury the message).
const aclRepairMax = 5

func (r *Run) checkACL(ctx context.Context) error {
	mode := r.Config.Preflight.ACLCheck
	if mode == config.PreflightOff || r.Platform == nil || r.Platform.Access == nil {
		return nil
	}
	in := r.Facts.Install
	rep := hermes.ACLChecker{Access: r.Platform.Access, Home: in.Home, Checkout: in.Checkout, Now: r.Clock.Now, Sleep: r.Sleep}.Run(ctx)
	if err := ctx.Err(); err != nil {
		return err // Ctrl+C: nothing was changed
	}
	r.Log.Info("acl check", "opens", rep.Opens, "stop", len(rep.Stop), "warn", len(rep.Warn), "incomplete", rep.Incomplete)
	if rep.Incomplete {
		r.info("ACL check stopped early after %d opens; deeper entries were not looked at", rep.Opens)
	}
	if len(rep.Stop) == 0 && len(rep.Warn) == 0 {
		return nil
	}
	tools := filepath.Join(in.Home, "tools")
	var names []string
	for _, f := range rep.Stop {
		names = append(names, aclName(tools, f.Path))
	}
	if len(rep.Stop) > 0 {
		r.rows(ui.Row{Key: "acl", Value: fmt.Sprintf("%d entr%s in tools\\ cannot be opened by this account: %s",
			len(rep.Stop), plural(len(rep.Stop), "y", "ies"), joinMax(names, 4))})
	}
	for _, f := range rep.Warn {
		what := "cannot be opened"
		if f.Mask == platform.AccessModify {
			what = "cannot be changed or deleted by this account (an antivirus folder guard does this too)"
		}
		r.warn("acl: %s %s", aclName(in.Home, f.Path), what)
	}
	// the repair block: read refusals only (a modify refusal has other causes)
	var fix []hermes.ACLFinding
	fix = append(fix, rep.Stop...)
	for _, f := range rep.Warn {
		if f.Mask == platform.AccessRead {
			fix = append(fix, f)
		}
	}
	stopping := len(rep.Stop) > 0 && mode == config.PreflightStop
	if len(fix) > 0 {
		lead := "To repair, run this in an elevated terminal (administrator), then run Safe Update again. /d answers Windows' yes question: use y, or j on a German Windows, or your language's yes letter:"
		if len(rep.Stop) > 0 && mode == config.PreflightWarn {
			lead = "The update is expected to fail on these (WinError 5). To repair, run this in an elevated terminal (administrator). /d answers Windows' yes question: use y, or j on a German Windows, or your language's yes letter:"
		}
		if stopping {
			r.stop("The update is expected to fail on tools\\ entries this account cannot open ([WinError 5]; left behind by an elevated install or update). Nothing was changed.")
		}
		r.info("%s", lead)
		for i, f := range fix {
			if i == aclRepairMax {
				r.info("  ... and %d more entr%s; repair these first, then run Safe Update again to see the rest", len(fix)-aclRepairMax, plural(len(fix)-aclRepairMax, "y", "ies"))
				break
			}
			for _, c := range hermes.RepairCommands(f.Path) {
				r.info("  %s", c)
			}
		}
	}
	if stopping {
		r.info("To go on anyway, set preflight.acl_check: \"warn\" in safe-update.yaml (the update is then expected to fail on these entries).")
		return fmt.Errorf("%w: %d tools entries cannot be opened", apperr.ErrPreflightStop, len(rep.Stop))
	}
	return nil
}

// aclName shows path relative to base when it lies under it.
func aclName(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func joinMax(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") + fmt.Sprintf(", and %d more", len(items)-limit)
}

// ---- ARM64 build tools ----

func (r *Run) checkARM64(ctx context.Context) error {
	mode := r.Config.Preflight.ARM64Toolchain
	c := r.Facts.Check
	if mode == config.PreflightOff || c.Behind == nil || !*c.Behind || (c.PyDeps != nil && !*c.PyDeps) {
		return nil // no update, or the Python dependencies stay as they are
	}
	python := r.Facts.Install.Python
	if r.ManagedPython != nil {
		python = r.ManagedPython()
	}
	if hermes.ManagedTarget(python) != hermes.ARM64Target || r.Getenv == nil || r.Runner == nil {
		return nil
	}
	rep := hermes.ARM64Tools{Getenv: r.Getenv, Exists: r.FileExists, Run: r.Runner}.Probe(ctx)
	switch {
	case rep.Unknown != "":
		r.Log.Info("arm64 build tools: not decided", "reason", rep.Unknown)
		return nil
	case !rep.Missing():
		r.Log.Info("arm64 build tools: found", "visual_studio", rep.VS, "clang", rep.Clang)
		return nil
	}
	head, fix := arm64Text(rep)
	if mode == config.PreflightStop {
		r.stop("%s Stopping before anything is closed (preflight.arm64_toolchain: stop). %s", head, fix)
		r.joinEarly(ctx)
		return fmt.Errorf("%w: ARM64 build tools missing", apperr.ErrPreflightStop)
	}
	r.warn("%s %s", head, fix)
	return nil
}

// arm64Text words what is missing and what to do first.
func arm64Text(rep hermes.ARM64Report) (head, fix string) {
	const tail = "The dependency step of this update will try to install Visual Studio Build Tools (several GB, an administrator approval prompt) after Hermes is closed, and can stall."
	switch {
	case rep.VS == "" && rep.Clang == "":
		return "ARM64 build tools: no Visual Studio with the ARM64 C++ tools and no clang.exe found. " + tail,
			"Fix first: install Visual Studio Build Tools with the MSVC ARM64 build tools, and LLVM (`winget install " + hermes.ARM64WingetLLVM + "`). Safe Update installs nothing."
	case rep.VS == "":
		return "ARM64 build tools: no Visual Studio with the ARM64 C++ tools found (clang.exe is there, but Hermes needs both). " + tail,
			"Fix first: install Visual Studio Build Tools with the MSVC ARM64 build tools. Safe Update installs nothing."
	}
	head = "ARM64 build tools: clang.exe not found."
	if rep.ClangFormatOnly != "" {
		head += " clang-format.exe is installed without it, so the Clang component looks half-installed."
	}
	return head + " " + tail,
		"Fix first: `winget install " + hermes.ARM64WingetLLVM + "` (reported to fix it; unverified by Safe Update, which installs nothing)."
}
