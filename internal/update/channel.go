package update

// Release-channel handling of a run: the early capability probe, the plan
// (which argv, which check), the remote stage on a release channel and the
// rollback guard. The wording Hermes prints is matched in
// internal/hermes/contracts.go only; the argv per mode in hermes/channel.go.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// chanProbe is what the early job learned about this Hermes.
type chanProbe struct {
	caps     hermes.Caps
	resolved hermes.Resolved
}

// startChannelProbe runs the capability probe (`update --help`, with the
// checkout's source files as the fallback) and, for mode follow on a
// channel-capable Hermes, the offline read of Hermes' own channel, beside the
// process listing. Mode main needs neither: its argv does not depend on the
// Hermes build.
func (r *Run) startChannelProbe(ctx context.Context) {
	mode := r.Config.Update.Channel
	if mode == config.ChannelMain {
		return
	}
	in := r.Facts.Install
	ch := make(chan chanProbe, 1)
	r.chanProbe = ch
	go func() {
		// Both calls are a Python start-up (seconds on a cold machine), so a
		// follow run reads the install id beside the help probe.
		caps, res := hermes.ProbeChannel(ctx, r.Runner, in.Launcher, in.Home, in.Checkout, mode != config.ChannelStable)
		ch <- chanProbe{caps: caps, resolved: res}
	}()
}

// channelPlan joins the early probe, builds the plan for the configured mode
// and logs the decision. Safe Update only reads Hermes' channel; it never
// writes it. A cancelled run ends here: the plan of an unfinished probe is
// never used (it would be a main plan, and the update must not follow).
func (r *Run) channelPlan(ctx context.Context) (hermes.Plan, error) {
	if err := ctx.Err(); err != nil {
		r.chanProbe = nil
		return hermes.Plan{}, err
	}
	p := chanProbe{resolved: hermes.Resolved{Source: hermes.SourceUnknown}}
	if r.chanProbe != nil {
		select {
		case p = <-r.chanProbe:
		case <-ctx.Done():
			r.chanProbe = nil
			return hermes.Plan{}, ctx.Err()
		}
		r.chanProbe = nil
	}
	mode := r.Config.Update.Channel
	plan := hermes.BuildPlan(mode, p.caps, p.resolved)
	r.Facts.Channel.Plan = plan
	r.logPlan("channel decision", plan)
	return plan, nil
}

func (r *Run) logPlan(msg string, plan hermes.Plan) {
	caps := "not probed"
	switch {
	case plan.Caps.Probed:
		caps = fmt.Sprintf("channel=%t install-id=%t", plan.Caps.Channel, plan.Caps.InstallID)
	case plan.Caps.Err != "":
		caps = "probe failed: " + plan.Caps.Err
	}
	r.Log.Info(msg,
		slog.String("mode", plan.Mode),
		slog.Bool("flag", r.Config.Run.ChannelFlag),
		slog.String("caps", caps),
		slog.String("caps_source", plan.Caps.Source),
		slog.String("help_error", plan.Caps.Err),
		slog.String("hermes_channel", plan.Resolved.Channel),
		slog.String("hermes_channel_source", string(plan.Resolved.Source)),
		slog.String("effective", plan.Effective),
		slog.Bool("release", plan.Release),
		slog.String("update_argv", "hermes "+strings.Join(plan.UpdateArgv, " ")),
		slog.String("check_argv", "hermes "+strings.Join(plan.CheckArgv, " ")),
		slog.String("reason", plan.Reason))
}

// updateArgs is the argv of `hermes update` after the launcher. The legacy
// main argv is only the fallback for a run that skipped the remote stage.
func (r *Run) updateArgs() []string {
	if a := r.Facts.Channel.Plan.UpdateArgv; len(a) > 0 {
		return a
	}
	return hermes.UpdateArgv
}

// noteCheck stores the pre-flight result and feeds the estimator.
func (r *Run) noteCheck(c hermes.UpdateCheck) {
	r.Facts.Check = c
	r.withEst(func(e *timings.Estimator) {
		e.SetConditions(timings.Conditions{Source: c.Source, Mirror: ptr(c.Source == "mirror"), NPM: c.NPM, PyDeps: c.PyDeps, Commits: c.Commits})
		if c.PyDeps != nil {
			e.ExpectSkip("pydeps", !*c.PyDeps)
		}
	})
}

