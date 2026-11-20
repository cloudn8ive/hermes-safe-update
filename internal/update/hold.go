package update

// Stable hold (update.older_release: wait, the default). On the stable
// channel Hermes with a stored stable record moves a checkout BACK to the
// latest release with no question when that release is older than the
// checkout. Safe Update therefore never runs the stable update unless the
// release is proven to contain the installed commit; otherwise it stays on the
// installed build and ends the run like an up-to-date one (exit 0) until a
// stable release catches up. update.older_release: ask (or --allow-rollback
// for one run) keeps the older behaviour: the rollback guard asks first.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// holdState says how the installed commit stands against the stable release.
type holdState int

const (
	holdContains holdState = iota // the release contains the installed commit (or is it): update as usual
	holdNewer                     // the release is an ancestor of the installed commit: updating would move back
	holdDiverged                  // neither contains the other
	holdUnproven                  // git and GitHub could not say
)

// holdRelation is holdState with what is known about it.
type holdRelation struct {
	state   holdState
	commits int    // holdNewer: commits only the installed build has; 0 = unknown
	why     string // holdUnproven: why it could not be proven
}

// Hold is the "waiting for stable" outcome of a run, kept in the run facts for
// the end card and the stops.
type Hold struct {
	Text string // the headline sentence
	Rel  holdRelation
}

// holdApplies: the setting is wait. It is asked only by the remote stage of a
// release channel (main never gets there), and does not look at the channel
// name Hermes printed: a release check that names no channel, or one this
// tool does not know, still compares against a release and can still move the
// checkout back, so it must hold too.
func (r *Run) holdApplies() bool {
	return r.Config.Update.OlderRelease != config.OlderReleaseAsk
}

// installedName names the installed build for a sentence: the version Hermes
// printed in the pre-flight, else the short commit.
func (r *Run) installedName() string {
	if v := strings.TrimSpace(r.Facts.VersionBefore); v != "" {
		return v
	}
	if r.Facts.HeadShort != "" {
		return "commit " + r.Facts.HeadShort
	}
	return "the installed version"
}

// holdFromHermes is the relation when Hermes itself said the checkout is
// newer than the selected release (v0.22.0 and later), refined with how many
// commits when the release commit is in the checkout.
func (r *Run) holdFromHermes(ctx context.Context, ch hermes.ChannelCheck) holdRelation {
	rel := holdRelation{state: holdNewer}
	if d := r.rollbackCount(ctx, ch); d.commits > 0 {
		rel.commits = d.commits
	} else if n, ok := hermes.AheadOfRelease(r.Facts.VersionBefore, ch.Version); ok {
		rel.commits = n
	}
	return rel
}

// holdFromGit decides, for a release Hermes calls available, whether it
// contains the installed commit. Only a proof lets the update go on: a
// release that is an ancestor of the installed commit, diverged from it, or
// not provable either way all hold. Git's answer comes first; GitHub's
// compare settles what git cannot see because the release commit is not in
// the checkout yet.
func (r *Run) holdFromGit(ctx context.Context, ch hermes.ChannelCheck, info relInfo) holdRelation {
	unproven := func(why string) holdRelation {
		r.Log.Info("stable hold: not proven", slog.String("release", ch.Label()), slog.String("why", why))
		return holdRelation{state: holdUnproven, why: why}
	}
	if r.Repo == nil {
		return unproven("git is not available for this checkout, so the release cannot be compared with the installed commit")
	}
	if ch.Version == "" && ch.SHA12 == "" {
		return unproven("Hermes named no release commit to compare")
	}
	head := r.Facts.HeadFull
	if info.sha != "" {
		if head == "" {
			return unproven("git did not report the installed commit")
		}
		if info.sha == head {
			return holdRelation{state: holdContains}
		}
		back, backOK := r.Repo.IsAncestor(ctx, info.sha, head) // release is older than the checkout
		fwd, fwdOK := r.Repo.IsAncestor(ctx, head, info.sha)   // release contains the checkout
		switch {
		case backOK && back:
			n, _ := r.Repo.CommitCount(ctx, info.sha, head)
			return holdRelation{state: holdNewer, commits: n}
		case fwdOK && fwd:
			return holdRelation{state: holdContains}
		case backOK && fwdOK:
			// "neither" is proof only with the whole history
			if shallow, ok := r.Repo.IsShallow(ctx); !ok || shallow {
				return unproven("git says neither contains the other, but in a shallow clone (or when git does not answer) that is not proof")
			}
			return holdRelation{state: holdDiverged}
		}
		return unproven("git could not tell how the release and the installed commit relate")
	}
	switch info.apiStatus {
	case "ahead", "identical":
		return holdRelation{state: holdContains}
	case "behind":
		rel := holdRelation{state: holdNewer}
		if n, ok := hermes.AheadOfRelease(r.Facts.VersionBefore, ch.Version); ok {
			rel.commits = n
		}
		return rel
	case "diverged":
		return holdRelation{state: holdDiverged}
	}
	return unproven("the release commit is not in the checkout and could not be compared through GitHub either")
}

