package update

// The extras stage (v0.7.0): the parts of a Hermes install that do not depend
// on the Hermes version. After the Hermes verdict of every run (up to date,
// waiting for stable, check, and after a verified update) Safe Update looks,
// read-only and in parallel, at catalog plugins, hub skills and the bundled
// tools, shows one row each, and (attended, after an explicit yes for each
// plugin) updates plugins one at a time through Hermes and hub skills. Hermes'
// own plugin update is all-or-nothing and never evicts a plugin, so Safe
// Update writes no file around it: it only checks the result. The eviction
// that can really happen is the one inside `hermes update`; the plugin guard
// (pluginguard.go) watches for it. The stage never touches Hermes' own
// checkout and never updates the tools.
//
// Placement: after the Hermes verdict, and for an update run after the
// relaunch and cleanup, so the closed-window of Hermes is not stretched by
// probes or by a question. An up-to-date run runs it in Execute, on the run's
// own context (never inside finally's five-minute one). A plugin update
// rebuilds the plugin's Python environment; Hermes handles that with the
// gateway running (it reloads its plugins).

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/extras"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// stageExtras is the stage of the check and update flows.
func stageExtras(ctx context.Context, r *Run) error {
	r.runExtras(ctx)
	return nil
}

// extrasSkip: no service, nothing configured, or (update flow) not verified.
func extrasSkip(r *Run) bool {
	if r.Extras == nil || !r.extrasConfigured() {
		return true
	}
	return r.Mode == ModeUpdate && !r.Facts.Verified
}

func (r *Run) extrasConfigured() bool {
	c := r.Config.Extras
	return c.Plugins != config.ExtrasOff || c.Skills != config.ExtrasOff || c.Tools != config.ExtrasOff || r.staleTag() != ""
}

// staleTag is the release tag of the stale-stable row, "" when the row does
// not apply (not a release channel, setting off, no release named).
func (r *Run) staleTag() string {
	if r.Config.Extras.StaleDays <= 0 || !r.Facts.Channel.Plan.Release || r.Facts.Channel.Check.Version == "" {
		return ""
	}
	return "v" + r.Facts.Channel.Check.Version
}

// extrasCanAsk: a person at a console, an update run, this run's Hermes in a
// state that was verified (applyBlock), and not inside a Hermes chat (a plugin
// update rebuilds an environment under the process that runs that chat): the
// environment test and, as in the main update, the parent chain.
func (r *Run) extrasCanAsk(ctx context.Context) bool {
	c := r.Config.Run
	if r.Mode != ModeUpdate || c.Unattended || c.Yes || r.applyBlock() != "" || r.chatEnv() != "" ||
		r.Platform == nil || r.Platform.Console == nil || !r.Platform.Console.IsTerminal() || !r.Platform.Console.InputIsTerminal() {
		return false
	}
	if r.Platform.Procs != nil {
		procs, err := r.Platform.Procs.List(ctx)
		if err != nil {
			return false // cannot tell whether this is a chat: do not change anything
		}
		if _, inChat := r.chatAncestor(procs); inChat {
			return false
		}
	}
	return true
}

// applyBlock says why this run must not change plugins or skills ("" = it
// may): Hermes' state was not verified in this run. After an update that is
// Verified; without one, an earlier update that did not finish (found or not
// retried) means this Hermes may be half-updated, and so does too little free
// disk space (a rebuild would fail), so the stage only reports. A clean "up to
// date" verdict is sound, whatever was answered to the repair offer that every
// attended up-to-date screen makes (that offer is not a sign of trouble).
func (r *Run) applyBlock() string {
	f := r.Facts
	switch {
	case f.UpdateStarted && !f.Verified:
		return "Hermes was not verified in this run"
	case !f.UpdateStarted && (f.Previous != nil || f.FinishSkipped != ""):
		return "an earlier Hermes update did not finish"
	case !f.UpdateStarted && f.DiskLow:
		return "there is not enough free disk space"
	}
	return ""
}

// extraRows are the rows the stage collected, for the end card.
func (r *Run) extraRows() []ui.Row { return append([]ui.Row(nil), r.Facts.ExtraRows...) }