// releaseCheck is one run of `hermes update --check` on a release channel.
type releaseCheck struct {
	res    execx.Result
	err    error
	parsed hermes.ChannelCheck
}

func (r *Run) runReleaseCheck(ctx context.Context, plan hermes.Plan) releaseCheck {
	argv := append([]string{r.Facts.Install.Launcher}, plan.CheckArgv...)
	res, err := r.Runner.Run(ctx, execx.Cmd{Argv: argv, Timeout: checkTimeout, Env: utf8Env})
	rc := releaseCheck{res: res, err: err}
	if err == nil {
		rc.parsed = hermes.ParseChannelCheck(res.Output)
	}
	return rc
}

// channelName is the channel to show for a release check: the line Hermes
// printed, else the plan.
func (r *Run) channelName(ch hermes.ChannelCheck) string {
	if ch.Channel != "" {
		return ch.Channel
	}
	if n := r.Facts.Channel.Plan.Effective; n != "" {
		return n
	}
	return "release"
}

// remoteRelease is the remote stage on a release channel (stable, or follow
// resolving to a channel other than main): Hermes' own `--check` is the only
// judge. The mirror and the GitHub comparison answer questions about main and
// are not used. pre carries a check that already ran to find the channel.
func (r *Run) remoteRelease(ctx context.Context, plan hermes.Plan, pre *releaseCheck) error {
	if r.Mirror != nil {
		st := r.Mirror.Status() // local state only, for the check card
		r.Facts.MirrorState = &st
	}
	if plan.Effective == "" {
		r.info("Using the channel Hermes picks for this install; asking Hermes whether a release is waiting.")
	} else {
		r.info("Using the %s channel (%s); asking Hermes whether a release is waiting.", plan.Effective, r.planOrigin(plan))
	}
	rc := pre
	if rc == nil {
		x := r.runReleaseCheck(ctx, plan)
		rc = &x
	}
	c := hermes.UpdateCheck{Source: "git"}
	if rc.err != nil {
		c.Detail = rc.err.Error()
		return r.failedReleaseCheck(ctx, c)
	}
	ch := rc.parsed
	r.Facts.Channel.Check = ch
	c.Detail = fmt.Sprintf("update --check (rc=%d): %s", rc.res.Code, execx.Tail(rc.res.Output, 400))
	r.info("%s", c.Detail)
	name := r.channelName(ch)

	switch {
	case ch.Verdict == hermes.VerdictUnresolved:
		r.noteCheck(c)
		reason := strings.TrimRight(ch.Reason, ". \t\r")
		if reason == "" {
			reason = "no reason given"
		}
		r.stop("Hermes could not resolve the %s release: %s. Nothing was changed. Try again later, or set update.channel: main to follow main instead.", name, reason)
		r.joinEarly(ctx)
		return fmt.Errorf("%w: Hermes could not resolve the %s release: %s", apperr.ErrPreflightStop, name, reason)
	case rc.res.Code != 0 || rc.res.TimedOut:
		return r.failedReleaseCheck(ctx, c)
	case ch.Verdict == hermes.VerdictUnknown:
		r.noteCheck(c)
		r.warn("Hermes output changed: `hermes update --check` printed nothing this tool recognises (see the log for the text); not guessing")
		r.stop("could not read Hermes' update check; nothing was changed")
		r.joinEarly(ctx)
		return fmt.Errorf("%w: unrecognised update check output", apperr.ErrPreflightStop)
	}

	yes, no := true, false
	switch ch.Verdict {
	case hermes.VerdictAvailable:
		c.Behind = &yes
		info := r.predictRelease(ctx, &c, ch)
		// Hermes before v0.22.0 words a release that is OLDER than the
		// checkout as "available" too, so git decides, not the wording.
		dir := r.releaseDirection(ctx, ch, info)
		if dir.state == backYes {
			r.info("git: release %s is an ancestor of this checkout, so updating would move it back", ch.Label())
			ch.Verdict = hermes.VerdictRollback
			r.Facts.Channel.Check = ch
		}
		r.noteCheck(c)
		r.reportRelease(c, ch, name)
		if dir.state == backNo {
			return nil
		}
		return r.rollbackGuard(ctx, ch, dir)
	case hermes.VerdictRollback:
		c.Behind = &yes
		r.noteCheck(c)
		r.reportRelease(c, ch, name)
		return r.rollbackGuard(ctx, ch, r.rollbackCount(ctx, ch))
	case hermes.VerdictUpToDate, hermes.VerdictWaitsForNext:
		c.Behind = &no
		r.noteCheck(c)
		r.reportRelease(c, ch, name)
		nothing := fmt.Sprintf("Nothing to update: already at the latest release (%s).", ch.Label())
		if ch.Verdict == hermes.VerdictWaitsForNext {
			nothing = fmt.Sprintf("Nothing to update: this checkout is newer than the latest release (%s) and waits for the next release.", ch.Label())
		}
		return r.verdict(ctx, c, nothing)
	}
	return fmt.Errorf("%w: unhandled check verdict %v", apperr.ErrPreflightStop, ch.Verdict)
}

