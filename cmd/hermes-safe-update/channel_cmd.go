package main

// The `channel` command: show which Hermes update channel this install uses
// and what Safe Update will do about it, and `channel set stable|main` to
// switch it. Reading is offline (the same read-only path a run uses). The
// switch goes through Hermes' own `hermes update --set-channel`; Safe Update
// never edits Hermes' config.yaml.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

func init() {
	commands["channel"] = channelShow
	commands["channel set"] = channelSet
}

// channelState is what the channel commands know about this Hermes.
type channelState struct {
	in   hermes.Install
	caps hermes.Caps
	res  hermes.Resolved // Hermes' own channel, read offline (Source unknown when not readable)
	plan hermes.Plan     // what the next run does, from safe-update.yaml and the above
}

// channelInstall locates the install. Channels exist for source checkouts
// only; a packaged app updates itself.
func channelInstall(ctx context.Context, cfg *config.Config, env *environment) (hermes.Install, error) {
	in, err := newLocator(cfg, env).Locate(ctx)
	if err != nil {
		return in, err
	}
	if in.Kind != hermes.KindCheckout {
		return in, fmt.Errorf("%w (%s): %s", apperr.ErrNotCheckout, in.Kind, in.Advice())
	}
	return in, nil
}

// readChannelState probes Hermes (`update --help`, with the checkout's source
// files as the fallback, and `--install-id`, both at once: each is a Python
// start-up) and reads its channel record. No network, nothing written. A
// caps that is already known (the state before a switch) is not probed again.
func readChannelState(ctx context.Context, cfg *config.Config, env *environment, in hermes.Install, known ...hermes.Caps) channelState {
	s := channelState{in: in, res: hermes.Resolved{Source: hermes.SourceUnknown}}
	if len(known) > 0 {
		s.caps = known[0]
		if s.caps.Channel && s.caps.InstallID {
			s.res = hermes.ResolveChannel(ctx, env.run(), in.Launcher, in.Home, in.Checkout)
		}
	} else {
		s.caps, s.res = hermes.ProbeChannel(ctx, env.run(), in.Launcher, in.Home, in.Checkout, true)
	}
	s.plan = hermes.BuildPlan(cfg.Update.Channel, s.caps, s.res)
	return s
}

// hermesText says what Hermes uses for this install and where that comes from.
func (s channelState) hermesText() string {
	switch {
	case !s.caps.Probed:
		return "unknown (could not ask Hermes: " + s.caps.Err + ")"
	case !s.caps.Channel:
		return "this Hermes has no release channels; it always follows main"
	case s.res.Known() && s.res.Source == hermes.SourceRecord:
		return s.res.Channel + " (set in Hermes: Settings > About, or `hermes update --set-channel`)"
	case s.res.Known() && s.res.Channel == config.ChannelMain:
		return "main (Hermes' default: this checkout does not come from the official repository)"
	case s.res.Known():
		return s.res.Channel + " (Hermes' default: this checkout comes from the official repository)"
	}
	return "not readable here (Hermes names it when a run checks for updates)"
}

// safeUpdateText says what Safe Update's own setting is and what the next
// run does with it.
func (s channelState) safeUpdateText(mode string) string {
	if mode == "" {
		mode = config.ChannelFollow
	}
	switch {
	case s.plan.Stop != "":
		return "update.channel is " + mode + " in safe-update.yaml, but this Hermes has no release channels (or they could not be checked): " +
			"a run stops before it changes anything. Set update.channel to main or follow."
	case mode == config.ChannelFollow:
		switch {
		case !s.caps.Channel:
			return "follow. This Hermes has no release channels, so a run updates from main."
		case s.plan.Pending:
			return "follow. Hermes' channel could not be read here, so a run asks Hermes first."
		}
		return "follow. A run follows Hermes: " + s.plan.Effective + "."
	}
	differs := ""
	if s.res.Known() && s.res.Channel != mode {
		differs = " (Hermes is set to " + s.res.Channel + ")"
	}
	return fmt.Sprintf("safe-update.yaml forces %s%s. A run uses %s whatever Hermes says. To follow Hermes again, set update.channel: follow.", mode, differs, mode)
}