// holdText is the headline sentence of the wait outcome.
func (r *Run) holdText(ch hermes.ChannelCheck, rel holdRelation) string {
	cur := r.installedName()
	target := ch.Label()
	if target == "" {
		target = "the latest stable release"
	}
	const stay = "Safe Update stays on it until a stable release includes it."
	switch rel.state {
	case holdNewer:
		how := "newer than"
		if rel.commits > 0 {
			how = commitsText(rel.commits) + " newer than"
		}
		return r.holdWords(fmt.Sprintf("Waiting for stable: this build (%s) is %s %s; %s", cur, how, target, stay))
	case holdDiverged:
		return r.holdWords(fmt.Sprintf("Waiting for stable: %s does not contain this build (%s), and this build does not contain it; %s", target, cur, stay))
	}
	return r.holdWords(fmt.Sprintf("Waiting for stable: Safe Update could not confirm that %s contains this build (%s): %s. It does not risk moving Hermes back, so it stays on this build until a stable release is confirmed to include it.",
		target, cur, rel.why))
}

// holdRowText is the pre-flight "updates" row of the wait outcome.
func (r *Run) holdRowText(ch hermes.ChannelCheck, rel holdRelation) string {
	target := ch.Label()
	if target == "" {
		target = "the latest stable release"
	}
	const via = " (Hermes release check)"
	switch rel.state {
	case holdNewer:
		return r.holdWords("none, this build is newer than " + target + "; waiting for stable" + via)
	case holdDiverged:
		return r.holdWords("none, " + target + " does not contain this build; waiting for stable" + via)
	}
	return r.holdWords("none, could not confirm that " + target + " contains this build; waiting for stable" + via)
}

// holdChange is how to get the rollback back; every wait screen names it, so
// the move back is never reachable only through a flag.
const holdFinish = "not available while Safe Update waits for stable: finishing it means running `hermes update` for the stable channel, which would move Hermes back. To finish it on main now (this also updates Hermes to the newest main commits): hermes-safe-update --channel main"

const holdChange = "To move Hermes back to the stable release on purpose, set update.older_release: ask in safe-update.yaml (or run with --allow-rollback for one run); Safe Update then asks first. To follow main instead, set update.channel: main."

// holdBeforeUpdatePaths is the hold's defence for a release check that says
// "up to date" or "waits for the next release" when Safe Update itself read a
// persisted channel record (plan.Resolved.Source == SourceRecord) and the run
// could still run `hermes update` (finish an unfinished update, repair,
// --force, the R offer). It runs the same relation check as an "available"
// release: only a release proven to contain this build lets the run go on
// (nil); anything else ends in waitForStable (never nil), which refuses those
// paths. Without a record, or with nothing that could run the update, it
// changes nothing, so the plain "up to date" screens stay as they were.
func (r *Run) holdBeforeUpdatePaths(ctx context.Context, plan hermes.Plan, c hermes.UpdateCheck, ch hermes.ChannelCheck) error {
	if !r.holdApplies() || r.Mode != ModeUpdate || plan.Resolved.Source != hermes.SourceRecord {
		return nil
	}
	if r.Facts.Previous == nil && !r.Repair && !r.Config.Run.Force && !r.repairCanBeAsked() {
		return nil
	}
	// c is this function's own copy: what predictRelease fills in (commit
	// count, package kinds) never reaches the caller's check.
	rel := r.holdFromGit(ctx, ch, r.predictRelease(ctx, &c, ch))
	if rel.state == holdContains {
		return nil
	}
	r.Log.Info("stable hold: the release check says nothing is waiting, but the release is not proven to contain this build", slog.String("verdict", ch.Verdict.String()))
	return r.waitForStable(ctx, c, ch, rel)
}