// failedReleaseCheck ends the remote stage when `hermes update --check` could
// not be run or read. --force does not get past this: the check is also what
// tells a rollback from a forward update, and Hermes does not ask under --yes.
func (r *Run) failedReleaseCheck(ctx context.Context, c hermes.UpdateCheck) error {
	r.noteCheck(c) // Behind stays unknown
	r.stop("could not check for updates (see above); nothing was changed")
	r.joinEarly(ctx)
	return fmt.Errorf("%w: update check failed", apperr.ErrPreflightStop)
}

// planOrigin says why the run is on this channel, in a few words.
func (r *Run) planOrigin(plan hermes.Plan) string {
	switch {
	case r.Config.Run.ChannelFlag:
		return "--channel " + plan.Mode
	case plan.Mode == config.ChannelStable:
		return "update.channel in safe-update.yaml"
	case plan.Resolved.Source == hermes.SourceRecord:
		return "set in Hermes"
	case plan.Resolved.Source == hermes.SourceDefault:
		return "Hermes' default for this checkout"
	}
	return "follows Hermes"
}

// reportRelease prints the "updates" row of a release check.
func (r *Run) reportRelease(c hermes.UpdateCheck, ch hermes.ChannelCheck, name string) {
	const via = "Hermes release check"
	label := ch.Label()
	if label == "" {
		label = name + " release"
	}
	switch ch.Verdict {
	case hermes.VerdictAvailable:
		r.rows(ui.Row{Key: "updates", Value: releaseAvailableText(ch, c.Commits) + " (" + via + ")"},
			ui.Row{Key: "python deps", Value: kindText(c.PyDeps, "changed, will be reinstalled")},
			ui.Row{Key: "node packages", Value: kindText(c.NPM, "changed, will be reinstalled (slower build)")})
	case hermes.VerdictRollback:
		r.rows(ui.Row{Key: "updates", Value: fmt.Sprintf("checkout is newer than release %s; updating would move it back (%s)", label, via)})
	case hermes.VerdictWaitsForNext:
		r.rows(ui.Row{Key: "updates", Value: fmt.Sprintf("none, checkout is newer than release %s; waits for the next release (%s)", label, via)})
	default:
		r.rows(ui.Row{Key: "updates", Value: fmt.Sprintf("none, already at release %s (%s)", label, via)})
	}
}

// releaseAvailableText says which release is waiting, with the number of
// commits it brings when that is known: "release v0.22.1 available, 7
// commits". The channel is not repeated here (the Channel row names it).
func releaseAvailableText(ch hermes.ChannelCheck, commits *int) string {
	s := "a release is available"
	if ch.Version != "" {
		s = "release v" + ch.Version + " available"
	}
	if commits != nil {
		s += ", " + commitsText(*commits)
	}
	return s
}