const channelExplain = `  stable  only published, tested releases: fewer updates. By default a checkout
          that is ahead of the latest release waits for the next release; if you
          pick stable yourself, the next update moves back to the release (Safe
          Update asks first).
  main    every change the day it lands: the newest fixes, more risk.
`

func printChannelState(env *environment, cfg *config.Config, s channelState) {
	fmt.Fprintf(env.stdout, "Hermes channel:      %s\n", s.hermesText())
	fmt.Fprintf(env.stdout, "Safe Update:         %s\n", s.safeUpdateText(cfg.Update.Channel))
}

func channelShow(ctx context.Context, _ options, cfg *config.Config, env *environment, log *slog.Logger) error {
	in, err := channelInstall(ctx, cfg, env)
	if err != nil {
		return err
	}
	s := readChannelState(ctx, cfg, env, in)
	log.Info("channel shown", slog.String("hermes_channel", s.res.Channel), slog.String("source", string(s.res.Source)),
		slog.String("mode", cfg.Update.Channel), slog.String("effective", s.plan.Effective))
	printChannelState(env, cfg, s)
	if !s.caps.Channel {
		return nil
	}
	fmt.Fprint(env.stdout, "\n"+channelExplain)
	if s.caps.SetChannel {
		fmt.Fprint(env.stdout, "\nSwitch with: hermes-safe-update channel set stable|main\n"+
			"(the desktop app's Settings > About switch does the same). A switch changes\n"+
			"nothing by itself; the next update uses the new channel.\n")
	} else {
		fmt.Fprint(env.stdout, "\nThis Hermes cannot switch channels from the command line; use the desktop\napp's Settings > About, or update Hermes.\n")
	}
	return nil
}

// refuse is a stop with exit 4 and a message that stands on its own.
func refuse(format string, a ...any) error {
	return &exitError{code: apperr.PreflightStop, err: fmt.Errorf(format, a...)}
}

func channelSet(ctx context.Context, o options, cfg *config.Config, env *environment, log *slog.Logger) error {
	target := o.setTo
	in, err := channelInstall(ctx, cfg, env)
	if err != nil {
		return err
	}
	s := readChannelState(ctx, cfg, env, in)
	switch {
	case !s.caps.Probed:
		return refuse("could not read `hermes update --help` (%s), so it is not known whether this Hermes can switch channels. Nothing was changed.", s.caps.Err)
	case !s.caps.Channel:
		return refuse("this Hermes has no release channels; it always follows main, so there is nothing to switch. Nothing was changed.")
	case !s.caps.SetChannel:
		return refuse("this Hermes cannot switch channels from the command line (`hermes update --set-channel` is missing). Use the desktop app's Settings > About, or update Hermes. Nothing was changed.")
	}
	if s.res.Known() && s.res.Source == hermes.SourceRecord && s.res.Channel == target {
		fmt.Fprintf(env.stdout, "Hermes is already set to %s. Nothing to change.\n", target)
		printChannelState(env, cfg, s)
		return nil
	}

	// Switching to stable while the checkout is ahead of the latest release
	// makes the next update move Hermes back: say so before it is written.
	if target == config.ChannelStable {
		if warn := stableWarning(ctx, env, s); warn != "" {
			if err := confirmSwitch(ctx, cfg, env, warn); err != nil {
				return err
			}
		}
	}

	log.Info("channel set", slog.String("to", target), slog.String("from", s.res.Channel), slog.String("source", string(s.res.Source)))
	r, err := hermes.SetChannel(ctx, env.run(), in.Launcher, target)
	if err != nil {
		return fmt.Errorf("could not run `hermes update --set-channel %s`: %w", target, err)
	}
	if r.Code != 0 {
		return refuse("Hermes did not switch the channel (exit %d): %s Nothing was changed.", r.Code, oneLine(r.Output))
	}
	switch {
	case r.Done != "":
		fmt.Fprintln(env.stdout, r.Done)
	default:
		fmt.Fprintf(env.stdout, "Hermes accepted the change, but its confirmation line was not recognised: %s\n", oneLine(r.Output))
	}

	// Re-read what Hermes now says, offline.
	after := readChannelState(ctx, cfg, env, in, s.caps)
	if after.res.Known() && after.res.Channel != target {
		return fmt.Errorf("hermes said it switched to %s, but its record now reads %s; check Settings > About", target, after.res.Channel)
	}
	printChannelState(env, cfg, after)
	if cfg.Update.Channel == config.ChannelFollow {
		fmt.Fprintf(env.stdout, "Nothing was updated or moved yet; the next update (hermes-safe-update run, or hermes update) uses %s.\n", target)
	} else {
		fmt.Fprintln(env.stdout, "Nothing was updated or moved yet. The next `hermes update` uses the new channel.")
	}
	if m := cfg.Update.Channel; m != config.ChannelFollow {
		fmt.Fprintf(env.stdout, "Note: safe-update.yaml sets update.channel: %s, so Safe Update keeps using %s whatever Hermes is set to. To follow Hermes, set update.channel: follow (or remove the line).\n", m, m)
	}
	return nil
}