// waitForStable ends the remote stage on the stable hold. The outcome is an
// up-to-date one (exit 0) with its own card; nothing runs `hermes update`:
// not a repair, not --force, not finishing an unfinished update, because all
// of them pass the stable argv (or, following Hermes' stored stable record,
// none) and Hermes would move this build back to the release.
func (r *Run) waitForStable(ctx context.Context, c hermes.UpdateCheck, ch hermes.ChannelCheck, rel holdRelation) error {
	no := false
	// the prediction was of an update that will not happen
	c = hermes.UpdateCheck{Source: c.Source, Detail: c.Detail, Behind: &no}
	r.noteCheck(c)
	text := r.holdText(ch, rel)
	r.Facts.Hold = &Hold{Text: text, Rel: rel}
	r.Log.Info("stable hold: waiting for stable", slog.String("state", rel.state.String()), slog.String("release", ch.Label()),
		slog.String("installed", r.installedName()), slog.Int("commits", rel.commits), slog.String("why", rel.why),
		slog.String("older_release", r.Config.Update.OlderRelease))
	r.rows(ui.Row{Key: "updates", Value: r.holdRowText(ch, rel)})

	refusal, short := r.holdRefusal()
	if refusal != "" {
		r.stop("%s %s", text, refusal)
		r.joinEarly(ctx)
		return fmt.Errorf("%w: %s", apperr.ErrPreflightStop, r.holdWords("waiting for stable, and "+short))
	}
	r.joinEarly(ctx)
	return errUpToDate
}

// holdRefusal is the refusal of a run that cannot go on while Safe Update
// waits ("" when nothing is refused) and its short form, with the channel name.
func (r *Run) holdRefusal() (refusal, short string) {
	// What the stable update would do to this build, for the refusals below.
	const would = "`hermes update` for the stable channel would move Hermes back to the release"
	switch {
	case r.Mode != ModeUpdate:
		// check shows the same outcome; nothing to refuse
	case r.Repair:
		short = "repair is not available"
		refusal = "Repair is not available while Safe Update waits for stable: " + would + ". Nothing was changed. To repair on main for this run (this also updates Hermes to the newest main commits): hermes-safe-update repair --channel main."
	case r.Config.Run.Force:
		short = "--force is not available"
		refusal = "--force is not available while Safe Update waits for stable: " + would + ". Nothing was changed. To run on main for this run (this also updates Hermes to the newest main commits): hermes-safe-update --force --channel main."
	case r.Facts.Previous != nil:
		short = "the last update cannot be finished"
		refusal = "The last update did not finish, but it cannot be finished while Safe Update waits for stable: finishing it runs `hermes update`, and " + would + ". Nothing was changed. To finish it on main for this run (this also updates Hermes to the newest main commits): hermes-safe-update --channel main."
	}
	return r.holdWords(refusal), short
}

func (s holdState) String() string {
	switch s {
	case holdContains:
		return "contains"
	case holdNewer:
		return "newer"
	case holdDiverged:
		return "diverged"
	}
	return "unproven"
}

// holdCard is the end card of the wait outcome.
func (r *Run) holdCard() ui.Card {
	rows := []ui.Row{}
	if row := r.channelRow(); row != "" {
		rows = append(rows, ui.Row{Key: "Channel", Value: row})
	}
	if r.Facts.Previous != nil {
		rows = append(rows, ui.Row{Key: "Last update", Value: r.previousWhat(), Verdict: ptr(ui.VerdictAttention)},
			ui.Row{Key: "Finish it", Value: r.holdWords(holdFinish), Verdict: ptr(ui.VerdictAttention)})
	}
	rows = append(rows, ui.Row{Key: "Move back", Value: r.holdWords(holdChange)}, ui.Row{Key: "Log", Value: r.LogPath})
	return ui.Card{OK: ptr(true), Headline: r.Facts.Hold.Text, Rows: r.withExtras(rows)}
}

// holdName is the channel the hold is on: "stable" unless the run is on
// another Hermes release channel (beta, ...), whose name the texts use.
func (r *Run) holdName() string {
	if n := r.Facts.Channel.Plan.Effective; n != "" && n != config.ChannelMain {
		return n
	}
	if n := r.Facts.Channel.Check.Release; n != "" {
		return n
	}
	return config.ChannelStable
}

// holdWords puts the effective channel name into the hold texts, which are
// written for "stable"; on stable the text is returned unchanged.
func (r *Run) holdWords(s string) string {
	n := r.holdName()
	if n == config.ChannelStable {
		return s
	}
	return strings.NewReplacer(
		"Waiting for stable", "Waiting for "+n,
		"waiting for stable", "waiting for "+n,
		"waits for stable", "waits for "+n,
		"stable release", n+" release",
		"stable channel", n+" channel",
		"until stable passes it", "until "+n+" passes it",
		"Stale stable", "Stale "+n,
		"until a stable", "until a "+n,
	).Replace(s)
}