// withExtras inserts the extras rows (and a Warnings row when the stage
// warned) before the last row of a card (its Log row), or at the end when
// there is none.
func (r *Run) withExtras(rows []ui.Row) []ui.Row {
	extra := r.extraRows()
	if r.Facts.ExtrasRan && r.Facts.Warnings > 0 {
		extra = append(extra, ui.Row{Key: "Warnings", Value: fmt.Sprintf("%d (see above)", r.Facts.Warnings)})
	}
	if len(extra) == 0 {
		return rows
	}
	if n := len(rows); n > 0 && rows[n-1].Key == "Log" {
		out := append(append([]ui.Row(nil), rows[:n-1]...), extra...)
		return append(out, rows[n-1])
	}
	return append(rows, extra...)
}

func (r *Run) extraRow(key, value string, v *ui.Verdict) {
	if key == "Plugins" {
		for _, e := range r.Facts.ExtraRows {
			if e.Key == "Plugins" {
				key = "Plugin check" // the guard's row (Hermes switched a plugin off) came first
				break
			}
		}
	}
	row := ui.Row{Key: key, Value: value, Verdict: v}
	r.Facts.ExtraRows = append(r.Facts.ExtraRows, row)
	// Screen: once, in the end card (withExtras); printing the row here too
	// showed Plugins, Skills and Tools twice. The log keeps both this line
	// and the SUMMARY line of the card.
	r.Log.Info(cleanRow(row).Key + ": " + cleanRow(row).Value)
}

// runExtras runs the stage once. It never fails the run: a panic in here is
// a WARN.
func (r *Run) runExtras(ctx context.Context) {
	if r.extrasDone || r.Extras == nil || ctx.Err() != nil || !r.extrasConfigured() {
		return
	}
	r.extrasDone = true
	defer func() {
		if p := recover(); p != nil {
			r.warn("plugins, skills and tools: internal error: %v", p)
		}
	}()
	c := r.Config.Extras
	opts := extras.Options{
		Plugins: c.Plugins != config.ExtrasOff,
		Skills:  c.Skills != config.ExtrasOff,
		Tools:   c.Tools != config.ExtrasOff,
		// pm update --check needs the network and takes about a minute: check only
		PMUpdate: c.Tools != config.ExtrasOff && r.Mode == ModeCheck,
	}
	r.Facts.ExtrasRan = true
	r.section("Plugins, skills and tools")
	var rep extras.Report
	if opts.Plugins || opts.Skills || opts.Tools {
		r.note("looking at plugins, skills and the bundled tools (read-only; up to about a minute)")
		rep = r.Extras.Probe(ctx, opts)
	}
	r.extrasPlugins(ctx, rep.Plugins)
	r.extrasSkills(ctx, rep.Skills)
	r.extrasTools(rep.Tools)
	r.extrasStale(ctx)
}

// ---- plugins ----

func names(rows []hermes.PluginRow) []string {
	var out []string
	for _, p := range rows {
		out = append(out, p.Name)
	}
	return out
}

