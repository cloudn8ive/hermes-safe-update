package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// The `channel` command against the synthetic install: Hermes' answers are
// scripted, nothing real is touched. `hermes update --set-channel` is the
// harness' fake, which writes the record the way Hermes would.

func (w *e2eWorld) stdout() string { return w.out.String() }

func (w *e2eWorld) wantOut(subs ...string) {
	w.t.Helper()
	for _, s := range subs {
		if !strings.Contains(w.stdout(), s) {
			w.t.Errorf("output lacks %q\n---- output ----\n%s\n---- screen ----\n%s", s, w.stdout(), w.screen())
			return
		}
	}
}

func (w *e2eWorld) noOut(subs ...string) {
	w.t.Helper()
	for _, s := range subs {
		if strings.Contains(w.stdout(), s) {
			w.t.Errorf("output unexpectedly has %q\n---- output ----\n%s", s, w.stdout())
		}
	}
}

func (w *e2eWorld) setCalled(name string) bool {
	return w.ran(w.launcher, "update", "--set-channel", name)
}

// recordNow reads the channel record the world's config.yaml carries.
func (w *e2eWorld) recordNow() string {
	b, err := os.ReadFile(filepath.Join(w.home, "config.yaml"))
	if err != nil {
		return "none"
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "channel:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return "none"
}

// ---- channel (show) ----

func TestE2EChannelShowRecord(t *testing.T) {
	w := newWorld(t) // record main
	w.cli("channel")
	w.wantCode(apperr.OK)
	w.wantOut("Hermes channel:      main (set in Hermes", "A run follows Hermes: main.",
		"stable  only published, tested releases", "main    every change the day it lands",
		"hermes-safe-update channel set stable|main", "Settings > About")
	w.noOut("forces")
	// offline and read-only: two probes, no check, no network, no write, no prompt
	for _, c := range w.Calls() {
		if j := strings.Join(c[1:], " "); strings.Contains(j, "--check") || strings.Contains(j, "--set-channel") || strings.Contains(j, "update --yes") {
			t.Errorf("channel ran %q", j)
		}
	}
	if w.apiHits != 0 {
		t.Errorf("the GitHub API was asked %d times", w.apiHits)
	}
	if got := w.recordNow(); got != "main" {
		t.Errorf("record = %s", got)
	}
}

// The help probe and the install-id read are two Python start-ups (~20 s each
// on a loaded machine): the command runs them side by side.
func TestE2EChannelShowReadsHelpAndIDAtOnce(t *testing.T) {
	w := newWorld(t)
	w.probeBarrier = true
	w.cli("channel")
	w.wantCode(apperr.OK)
	if !w.probesOverlapped() {
		t.Error("`channel` ran `update --help` and `update --install-id` one after the other")
	}
}

// A switch reads the state before and after; the capabilities are known after
// the first read, so the help is not asked twice.
func TestE2EChannelSetAsksTheHelpOnce(t *testing.T) {
	w := newWorld(t) // record main: stable is a real switch, read before and after
	w.releaseOut = readContract(t, "update-check-stable-available.txt")
	w.cli("channel", "set", "stable")
	w.wantCode(apperr.OK)
	n := 0
	for _, c := range w.Calls() {
		if len(c) >= 3 && c[len(c)-2] == "update" && c[len(c)-1] == "--help" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("`update --help` ran %d times, want once", n)
	}
}

func TestE2EChannelShowDefaults(t *testing.T) {
	t.Run("official origin: stable", func(t *testing.T) {
		w := newWorld(t)
		w.record = "none"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("Hermes channel:      stable (Hermes' default: this checkout comes from the official repository)", "A run follows Hermes: stable.")
	})
	t.Run("a fork: main", func(t *testing.T) {
		w := newWorld(t)
		w.record = "none"
		w.origin = "https://github.com/someone/hermes-agent.git"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("Hermes channel:      main (Hermes' default: this checkout does not come from the official repository)", "A run follows Hermes: main.")
	})
	t.Run("record stable", func(t *testing.T) {
		w := newWorld(t)
		w.record = "stable"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("Hermes channel:      stable (set in Hermes", "A run follows Hermes: stable.")
	})
}

func TestE2EChannelShowOldHermes(t *testing.T) {
	w := newWorld(t)
	w.help = "legacy"
	w.cli("channel")
	w.wantCode(apperr.OK)
	w.wantOut("this Hermes has no release channels; it always follows main", "so a run updates from main")
	w.noOut("channel set", "stable  only published")
}

func TestE2EChannelShowYAMLOverride(t *testing.T) {
	t.Run("yaml stable, Hermes main", func(t *testing.T) {
		w := newWorld(t)
		w.yaml += "update:\n  channel: stable\n"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("Hermes channel:      main (set in Hermes", "safe-update.yaml forces stable (Hermes is set to main). A run uses stable whatever Hermes says.", "update.channel: follow")
	})
	t.Run("yaml main, Hermes stable", func(t *testing.T) {
		w := newWorld(t)
		w.record = "stable"
		w.yaml += "update:\n  channel: main\n"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("safe-update.yaml forces main (Hermes is set to stable)")
	})
	t.Run("yaml stable on a Hermes without channels", func(t *testing.T) {
		w := newWorld(t)
		w.help = "legacy"
		w.yaml += "update:\n  channel: stable\n"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("a run stops before it changes anything", "Set update.channel to main or follow")
	})
}

func TestE2EChannelShowOnAPackagedInstall(t *testing.T) {
	w := newWorld(t)
	if err := os.RemoveAll(filepath.Join(w.checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	w.cli("channel")
	w.wantCode(apperr.NotCheckout)
}

// ---- channel set ----

func TestE2EChannelSetMain(t *testing.T) {
	w := newWorld(t)
	w.record = "stable"
	w.cli("channel", "set", "main")
	w.wantCode(apperr.OK)
	if !w.setCalled("main") {
		t.Fatal("hermes update --set-channel main never ran")
	}
	if w.ran(w.launcher, "update", "--check") {
		t.Error("a switch to main ran a release check")
	}
	w.wantOut("Update channel for 0123456789abcdef: main", "Hermes channel:      main (set in Hermes", "Nothing was updated or moved yet; the next update (hermes-safe-update run, or hermes update) uses main.")
	if got := w.recordNow(); got != "main" {
		t.Errorf("record = %s", got)
	}
}

func TestE2EChannelSetStableWithoutRollback(t *testing.T) {
	w := newWorld(t)
	w.releaseOut = readContract(t, "update-check-stable-available.txt")
	w.cli("channel", "set", "stable")
	w.wantCode(apperr.OK)
	if !w.ran(w.launcher, "update", "--check", "--channel", "stable") {
		t.Error("no read-only release check before the switch")
	}
	if !w.setCalled("stable") || w.recordNow() != "stable" {
		t.Errorf("not switched: record %s", w.recordNow())
	}
	if w.con.prompts != 0 {
		t.Error("asked a question although nothing moves back")
	}
	w.wantOut("Update channel for 0123456789abcdef: stable", "Hermes channel:      stable (set in Hermes")
	w.noOut("Warning", "move Hermes back")
}

func switchRollbackWorld(t *testing.T) *e2eWorld {
	t.Helper()
	w := newWorld(t) // record main
	w.releaseOut = readContract(t, "update-check-stable-rollback.txt")
	w.version = "v0.22.0+38.g1f6c02c"
	return w
}

func TestE2EChannelSetStableRollbackAttended(t *testing.T) {
	t.Run("Y switches", func(t *testing.T) {
		w := switchRollbackWorld(t)
		w.vt = true // the fake console is a terminal
		w.con.script(conStep{key: 'y'})
		w.cli("--plain", "channel", "set", "stable")
		w.wantCode(apperr.OK)
		w.wantScreen("This checkout is newer than the latest release (stable v0.22.0)",
			"would move Hermes back to stable v0.22.0 (38 commits older)", "rollback guard will ask first", "Press Y to switch to stable anyway", "(auto-cancel in 10 min)")
		if !w.setCalled("stable") || w.recordNow() != "stable" {
			t.Errorf("not switched: record %s", w.recordNow())
		}
	})
	t.Run("N leaves everything as it was", func(t *testing.T) {
		w := switchRollbackWorld(t)
		w.vt = true
		w.con.script(conStep{key: 'n'})
		w.cli("--plain", "channel", "set", "stable")
		w.wantCode(apperr.Cancelled)
		w.wantScreen("would move Hermes back")
		if w.setCalled("stable") || w.recordNow() != "main" {
			t.Errorf("changed although declined: record %s", w.recordNow())
		}
	})
	t.Run("no answer in time cancels", func(t *testing.T) {
		w := switchRollbackWorld(t)
		w.vt = true
		w.con.script(conStep{timeout: true})
		w.cli("--plain", "channel", "set", "stable")
		w.wantCode(apperr.Cancelled)
		if w.setCalled("stable") {
			t.Error("switched on a timeout")
		}
	})
	t.Run("another key is no", func(t *testing.T) {
		w := switchRollbackWorld(t)
		w.vt = true
		w.con.script(conStep{key: 'q'}, conStep{key: 'n'}) // q is ignored by the prompt, n answers
		w.cli("--plain", "channel", "set", "stable")
		w.wantCode(apperr.Cancelled)
		if w.setCalled("stable") {
			t.Error("switched without a Y")
		}
	})
}

func TestE2EChannelSetStableRollbackYes(t *testing.T) {
	w := switchRollbackWorld(t)
	w.cli("--yes", "channel", "set", "stable")
	w.wantCode(apperr.OK)
	if w.con.prompts != 0 {
		t.Error("--yes asked anyway")
	}
	w.wantOut("Warning: This checkout is newer than the latest release (stable v0.22.0)", "(38 commits older)")
	if !w.setCalled("stable") || w.recordNow() != "stable" {
		t.Errorf("not switched: record %s", w.recordNow())
	}
}

func TestE2EChannelSetStableRollbackCannotAsk(t *testing.T) {
	t.Run("--unattended", func(t *testing.T) {
		w := switchRollbackWorld(t)
		w.vt = true
		w.cli("--unattended", "channel", "set", "stable")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("Nothing was changed", "pass --yes")
		if w.setCalled("stable") {
			t.Error("switched although it could not ask")
		}
	})
	t.Run("no terminal", func(t *testing.T) {
		w := switchRollbackWorld(t) // vt false: not a terminal
		w.cli("channel", "set", "stable")
		w.wantCode(apperr.PreflightStop)
		if w.setCalled("stable") || w.con.prompts != 0 {
			t.Error("switched or prompted without a terminal")
		}
	})
}

// Hermes' default stable waits for the next release when the checkout is
// ahead; a stable the user sets is not forward-only. The check says "waits",
// the command still warns.
func TestE2EChannelSetStableFromTheDefaultWarns(t *testing.T) {
	w := newWorld(t)
	w.record = "none"
	w.releaseOut = readContract(t, "update-check-stable-waits.txt")
	w.version = "v0.22.0+38.g1f6c02c"
	w.vt = true
	w.con.script(conStep{key: 'y'})
	w.cli("--plain", "channel", "set", "stable")
	w.wantCode(apperr.OK)
	w.wantScreen("would move Hermes back to stable v0.22.0 (38 commits older)", "Hermes waits for the next release only because stable is its default")
	if !w.setCalled("stable") {
		t.Error("not switched")
	}
}

func TestE2EChannelSetStableCheckCannotRun(t *testing.T) {
	t.Run("offline: still asks", func(t *testing.T) {
		w := newWorld(t)
		w.releaseOut = readContract(t, "update-check-stable-unresolved.txt")
		w.vt = true
		w.con.script(conStep{key: 'y'})
		w.cli("--plain", "channel", "set", "stable")
		w.wantCode(apperr.OK)
		w.wantScreen("The rollback check could not run (Hermes could not resolve the stable release: Connection to api.github.com failed", "not known whether the next update would move Hermes back")
		if !w.setCalled("stable") {
			t.Error("not switched after Y")
		}
	})
	t.Run("offline: N keeps it", func(t *testing.T) {
		w := newWorld(t)
		w.releaseOut = readContract(t, "update-check-stable-unresolved.txt")
		w.vt = true
		w.con.script(conStep{key: 'n'})
		w.cli("--plain", "channel", "set", "stable")
		w.wantCode(apperr.Cancelled)
		if w.setCalled("stable") {
			t.Error("switched although declined")
		}
	})
	t.Run("an answer nobody knows: still asks", func(t *testing.T) {
		w := newWorld(t)
		w.releaseOut = "something Hermes never said\n"
		w.vt = true
		w.con.script(conStep{key: 'n'})
		w.cli("--plain", "channel", "set", "stable")
		w.wantCode(apperr.Cancelled)
		w.wantScreen("answer this tool does not recognise")
	})
}

func TestE2EChannelSetRefusals(t *testing.T) {
	t.Run("Hermes refuses (its exit 2)", func(t *testing.T) {
		w := newWorld(t)
		w.record = "stable"
		w.setRC, w.setOut = 2, readContract(t, "update-set-channel-refused.txt")
		w.cli("channel", "set", "main")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("Hermes did not switch the channel (exit 2): channels don't apply here; updates are owned by an external steward")
		if w.recordNow() != "stable" {
			t.Errorf("record = %s", w.recordNow())
		}
	})
	t.Run("a Hermes without --set-channel", func(t *testing.T) {
		w := newWorld(t)
		w.help = "noset"
		w.cli("channel", "set", "main")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("cannot switch channels from the command line", "Settings > About")
		if w.setCalled("main") {
			t.Error("--set-channel was passed to a Hermes that does not list it")
		}
	})
	t.Run("a Hermes without channels", func(t *testing.T) {
		w := newWorld(t)
		w.help = "legacy"
		w.cli("channel", "set", "stable")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("no release channels; it always follows main")
		if w.setCalled("stable") || w.ran(w.launcher, "update", "--check") {
			t.Error("asked a Hermes without channels to switch")
		}
	})
}

func TestE2EChannelSetAlreadyThere(t *testing.T) {
	w := newWorld(t)
	w.record = "stable"
	w.cli("channel", "set", "stable")
	w.wantCode(apperr.OK)
	w.wantOut("Hermes is already set to stable. Nothing to change.")
	if w.setCalled("stable") || w.ran(w.launcher, "update", "--check") {
		t.Error("changed or checked although already set")
	}
}

func TestE2EChannelSetSaysWhenTheYAMLWins(t *testing.T) {
	w := newWorld(t)
	w.record = "stable"
	w.yaml += "update:\n  channel: stable\n"
	w.cli("channel", "set", "main")
	w.wantCode(apperr.OK)
	w.wantOut("Note: safe-update.yaml sets update.channel: stable, so Safe Update keeps using stable whatever Hermes is set to",
		"set update.channel: follow", "Hermes channel:      main (set in Hermes")
	// the YAML wins, so the next Safe Update run does not use the new channel
	w.noOut("hermes update) uses")
}

// Only the explicit command may pass --set-channel; a run or a check never does.
func TestE2EChannelRunsNeverSetTheChannel(t *testing.T) {
	w := newWorld(t)
	w.cli("--plain", "check")
	for _, c := range w.Calls() {
		if strings.Contains(strings.Join(c, " "), "--set-channel") {
			t.Errorf("check passed --set-channel: %v", c)
		}
	}
}

// ---- command line ----

func TestParseChannelCommand(t *testing.T) {
	for _, c := range []struct {
		args    []string
		command string
		to      string
	}{
		{[]string{"channel"}, "channel", ""},
		{[]string{"channel", "set", "stable"}, "channel set", "stable"},
		{[]string{"--yes", "channel", "set", "main"}, "channel set", "main"},
		{[]string{"channel", "set", "main", "--yes"}, "channel set", "main"},
	} {
		o, err := parseArgs(c.args)
		if err != nil || o.command != c.command || o.setTo != c.to {
			t.Errorf("%v: %q %q, %v", c.args, o.command, o.setTo, err)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"channel", "set", "canary"}, `channel "canary" is not offered`},
		{[]string{"channel", "set", "Stable"}, "is not offered"},
		{[]string{"channel", "set"}, "channel set needs the channel"},
		{[]string{"channel", "stable"}, "channel takes no argument"},
		{[]string{"channel", "set", "stable", "main"}, "unexpected argument"},
		{[]string{"channel", "--channel", "main"}, "--channel applies to run and check only"},
		{[]string{"channel", "--check"}, "--check cannot be combined"},
		{[]string{"set", "stable"}, "unknown command"},
	} {
		_, err := parseArgs(c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (want %q)", c.args, err, c.want)
			continue
		}
		if apperr.ExitCode(err) != apperr.Usage {
			t.Errorf("%v: exit %d, want %d", c.args, apperr.ExitCode(err), apperr.Usage)
		}
	}
}

func TestHelpListsTheChannelCommand(t *testing.T) {
	for _, want := range []string{"channel set stable|main", `"hermes update --set-channel"`, "channel set: refused or failed"} {
		if !strings.Contains(helpText, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

// `channel` shows state and never asks the untested-platform question; `channel
// set` writes Hermes' config and is gated like the other writers.
func TestChannelGate(t *testing.T) {
	for cmd, exempt := range map[string]bool{"channel": true, "channel set": false} {
		if got := gateExempt(options{command: cmd}); got != exempt {
			t.Errorf("gateExempt(%q) = %t, want %t", cmd, got, exempt)
		}
	}
}