// releaseAvailable is the check card's "Available" value on a release channel.
func (r *Run) releaseAvailable(commits *int) string {
	s := "a release"
	if v := r.Facts.Channel.Check.Version; v != "" {
		s = "release v" + v
	}
	if commits != nil {
		s += " (" + commitsText(*commits) + ")"
	}
	return s
}

// relInfo is what predictRelease learned about where the release stands
// against HEAD: the release commit when it is in the checkout, and GitHub's
// verdict when it had to be asked.
type relInfo struct {
	sha       string // full commit of the release ("" = not in the checkout)
	apiStatus string // GitHub compare HEAD...tag: ahead | identical | behind | diverged | ""
}

// predictRelease works out what updating to the release Hermes named would
// change, so the check can say how many commits it is, whether Python or
// Node packages change, and the estimate can use that. The target is always
// the release itself, never main: with the commit in the checkout (put
// there from the local mirror when one is set up) git answers; otherwise
// GitHub compares HEAD with the tag; otherwise the answer stays unknown.
func (r *Run) predictRelease(ctx context.Context, c *hermes.UpdateCheck, ch hermes.ChannelCheck) (info relInfo) {
	f := &r.Facts
	tag := hermes.ReleaseTag(ch.Version)
	var seedCount *int
	if r.Mirror != nil && tag != "" && f.MirrorState != nil && f.MirrorState.OK {
		res := r.Mirror.SeedRelease(ctx, f.Install.Checkout, tag, ch.SHA12)
		switch {
		case res.Used:
			st := r.Mirror.Status() // a refresh inside the seed moved LastOK
			f.MirrorState = &st
			seedCount = res.Commits
			proof := "not proven"
			if res.Complete {
				proof = "yes"
			}
			r.info("mirror: release %s seeded from the local mirror in %s (nothing left to download from GitHub: %s)", tag, fmtDuration(res.Took), proof)
		case res.Reason != "" && res.Reason != "no mirror set up":
			r.info("mirror: not used for release %s (%s)", tag, res.Reason)
		}
	}
	if r.Repo != nil {
		if ref, sha := r.Repo.ResolveRelease(ctx, tag, ch.SHA12); ref != "" {
			info.sha = sha
			c.NPM, c.PyDeps = r.Repo.DiffKinds(ctx, ref)
			if n, ok := r.Repo.CommitCount(ctx, f.HeadFull, sha); ok && n > 0 {
				c.Commits = &n
			} else if seedCount != nil && *seedCount > 0 {
				c.Commits = seedCount
			}
			return info
		}
	}
	if r.Mirror != nil && tag != "" && f.HeadFull != "" {
		if cmp := r.Mirror.APICompareTo(ctx, f.HeadFull, tag); cmp != nil {
			info.apiStatus = cmp.Status
			if cmp.Commits > 0 {
				n := cmp.Commits
				c.Commits = &n
			}
			if cmp.Status == "ahead" || cmp.Status == "identical" {
				c.NPM, c.PyDeps = hermes.ClassifyChanged(cmp.Files, cmp.FilesComplete)
			}
			return info
		}
	}
	r.info("release %s is not in the checkout yet and GitHub could not compare it: commit count and package changes are unknown", ch.Label())
	return info
}

// backState says whether updating to a release moves the checkout back.
type backState int

const (
	backNo       backState = iota // a forward update (or Hermes' own wording is all there is)
	backYes                       // the release is an ancestor of HEAD: it moves back
	backUnproven                  // git cannot rule it out (shallow clone, git failed)
)

type releaseDir struct {
	state   backState
	commits int  // backYes: commits only HEAD has (how far back); 0 = unknown
	noHead  bool // backUnproven: the installed commit is unknown, so nothing was compared
}