func (r *Run) extrasPlugins(ctx context.Context, p *extras.PluginsResult) {
	if p == nil {
		// The plugin check is off: the notice of `hermes update` is all there is.
		r.noticeFallback()
		return
	}
	attn := ptr(ui.VerdictAttention)
	if p.Err != "" {
		r.extraRow("Plugins", "could not check ("+p.Err+"; see log)"+r.noticeText(nil, nil, true), attn)
		return
	}
	up, other, held := p.Updatable(), p.Other(), p.Held()
	extra := func() string {
		var s string
		for _, o := range other {
			if o.NeedsFixing != nil {
				s += "; " + o.Name + " needs fixing (see log)"
			} else {
				s += "; " + o.Name + " has an update Hermes does not apply (" + o.Class + ")"
			}
			r.Log.Info("plugin needs a look", slog.String("name", o.Name), slog.String("class", o.Class), slog.String("reason", o.Reason))
		}
		for _, h := range held {
			s += "; " + h.HeldText()
			r.Log.Info("plugin not offered", slog.String("name", h.Row.Name), slog.String("why", h.HeldText()))
		}
		return s + r.noticeText(up, held, false)
	}
	if len(up) == 0 {
		v, verdict := "up to date", (*ui.Verdict)(nil)
		if e := extra(); e != "" {
			v, verdict = strings.TrimPrefix(e, "; "), attn
		}
		r.extraRow("Plugins", v, verdict)
		return
	}
	list := strings.Join(names(up), ", ")
	word := "updates"
	if len(up) == 1 {
		word = "update"
	}
	text := fmt.Sprintf("%d %s: %s", len(up), word, list)
	c := r.Config.Extras
	if c.Plugins != config.ExtrasAsk || !r.extrasCanAsk(ctx) {
		r.extraRow("Plugins", text+extra()+" (not applied: "+r.extrasHow("hermes plugins update <name>", c.Plugins)+")", attn)
		return
	}
	// One question per plugin (default no); the memory provider is named as
	// such and goes last.
	provider := ""
	if st, err := r.Extras.PluginState(ctx); err == nil {
		provider = st.Provider
	}
	ordered := make([]hermes.PluginRow, 0, len(up))
	for _, row := range up {
		if row.Name != provider {
			ordered = append(ordered, row)
		}
	}
	for _, row := range up {
		if row.Name == provider {
			ordered = append(ordered, row)
		}
	}
	var done, skipped, bad, pinned []string
	for i, row := range ordered {
		if ctx.Err() != nil {
			for _, rest := range ordered[i:] {
				skipped = append(skipped, rest.Name)
			}
			break
		}
		label := row.Name
		if provider != "" && row.Name == provider {
			label += " (your memory provider)"
		}
		q := fmt.Sprintf("Update %s %s -> %s? [y/N] Hermes applies a plugin update all or nothing and Safe Update changes no file itself; Hermes refuses one that adds dependencies or capabilities when it has no terminal (then run `hermes plugins update %s` yourself). Press Y to update, any other key to skip.",
			label, hermes.Short(row.Current), hermes.Short(row.Latest), row.Name)
		if !r.extrasYes(ctx, q, "plugin update "+row.Name) {
			skipped = append(skipped, row.Name)
			continue
		}
		r.info("Updating plugin %s (%s -> %s) ...", row.Name, hermes.Short(row.Current), hermes.Short(row.Latest))
		stop := r.finishingNote(ctx, "the update of plugin "+row.Name, extras.ApplyTimeout)
		r.Facts.PluginUpdateRan = true
		o := r.Extras.UpdatePlugin(ctx, row.Name)
		stop()
		r.Log.Info("plugin update", slog.String("name", o.Name), slog.String("status", o.Status), slog.String("detail", o.Detail), slog.Any("problems", o.Problems))
		switch {
		case o.Status == extras.StatusPinned:
			// the record changed since the probe: shown like the probe's pinned row, not a failure
			pinned = append(pinned, extras.HeldRow{Row: row, Pin: o.Pin}.HeldText())
			r.warn("plugin %s: %s", row.Name, o.Detail)
		case o.Status == extras.StatusCurrent:
			done = append(done, row.Name+" (already current)")
		case o.Status == extras.StatusUpdated && len(o.Problems) == 0:
			done = append(done, row.Name)
			r.info("plugin %s updated", row.Name)
		case o.Status == extras.StatusUpdated:
			// applied, but the check afterwards found something to put right
			done = append(done, row.Name)
			bad = append(bad, row.Name+": "+o.Detail)
			r.warn("plugin %s: %s", row.Name, o.Detail)
		default:
			d := o.Detail
			if o.Status == extras.StatusRefused {
				d += "; run `hermes plugins update " + row.Name + "` in a terminal"
			}
			bad = append(bad, row.Name+": "+d)
			r.warn("plugin %s: %s", row.Name, d)
		}
	}
	v := fmt.Sprintf("%d of %d updated", len(done), len(up))
	if len(done) > 0 {
		v += ": " + strings.Join(done, ", ")
	}
	if len(skipped) > 0 {
		v += "; skipped: " + strings.Join(skipped, ", ") + " (update later with hermes plugins update <name>)"
	}
	verdict := ptr(ui.VerdictGood)
	for _, p := range pinned {
		v += "; " + p
	}
	switch {
	case len(bad) > 0:
		v += "; NOT updated or needs attention: " + strings.Join(bad, "; ")
		verdict = ptr(ui.VerdictBad)
	case len(skipped) > 0 || len(pinned) > 0:
		verdict = attn
	}
	r.extraRow("Plugins", v+extra()+r.absentSuffix(), verdict)
}

