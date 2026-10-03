package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// options are the parsed command line. Owner after T1: I1b (other tasks
// request flag changes through their handoff).
type options struct {
	command string // run | check | migrate-settings | backup-settings | revert-settings | list-backups | mirror setup|refresh|status | version | help

	// global
	configPath     string
	yes            bool
	unattended     bool
	acceptUntested bool
	plain          bool
	logLevel       string

	// Python-compatible run flags (D18)
	check      bool
	delay      int
	noRelaunch bool
	force      bool
	noElevate  bool

	// subcommand flags
	dryRun     bool   // migrate-settings
	origin     string // migrate-settings
	backupID   string // revert-settings
	mirrorPath string // mirror setup
	pause      bool   // hidden: mirror setup keeps its own window open
	appID      string // hidden: console-identity child mode (Windows, I1b)

	set map[string]bool // flags given explicitly
}

var subcommands = map[string]bool{
	"run": true, "check": true, "migrate-settings": true, "backup-settings": true,
	"revert-settings": true, "list-backups": true, "mirror": true, "version": true, "help": true,
}

var mirrorActions = map[string]bool{"setup": true, "refresh": true, "status": true}

func usageError(format string, a ...any) error {
	return fmt.Errorf("%w: %s", apperr.ErrUsage, fmt.Sprintf(format, a...))
}

func newFlagSet(o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("hermes-safe-update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.configPath, "config", "", "")
	fs.BoolVar(&o.yes, "yes", false, "")
	fs.BoolVar(&o.unattended, "unattended", false, "")
	fs.BoolVar(&o.acceptUntested, "accept-untested", false, "")
	fs.BoolVar(&o.plain, "plain", false, "")
	fs.StringVar(&o.logLevel, "log-level", "", "")
	fs.BoolVar(&o.check, "check", false, "")
	fs.IntVar(&o.delay, "delay", 0, "")
	fs.BoolVar(&o.noRelaunch, "no-relaunch", false, "")
	fs.BoolVar(&o.force, "force", false, "")
	fs.BoolVar(&o.noElevate, "no-elevate", false, "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.StringVar(&o.origin, "origin", "", "")
	fs.StringVar(&o.backupID, "backup", "", "")
	fs.StringVar(&o.mirrorPath, "path", "", "")
	fs.StringVar(&o.appID, "appid", "", "")
	fs.BoolVar(&o.pause, "pause", false, "")
	var showVersion bool
	fs.BoolVar(&showVersion, "version", false, "")
	return fs
}

// parseArgs accepts flags before and after the subcommand.
func parseArgs(args []string) (options, error) {
	var o options
	fs := newFlagSet(&o)
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				o.command = "help"
				return o, nil
			}
			return o, usageError("%v", err)
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		rest = rest[1:]
	}
	o.set = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { o.set[f.Name] = true })
	if o.set["version"] {
		o.command = "version"
		return o, nil
	}

	if len(pos) == 0 {
		o.command = "run"
	} else {
		o.command = pos[0]
		pos = pos[1:]
	}
	if !subcommands[o.command] {
		return o, usageError("unknown command %q (see --help)", o.command)
	}
	if o.command == "mirror" {
		if len(pos) == 0 || !mirrorActions[pos[0]] {
			return o, usageError("mirror needs one of: setup, refresh, status")
		}
		o.command += " " + pos[0]
		pos = pos[1:]
	}
	if len(pos) > 0 {
		return o, usageError("unexpected argument %q", strings.Join(pos, " "))
	}
	if o.check {
		if o.command != "run" && o.command != "check" {
			return o, usageError("--check cannot be combined with %s", o.command)
		}
		o.command = "check"
	}
	if o.set["delay"] && o.delay <= 0 {
		return o, usageError("--delay must be greater than 0")
	}
	return o, nil
}

const helpText = `hermes-safe-update %s: update a Hermes source-checkout install safely.

Usage:
  hermes-safe-update [flags] [command]

Commands:
  run (default)        check, wait for idle sessions, close Hermes, update,
                       verify, migrate desktop settings, reopen Hermes
  check                look-only pre-flight: closes and updates nothing, but may
                       fetch commits into the git data, like git fetch (same
                       as --check)
  migrate-settings     back up, then migrate desktop settings to the current
                       origin (--dry-run shows the plan, --origin URL overrides)
  backup-settings      back up the desktop settings now
  revert-settings      restore a settings backup (--backup ID, default newest)
  list-backups         list settings backups
  mirror setup|refresh|status
                       optional local git mirror that speeds up updates
                       (setup: --path DIR)
  version              print the version

Flags:
  --config FILE        safe-update.yaml to use (default: <hermes home>/safe-update.yaml)
  --yes                skip the final "Press Y" confirmation (never skips the
                       busy-session check)
  --unattended         never ask; wait for idle sessions (max 30 min), then go
  --accept-untested    run on macOS/Linux without the untested-platform prompt
  --plain              plain text output (also HERMES_SAFE_UPDATE_PLAIN=1, NO_COLOR)
  --log-level LEVEL    debug | info | warn | error
  --check              same as the check command
  --delay N            seconds the final confirmation waits (default 30)
  --no-relaunch        do not reopen the desktop app afterwards
  --force              run even when no update is available
  --no-elevate         never show a UAC prompt (log the admin command instead)

Exit codes:
  0  done (updated and verified, check finished, or already up to date)
  1  the update needs attention, or an unexpected error
  2  the hermes launcher was not found
  3  cancelled before anything changed (Hermes left running)
  4  pre-flight stop (another update running, low disk, update check failed)
  5  not a source-checkout install (packaged app, MSIX, Docker, Nix): use the
     in-app updater or your package manager
  6  refused because the Hermes desktop app is running (settings commands)
  64 bad command line or invalid configuration

Windows is the tested platform. macOS and Linux builds are untested.
`