// releaseDirection decides from git, not from Hermes' wording, whether the
// release Hermes named is behind the checkout. Hermes before v0.22.0 calls
// such a release "available" and then checks it out. Hermes' own rule is
// `git merge-base --is-ancestor <release> HEAD`:
//
//   - release commit in the checkout: equal to HEAD = forward (nothing to
//     move); an ancestor = back (proof in any clone); not an ancestor =
//     forward in a full clone, but only a guess in a shallow one (a release
//     behind the shallow boundary is an ancestor git cannot see).
//   - not in the checkout: in a full clone (a blob:none partial clone has
//     every commit) an unknown commit cannot be an ancestor, so forward; a
//     shallow clone cannot prove that.
//   - GitHub's compare of HEAD with the tag, when it was asked, settles what
//     git cannot: "behind" = back; ahead, identical and diverged = forward
//     (Hermes treats diverged as an upgrade target too).
//   - git failing to answer, or an unknown installed commit, proves nothing:
//     unproven.
//
// A check that names no release at all has nothing to compare.
func (r *Run) releaseDirection(ctx context.Context, ch hermes.ChannelCheck, info relInfo) releaseDir {
	if r.Repo == nil || (ch.Version == "" && ch.SHA12 == "") {
		return releaseDir{}
	}
	// api is GitHub's answer when there is one (ok); it never overrules git
	// having found the release to be an ancestor.
	api := func() (releaseDir, bool) {
		switch info.apiStatus {
		case "":
			return releaseDir{}, false
		case "behind":
			return releaseDir{state: backYes}, true
		}
		return releaseDir{}, true
	}
	unproven := func(why string) releaseDir {
		if d, ok := api(); ok {
			return d
		}
		r.info("release %s: %s", ch.Label(), why)
		return releaseDir{state: backUnproven}
	}
	if info.sha != "" {
		if r.Facts.HeadFull == "" {
			// no installed commit to compare against: do not guess a direction
			d := unproven("the installed commit is unknown, so git cannot tell whether the release is older")
			d.noHead = d.state == backUnproven
			return d
		}
		if info.sha == r.Facts.HeadFull {
			return releaseDir{}
		}
		head := r.Facts.HeadFull // empty or a full 40-char id (stageLocal)
		yes, ok := r.Repo.IsAncestor(ctx, info.sha, head)
		switch {
		case !ok:
			return unproven("git could not tell whether it is an ancestor of this checkout")
		case yes:
			n, _ := r.Repo.CommitCount(ctx, info.sha, head)
			return releaseDir{state: backYes, commits: n}
		}
		// "not an ancestor" is proof only with the full history
		if d, ok := api(); ok {
			return d
		}
		if shallow, ok := r.Repo.IsShallow(ctx); !ok || shallow {
			return unproven("git says it is not an ancestor, but in a shallow clone (or when git does not answer) that is not proof")
		}
		return releaseDir{}
	}
	if d, ok := api(); ok {
		return d
	}
	shallow, ok := r.Repo.IsShallow(ctx)
	if !ok || shallow {
		return unproven("it is not in the checkout and git cannot rule out that it is older (shallow clone, or git did not answer)")
	}
	return releaseDir{}
}

// rollbackCount adds how far back to a rollback Hermes itself reported, when
// the release commit is in the checkout.
func (r *Run) rollbackCount(ctx context.Context, ch hermes.ChannelCheck) releaseDir {
	d := releaseDir{state: backYes}
	if r.Repo == nil {
		return d
	}
	if _, sha := r.Repo.ResolveRelease(ctx, hermes.ReleaseTag(ch.Version), ch.SHA12); sha != "" {
		head := r.Facts.HeadFull
		if n, ok := r.Repo.CommitCount(ctx, sha, head); ok {
			d.commits = n
		}
	}
	return d
}

// channelRow is the summary's "Channel" row: the channel the run acted on and
// why it was that one. The name comes from the most authoritative place that
// has one (the update's own "Update channel" line, the check's, the plan);
// "" (no row) when no stage learned it.
func (r *Run) channelRow() string {
	name := r.Facts.Channel.Name()
	if name == "" {
		return ""
	}
	if why := r.channelWhy(name); why != "" {
		return name + " (" + why + ")"
	}
	return name
}