// absentSuffix explains skipped questions after one went unanswered.
func (r *Run) absentSuffix() string {
	if !r.extrasAbsent {
		return ""
	}
	return " (no answer to a question, so the others were not asked)"
}

// extrasHow is the "how to apply" part of a not-applied row.
func (r *Run) extrasHow(cmd, mode string) string {
	switch {
	case mode == config.ExtrasReport:
		return "extras setting is report; " + cmd
	case r.Mode == ModeCheck:
		return "check only; " + cmd
	case r.quiet():
		return "no one to ask; " + cmd
	case r.applyBlock() != "":
		return r.applyBlock() + "; " + cmd
	}
	return "run Safe Update from a terminal to be asked, or " + cmd
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// extrasYes asks one question: only Y says yes; anything else, the timeout
// and Ctrl+C say no.
func (r *Run) extrasYes(ctx context.Context, text, what string) bool {
	if r.extrasAbsent {
		r.info("%s: skipped (no answer to an earlier question)", what)
		return false
	}
	r.drainInput()
	tun := r.Config.Tunables
	full := text + " (skipping in " + ui.CountdownText(tun.ConfirmTimeoutS) + ")"
	a, err := r.askSpec(ctx, ui.PromptSpec{Text: full, Keys: []rune{'y'}, Timeout: tun.ConfirmTimeout(), CountdownLabel: "skipping in", AbortOnCtrlC: true, AnyKey: true}, text, true)
	if err == nil && a.TimedOut {
		r.extrasAbsent = true
		r.Log.Info("extras: no answer within the confirm timeout; the remaining extras questions are skipped", slog.String("question", what))
	}
	if err != nil || a.Abort || a.TimedOut || a.Key != 'y' {
		r.info("%s: skipped", what)
		return false
	}
	return true
}

// finishingNote tells the user at once, when Ctrl+C arrives during a step that
// is not cut half way (a plugin or skills update runs on its own time budget),
// that the run is finishing that step and for how long at most. stop ends the
// watcher; it prints the note itself when the signal came just before it.
func (r *Run) finishingNote(ctx context.Context, what string, limit time.Duration) (stop func()) {
	done, finished := make(chan struct{}), make(chan struct{})
	say := func() {
		r.note("Ctrl+C received: finishing %s first, up to %d min (stopping it half way could leave it half installed)", what, int(limit.Minutes()))
	}
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			say()
		case <-done:
			if ctx.Err() != nil {
				say()
			}
		}
	}()
	return func() { close(done); <-finished }
}

// ---- skills ----

func (r *Run) extrasSkills(ctx context.Context, s *extras.SkillsResult) {
	if s == nil {
		return
	}
	attn := ptr(ui.VerdictAttention)
	if s.Err != "" {
		r.extraRow("Skills", "could not check ("+s.Err+"; see log)", attn)
		return
	}
	if s.Check.Empty {
		r.extraRow("Skills", "none installed from the hub", nil)
		return
	}
	if s.Check.Updates == 0 {
		r.extraRow("Skills", "up to date", nil)
		return
	}
	list := strings.Join(s.Check.UpdateNames(), ", ")
	if list == "" {
		list = "see log"
	}
	text := fmt.Sprintf("%s: %s", pluralize(s.Check.Updates, "update", "updates"), list)
	c := r.Config.Extras
	if c.Skills != config.ExtrasAsk || !r.extrasCanAsk(ctx) {
		r.extraRow("Skills", text+" (not applied: "+r.extrasHow("hermes skills update", c.Skills)+")", attn)
		return
	}
	q := fmt.Sprintf("%s waiting for hub skills: %s. Update %s now with `hermes skills update`? Skills you edited are kept. Press Y to update, any other key to skip.",
		pluralize(s.Check.Updates, "update is", "updates are"), list, pluralWord(s.Check.Updates, "it", "them"))
	if !r.extrasYes(ctx, q, "skill updates") {
		r.extraRow("Skills", text+" (skipped; update later with hermes skills update)"+r.absentSuffix(), attn)
		return
	}
	stop := r.finishingNote(ctx, "the skills update", extras.SkillsApplyTmo)
	o := r.Extras.UpdateSkills(ctx)
	stop()
	r.Log.Info("skills update", slog.Bool("ok", o.OK), slog.String("detail", o.Detail))
	if !o.OK {
		r.warn("skills: %s", o.Detail)
		r.extraRow("Skills", text+" — NOT updated: "+o.Detail, ptr(ui.VerdictBad))
		return
	}
	r.extraRow("Skills", text+" — "+o.Detail, ptr(ui.VerdictGood))
}

