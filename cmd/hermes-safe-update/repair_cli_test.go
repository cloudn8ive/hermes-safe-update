package main

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// `repair` is a command of its own, not a flag, and --help says what it does.
func TestHelpDescribesRepair(t *testing.T) {
	i := strings.Index(helpText, "\n  repair ")
	if i < 0 {
		t.Fatal("help does not list the repair command")
	}
	entry := helpText[i+1:]
	if j := strings.Index(entry, "\n  check "); j >= 0 {
		entry = entry[:j]
	}
	for _, want := range []string{"update steps again", "no new commits", "Y question", "R", "did not finish", "finished first", "ordinary update"} {
		if !strings.Contains(entry, want) {
			t.Errorf("the repair entry does not say %q:\n%s", want, entry)
		}
	}
	if !strings.Contains(helpText, "last update did not finish") {
		t.Error("help does not say that an unfinished update is finished")
	}
}

// repair needs the run flags that matter and refuses --check (it closes things).
func TestRepairTakesRunFlagsAndRefusesCheck(t *testing.T) {
	for _, args := range [][]string{
		{"repair", "--unattended"}, {"repair", "--yes"}, {"repair", "--plain"}, {"repair", "--force"}, {"repair", "--no-relaunch"},
		{"repair", "--delay", "30"}, {"repair", "--channel", "stable"}, {"repair", "--allow-elevated"},
	} {
		o, err := parseArgs(args)
		if err != nil || o.command != "repair" {
			t.Errorf("%v: %+v, %v", args, o, err)
		}
	}
	for _, args := range [][]string{{"repair", "--check"}, {"--check", "repair"}} {
		if _, err := parseArgs(args); err == nil || !strings.Contains(err.Error(), "--check cannot be combined with repair") {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"repair", "extra"}, {"repair", "now"}} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%v: an extra word must be a usage error", args)
		}
	}
	code, _, errs := runCLI(t, "--plain", "repair", "--check")
	if code != apperr.Usage {
		t.Errorf("exit %d, want %d (%q)", code, apperr.Usage, errs)
	}
}

// Every command that takes the flags says repair too.
func TestRepairIsHandledByTheRunFlow(t *testing.T) {
	if commands["repair"] == nil {
		t.Fatal("no handler for the repair command")
	}
	if commands["run"] == nil || commands["check"] == nil {
		t.Fatal("run/check lost their handlers")
	}
}