func (r *Run) channelWhy(name string) string {
	p := r.Facts.Channel.Plan
	if p.Mode == "" {
		return ""
	}
	if p.Effective != "" && p.Effective != name {
		return "reported by Hermes" // Hermes printed another channel than the plan expected
	}
	switch p.Mode {
	case config.ChannelStable, config.ChannelMain:
		if r.Config.Run.ChannelFlag {
			return "--channel"
		}
		return "update.channel in safe-update.yaml"
	}
	switch p.Resolved.Source {
	case hermes.SourceRecord:
		return "set in Hermes"
	case hermes.SourceDefault:
		return "Hermes default"
	case hermes.SourceCheck:
		return "reported by Hermes"
	}
	switch {
	case !p.Caps.Probed:
		return "could not ask Hermes about channels"
	case !p.Caps.Channel:
		return "this Hermes has no release channels"
	}
	return "follows Hermes"
}

// rollbackGuard stops a run whose update would move the checkout BACK to an
// older release (Hermes prints "This checkout is newer than the selected
// release"). Hermes' own CLI does not ask under --yes, so this is the only
// place that can. Nothing has been closed yet. Attended: an explicit Y that
// names both versions; anything else, or a timeout, stops with exit 4 (--yes
// does not skip it). Unattended and check runs never roll back.
func (r *Run) rollbackGuard(ctx context.Context, ch hermes.ChannelCheck, dir releaseDir) error {
	cur := strings.TrimSpace(r.Facts.VersionBefore)
	if cur == "" {
		cur = "the installed version"
		if r.Facts.HeadShort != "" {
			cur = "commit " + r.Facts.HeadShort
		}
	}
	target := ch.Label()
	if target == "" {
		target = "the selected release"
	}
	unproven := dir.state == backUnproven
	var msg string
	if unproven && dir.noHead {
		msg = fmt.Sprintf("Safe Update cannot tell whether updating to %s is an update or a downgrade from %s: git did not report the installed commit.", target, cur)
	} else if unproven {
		msg = fmt.Sprintf("Updating might move Hermes BACK from %s to %s: git cannot rule out that release is older (shallow clone, release not in this checkout, or git did not answer).", cur, target)
	} else {
		how := ""
		if dir.commits > 0 {
			how = fmt.Sprintf(" (%s back)", commitsText(dir.commits))
		} else if n, ok := hermes.AheadOfRelease(cur, ch.Version); ok {
			how = fmt.Sprintf(" (%d commits back)", n)
		}
		msg = fmt.Sprintf("Updating would move Hermes BACK from %s to %s%s.", cur, target, how)
	}
	manual := "To do it on purpose, run `hermes-safe-update run` yourself (it asks first) or `hermes update`; to follow main instead, set update.channel: main."
	stopErr := fmt.Errorf("%w: updating would roll back %s to %s", apperr.ErrPreflightStop, cur, target)
	never := "An unattended run never rolls back"
	yesText := "roll back"
	if unproven {
		never = "An unattended run does not go on when it cannot rule that out"
		yesText = "go on"
	}

	switch {
	case r.Mode == ModeCheck:
		r.stop("%s A run would ask before doing that (an unattended run refuses). %s", msg, manual)
		r.joinEarly(ctx)
		return stopErr
	case r.Config.Run.Unattended:
		r.stop("%s %s, so nothing was changed. %s", msg, never, manual)
		r.joinEarly(ctx)
		return stopErr
	}
	tun := r.Config.Tunables
	text := fmt.Sprintf("%s Press Y to %s, N to stop (auto-stop in %s).", msg, yesText, ui.CountdownText(tun.ConfirmTimeoutS))
	a, err := r.ask(ctx, text, []rune{'y', 'n', 'a', 'q', '\r', 0x1b}, tun.ConfirmTimeout(), "auto-stop in")
	if err != nil {
		r.joinEarly(ctx)
		return err
	}
	if a.Key != 'y' {
		r.stop("not rolled back; nothing was changed. %s", manual)
		r.joinEarly(ctx)
		return stopErr
	}
	r.info("rollback confirmed by the user")
	return nil
}

// channelNameForRow is the channel for a card row ("release" when unknown).
func (r *Run) channelNameForRow() string {
	if n := r.Facts.Channel.Name(); n != "" {
		return n
	}
	return "release"
}