// ---- tools ----

func (r *Run) extrasTools(t *extras.ToolsResult) {
	if t == nil {
		return
	}
	attn := ptr(ui.VerdictAttention)
	switch {
	case t.DoctorErr != "":
		r.extraRow("Tools", "could not check ("+t.DoctorErr+"; see log)", attn)
	case len(t.Doctor.Faults) == 0:
		r.extraRow("Tools", fmt.Sprintf("ok (%d checked by hermes pm doctor)", t.Doctor.OK), nil)
	default:
		var parts []string
		for _, f := range t.Doctor.Faults {
			why := f.Reason
			if f.DigestFault() {
				why = "files changed since install"
			}
			parts = append(parts, f.Name+": "+why)
			r.note("%s: %s — %s", f.Name, f.Reason, f.Advice())
		}
		r.extraRow("Tools", strings.Join(parts, "; ")+" (see log)", attn)
	}
	if !t.UpdatesRan {
		return
	}
	arrow := r.Config.Theme.Glyphs.Arrow
	switch {
	case t.UpdatesErr != "":
		r.extraRow("Newer tools", "could not check ("+t.UpdatesErr+"; see log)", attn)
	case len(t.Updates) == 0:
		r.extraRow("Newer tools", "nothing newer listed", nil)
	default:
		var parts []string
		for i, u := range t.Updates {
			if i == 4 {
				parts = append(parts, fmt.Sprintf("+%d more (see log)", len(t.Updates)-4))
				break
			}
			parts = append(parts, u.Name+" "+u.From+" "+arrow+" "+u.To)
		}
		r.extraRow("Newer tools", fmt.Sprintf("%d newer: %s (listed only; Hermes updates them, Safe Update does not)", len(t.Updates), strings.Join(parts, ", ")), nil)
	}
}

// ---- stale stable ----

// staleRowText is the key and text of the stale row, with the channel name.
func (r *Run) staleRowText(why []string) (string, string) {
	return r.holdWords("Stale stable"), r.holdWords(strings.Join(why, "; ") + ". One-time way out: hermes-safe-update --channel main (updates Hermes to the newest main commits; afterwards the hold waits until stable passes it). Nothing is done automatically.")
}

func (r *Run) extrasStale(ctx context.Context) {
	tag := r.staleTag()
	if tag == "" {
		return
	}
	mirrorPath := ""
	if st := r.Facts.MirrorState; st != nil && st.OK {
		mirrorPath = st.ConfiguredPath
	}
	s, ok := r.Extras.CheckStale(ctx, extras.StaleParams{Tag: tag, StaleDays: r.Config.Extras.StaleDays, Mirror: mirrorPath})
	if !ok || !s.Show() {
		return
	}
	var why []string
	if s.OldEnough {
		why = append(why, fmt.Sprintf("%s is %d days old (limit %d)", tag, s.AgeDays, r.Config.Extras.StaleDays))
	} else {
		why = append(why, r.holdWords(tag+" is the latest stable release"))
	}
	if s.AheadKnown && s.MainAhead > 0 {
		t := fmt.Sprintf("main has %s since", commitsText(s.MainAhead))
		if s.SecurityN > 0 {
			t += fmt.Sprintf(", %d of them with CVE, GHSA or \"security\" in the subject (the first subjects are in the log)", s.SecurityN)
		}
		if s.Source == "checkout" {
			t += " (as of this checkout's last fetch)"
		}
		why = append(why, t)
	}
	key, val := r.staleRowText(why)
	r.extraRow(key, val, ptr(ui.VerdictAttention))
}