// stableWarning runs the read-only release check and returns the warning a
// switch to stable needs ("" when it would not move Hermes back). When the
// check cannot run it says so: the user still has to confirm.
func stableWarning(ctx context.Context, env *environment, s channelState) string {
	// The version is only read for the message, but it is a Python start-up
	// like the check itself: ask for both at once.
	version := make(chan string, 1)
	go func() { version <- hermes.CurrentVersion(ctx, env.run(), s.in.Launcher) }()
	ch, res, err := hermes.CheckStable(ctx, env.run(), s.in.Launcher)
	current := <-version
	if err != nil {
		return "The rollback check could not run (" + err.Error() + "), so it is not known whether the next update would move Hermes back to an older release."
	}
	switch ch.Verdict {
	case hermes.VerdictRollback, hermes.VerdictWaitsForNext:
		release := ch.Label()
		if release == "" {
			release = "the latest stable release"
		}
		back := ""
		if n, ok := hermes.AheadOfRelease(current, ch.Version); ok && n > 0 {
			back = fmt.Sprintf(" (%d commits older)", n)
		}
		msg := fmt.Sprintf("This checkout is newer than the latest release (%s). Once the channel is set to stable, the next update would move Hermes back to %s%s.", release, release, back)
		if ch.Verdict == hermes.VerdictWaitsForNext {
			msg += " Today Hermes waits for the next release only because stable is its default; a stable you set yourself does not wait."
		}
		return msg + " Safe Update's rollback guard will ask first."
	case hermes.VerdictAvailable, hermes.VerdictUpToDate:
		return ""
	case hermes.VerdictUnresolved:
		return "The rollback check could not run (Hermes could not resolve the stable release: " + strings.TrimRight(ch.Reason, ". ") + "), so it is not known whether the next update would move Hermes back to an older release."
	}
	return fmt.Sprintf("The rollback check gave an answer this tool does not recognise (exit %d), so it is not known whether the next update would move Hermes back to an older release.", res.Code)
}

// confirmSwitch asks for an explicit Y. --yes goes ahead (the warning is
// still printed); anything that cannot ask (--unattended, no terminal)
// refuses; N or a timeout leaves everything as it was.
func confirmSwitch(ctx context.Context, cfg *config.Config, env *environment, warn string) error {
	switch {
	case cfg.Run.Yes:
		fmt.Fprintln(env.stdout, "Warning: "+warn)
		return nil
	case cfg.Run.Unattended || !env.platform.Console.IsTerminal():
		return refuse("%s Nothing was changed. Run this in a terminal to confirm, or pass --yes.", warn)
	}
	a, err := ui.NewPlain(env.stderr, env.platform.Console).Prompt(ctx, ui.PromptSpec{
		Text:         fmt.Sprintf("%s Press Y to switch to stable anyway, N to leave the channel as it is (auto-cancel in %ds).", warn, cfg.Tunables.ConfirmTimeoutS),
		Keys:         []rune{'y', 'n'},
		Timeout:      cfg.Tunables.ConfirmTimeout(),
		AbortOnCtrlC: true,
	})
	if err != nil {
		return err
	}
	if a.Key != 'y' {
		return &exitError{code: apperr.Cancelled, err: errors.New("cancelled; the channel was not changed")}
	}
	return nil
}

// oneLine folds Hermes' output into one line for a message.
func oneLine(out string) string {
	s := strings.Join(strings.Fields(strings.ReplaceAll(out, "\r", "")), " ")
	if s == "" {
		return "(no output)"
	}
	return s
}
