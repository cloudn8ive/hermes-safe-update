package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	env := &environment{
		platform: platform.NewFake(),
		getenv:   func(string) string { return "" },
		stdout:   &out,
		stderr:   &errb,
	}
	code := run(context.Background(), args, env)
	return code, out.String(), errb.String()
}

func TestVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out, _ := runCLI(t, args...)
		if code != 0 || !strings.Contains(out, "hermes-safe-update "+version) {
			t.Errorf("%v: code=%d out=%q", args, code, out)
		}
	}
}

func TestHelpListsSubcommandsAndExitCodes(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		code, out, _ := runCLI(t, args...)
		if code != 0 {
			t.Errorf("%v exit %d", args, code)
		}
		for _, want := range []string{"check", "migrate-settings", "revert-settings", "backup-settings", "list-backups", "mirror", "version", "--accept-untested", "--unattended", "--no-elevate", "Exit codes"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: help lacks %q", args, want)
			}
		}
	}
}

func TestUsageErrorsExit64(t *testing.T) {
	cases := [][]string{
		{"frobnicate"},
		{"--no-such-flag"},
		{"mirror"},
		{"mirror", "explode"},
		{"--log-level", "loud", "check"},
		{"revert-settings", "extra-arg"},
	}
	for _, args := range cases {
		code, _, errs := runCLI(t, args...)
		if code != apperr.Usage {
			t.Errorf("%v: exit %d, want %d (stderr %q)", args, code, apperr.Usage, errs)
		}
	}
}

func TestParseSubcommandsAndFlags(t *testing.T) {
	cases := []struct {
		args []string
		cmd  string
		chk  func(o options) bool
	}{
		{nil, "run", func(o options) bool { return true }},
		{[]string{"--check"}, "check", func(o options) bool { return true }},
		{[]string{"check", "--plain"}, "check", func(o options) bool { return o.plain }},
		{[]string{"--unattended", "--delay", "10", "--no-relaunch", "--force", "--no-elevate", "--yes"}, "run", func(o options) bool {
			return o.unattended && o.delay == 10 && o.noRelaunch && o.force && o.noElevate && o.yes
		}},
		{[]string{"migrate-settings", "--dry-run", "--origin", "http://127.0.0.1:47892"}, "migrate-settings", func(o options) bool {
			return o.dryRun && o.origin == "http://127.0.0.1:47892"
		}},
		{[]string{"revert-settings", "--backup", "settings-20261001-031500"}, "revert-settings", func(o options) bool {
			return o.backupID == "settings-20261001-031500"
		}},
		{[]string{"mirror", "setup", "--path", "X:/m"}, "mirror setup", func(o options) bool { return o.mirrorPath == "X:/m" }},
		{[]string{"mirror", "refresh"}, "mirror refresh", func(o options) bool { return true }},
		{[]string{"--config", "c.yaml", "--log-level", "debug", "list-backups"}, "list-backups", func(o options) bool {
			return o.configPath == "c.yaml" && o.logLevel == "debug"
		}},
		{[]string{"--accept-untested", "backup-settings"}, "backup-settings", func(o options) bool { return o.acceptUntested }},
	}
	for _, c := range cases {
		o, err := parseArgs(c.args)
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if o.command != c.cmd || !c.chk(o) {
			t.Errorf("%v: got %+v", c.args, o)
		}
	}
}

func TestInvalidConfigExits64AndNamesLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "safe-update.yaml")
	if err := os.WriteFile(p, []byte("tunables:\n  bogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errs := runCLI(t, "--config", p, "check")
	if code != apperr.Usage || !strings.Contains(errs, "line 2") || !strings.Contains(errs, "bogus") {
		t.Errorf("code=%d stderr=%q", code, errs)
	}
}

func TestMissingExplicitConfigExits64(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope", "x.yaml")
	code, _, errs := runCLI(t, "--config", p, "check")
	if code != apperr.Usage || !strings.Contains(errs, "x.yaml") {
		t.Errorf("code=%d stderr=%q", code, errs)
	}
}

func TestCorruptMachineStateWarnsAndContinues(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "safe-update.json"), []byte("{trunc"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	p := platform.NewFake()
	p.Paths.(*platform.FakePaths).Home = home
	env := &environment{platform: p, getenv: func(string) string { return "" }, stdout: &out, stderr: &errb}
	code := run(context.Background(), []string{"check"}, env)
	// gets past config loading into the flow (no checkout here: exit 5), with a warning
	if code != apperr.NotCheckout || !strings.Contains(errb.String(), "safe-update.json") {
		t.Errorf("code=%d stderr=%q", code, errb.String())
	}
}

func TestUntestedPlatformGate(t *testing.T) {
	var out, errb bytes.Buffer
	p := platform.NewFake()
	p.Tested = false
	p.OS = "linux"
	env := &environment{platform: p, getenv: func(string) string { return "" }, stdout: &out, stderr: &errb}

	code := run(context.Background(), []string{"--unattended", "check"}, env)
	if code != apperr.Cancelled || !strings.Contains(errb.String(), "untested") {
		t.Errorf("unattended without --accept-untested: code=%d stderr=%q", code, errb.String())
	}

	// interactive: 'n' declines
	errb.Reset()
	p.Console = &platform.FakeConsole{Keys: []platform.Key{{Rune: 'n'}}}
	code = run(context.Background(), []string{"check"}, env)
	if code != apperr.Cancelled {
		t.Errorf("declined: code=%d", code)
	}

	// --accept-untested passes the gate (then hits the skeleton stub)
	errb.Reset()
	code = run(context.Background(), []string{"--accept-untested", "check"}, env)
	if code == apperr.Cancelled || strings.Contains(errb.String(), "untested platform not accepted") {
		t.Errorf("--accept-untested should pass the gate: code=%d stderr=%q", code, errb.String())
	}

	// version and help never ask
	errb.Reset()
	if code := run(context.Background(), []string{"version"}, env); code != 0 {
		t.Errorf("version on untested OS: %d", code)
	}
}