// ---- the catalog notice of `hermes update` (Hermes v0.23.0) ----
//
// At the end of an update Hermes names the catalog plugins that the new
// catalog moved on ("N plugin update(s) from the new catalog in this
// profile: a, b"). After a Hermes update the plugin probe runs anyway and
// asks one question per plugin, so the notice never prints a line or asks
// anything of its own: it is logged when read (readApplyOutput) and folded
// into the Plugins row, where it only adds what the probe did not offer.
// The probe stays the one source of the questions; a name that only the
// notice has is shown with the command, never asked.

// noticeNames are the plugins the notice lists for the active profile.
func (r *Run) noticeNames() []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range r.Facts.CatalogNotices {
		if n.Label != hermes.ThisProfileLabel {
			continue
		}
		for _, name := range n.Names {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// noticeOthers are the notices about other profiles.
func (r *Run) noticeOthers() []hermes.CatalogNotice {
	var out []hermes.CatalogNotice
	for _, n := range r.Facts.CatalogNotices {
		if n.Label != hermes.ThisProfileLabel {
			out = append(out, n)
		}
	}
	return out
}

// noticeText is what the notice adds to the Plugins row, as "; ..." (empty
// when it adds nothing). up and held are the probe's offered and not offered
// plugins; all=true lists every name (no probe result to compare with).
func (r *Run) noticeText(up []hermes.PluginRow, held []extras.HeldRow, all bool) string {
	if len(r.Facts.CatalogNotices) == 0 {
		return ""
	}
	r.noticeFolded = true
	known := map[string]bool{}
	for _, u := range up {
		known[u.Name] = true
	}
	for _, h := range held {
		known[h.Row.Name] = true
	}
	var onlyNotice, agree []string
	for _, name := range r.noticeNames() {
		if known[name] {
			agree = append(agree, name)
		} else {
			onlyNotice = append(onlyNotice, name)
		}
	}
	var onlyProbe []string
	inNotice := map[string]bool{}
	for _, name := range r.noticeNames() {
		inNotice[name] = true
	}
	for _, u := range up {
		if !inNotice[u.Name] {
			onlyProbe = append(onlyProbe, u.Name)
		}
	}
	r.Log.Info("plugin catalog notice and plugin check compared",
		slog.String("both", strings.Join(agree, ",")), slog.String("only_notice", strings.Join(onlyNotice, ",")), slog.String("only_check", strings.Join(onlyProbe, ",")))
	var s string
	if len(onlyNotice) > 0 {
		if all {
			s += "; Hermes's notice after the update lists: " + strings.Join(onlyNotice, ", ") + " (apply: hermes plugins update <name>)"
		} else {
			s += "; Hermes's notice after the update also lists " + strings.Join(onlyNotice, ", ") + ", which the check did not offer (apply: hermes plugins update <name>)"
		}
	}
	for _, o := range r.noticeOthers() {
		s += fmt.Sprintf("; Hermes also lists %s in %s: %s (apply there: hermes plugins update)", pluralize(o.Count, "update", "updates"), o.Label, strings.Join(o.Names, ", "))
	}
	return s
}

// noticeFallback puts the notice into a Plugins row of its own when the probe
// did not (the plugin check is off, the extras stage did not run, or the
// update was not verified). The row names the command.
func (r *Run) noticeFallback() {
	if r.noticeFolded || len(r.Facts.CatalogNotices) == 0 {
		return
	}
	r.noticeFolded = true
	var parts []string
	if names := r.noticeNames(); len(names) > 0 {
		parts = append(parts, fmt.Sprintf("Hermes lists %s from the new catalog: %s", pluralize(len(names), "plugin update", "plugin updates"), strings.Join(names, ", ")))
	}
	for _, o := range r.noticeOthers() {
		parts = append(parts, fmt.Sprintf("in %s: %s", o.Label, strings.Join(o.Names, ", ")))
	}
	if len(parts) == 0 {
		return
	}
	r.extraRow("Plugins", strings.Join(parts, "; ")+" (not checked by Safe Update; apply: hermes plugins update <name>, or turn on: hermes plugins auto-update on)", ptr(ui.VerdictAttention))
}
