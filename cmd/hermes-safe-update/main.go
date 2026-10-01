// Command hermes-safe-update updates a Hermes source-checkout install
// without cutting off running work. main stays thin: parse flags, load
// config, wire packages, map the result to an exit code (apperr.ExitCode).
// Owner after T1: I1b.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/logx"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
	"github.com/cloudn8ive/hermes-safe-update/internal/update"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

// environment is everything main touches from the outside world, so tests
// can run the whole CLI with fakes.
type environment struct {
	platform *platform.Platform
	getenv   func(string) string
	stdout   io.Writer
	stderr   io.Writer
	runner   execx.Runner // nil = execx.New()
	self     string       // own executable; "" = os.Executable()

	// Test seams for the end-to-end replay (nil/"" = the real thing).
	clock   timings.Clock                                    // virtual clock for the flow and its services
	sleep   func(ctx context.Context, d time.Duration) error // replaces real waits in the flow
	apiBase string                                           // GitHub API base URL (main: real; tests: httptest; empty: none)
	newUI   func(cfg *config.Config, w io.Writer) ui.Renderer
	selfPID int // pid placed in the fake process table (0 = os.Getpid())
}

// now is the clock of the run (the real one unless a test injects one).
func (env *environment) now() time.Time {
	if env.clock != nil {
		return env.clock.Now()
	}
	return time.Now()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], &environment{
		platform: platform.New(),
		getenv:   os.Getenv,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		apiBase:  mirror.DefaultAPIBase,
	})
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, env *environment) int {
	o, err := parseArgs(args)
	if err != nil {
		return fail(env, err)
	}
	if o.set["appid"] {
		// hidden child mode of the console identity (§6.3): no config, no
		// log, no prompt; exit 0 ok, 1 COM failure, 2 not a window.
		return platform.ApplyAppID(appUserModelID, o.appID)
	}
	switch o.command {
	case "version":
		fmt.Fprintf(env.stdout, "hermes-safe-update %s (%s)\n", version, env.platform.OS)
		return apperr.OK
	case "help":
		fmt.Fprintf(env.stdout, helpText, version)
		return apperr.OK
	}

	cfg, err := loadConfig(o, env)
	if err != nil {
		return fail(env, err)
	}
	if cfg.MachineErr != nil {
		fmt.Fprintf(env.stderr, "warning: %v; continuing with empty machine state (the file is kept as .corrupt-<stamp> on the next save)\n", cfg.MachineErr)
	}
	log, closeLog := openLog(cfg, env)
	defer closeLog()
	log.Info("start", slog.String("version", version), slog.String("command", o.command), slog.String("os", env.platform.OS))
	if cfg.MachineErr != nil {
		log.Warn("machine state unreadable", slog.String("error", cfg.MachineErr.Error()))
	}

	if o.command == "mirror refresh" {
		cfg.Run.Unattended = true // cron: never prompt
	}
	if err := untestedGate(ctx, cfg, env); err != nil {
		log.Warn("untested platform not accepted")
		return fail(env, err)
	}

	err = dispatch(ctx, o, cfg, env, log)
	if err != nil {
		log.Error("finished", slog.String("error", err.Error()), slog.Int("exit", apperr.ExitCode(err)))
	}
	return fail(env, err)
}

// commands maps each command to its handler (settings commands: settings_cmd.go).
var commands = map[string]func(ctx context.Context, o options, cfg *config.Config, env *environment, log *slog.Logger) error{}

func init() {
	commands["run"] = runFlow(update.ModeUpdate)
	commands["check"] = runFlow(update.ModeCheck)
	commands["mirror setup"] = mirrorSetup
	commands["mirror refresh"] = mirrorRefresh
	commands["mirror status"] = mirrorStatus
}

func dispatch(ctx context.Context, o options, cfg *config.Config, env *environment, log *slog.Logger) error {
	h, ok := commands[o.command]
	if !ok {
		return fmt.Errorf("%w: unhandled command %q", apperr.ErrUsage, o.command)
	}
	return h(ctx, o, cfg, env, log)
}

func fail(env *environment, err error) int {
	if err == nil {
		return apperr.OK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if !ee.silent {
			fmt.Fprintf(env.stderr, "hermes-safe-update: %v\n", err)
		}
		return ee.code
	}
	code := apperr.ExitCode(err)
	if code == apperr.Cancelled && errors.Is(err, context.Canceled) {
		fmt.Fprintln(env.stderr, "hermes-safe-update: interrupted")
	} else {
		fmt.Fprintf(env.stderr, "hermes-safe-update: %v\n", err)
	}
	return code
}

func pathEnv(env *environment) platform.Env {
	home, _ := os.UserHomeDir()
	return platform.Env{Getenv: env.getenv, UserHome: home}
}

func loadConfig(o options, env *environment) (*config.Config, error) {
	opt := config.LoadOptions{Getenv: env.getenv}
	home, _ := env.platform.Paths.HermesHome(pathEnv(env))
	if home != "" {
		opt.YAMLPath = filepath.Join(home, "safe-update.yaml")
		opt.JSONPath = filepath.Join(home, "safe-update.json")
	}
	if o.configPath != "" {
		opt.YAMLPath = o.configPath
		opt.YAMLRequired = true // only the default location may be absent
	}
	f := &opt.Flags
	if o.set["delay"] {
		f.ConfirmTimeoutS = &o.delay
	}
	if o.set["log-level"] {
		f.LogLevel = &o.logLevel
	}
	if o.set["origin"] {
		f.Origin = &o.origin
	}
	b := func(name string, v *bool) *bool {
		if o.set[name] {
			return v
		}
		return nil
	}
	f.Plain = b("plain", &o.plain)
	f.Yes = b("yes", &o.yes)
	f.Unattended = b("unattended", &o.unattended)
	f.AcceptUntested = b("accept-untested", &o.acceptUntested)
	f.NoRelaunch = b("no-relaunch", &o.noRelaunch)
	f.Force = b("force", &o.force)
	f.NoElevate = b("no-elevate", &o.noElevate)
	cfg, err := config.Load(opt)
	if err != nil {
		return nil, err
	}
	// safe-update.json belongs to the home the YAML points at, not to the
	// platform default: reload once when paths.hermes_home moves it.
	if h := config.ExpandPath(cfg.Paths.HermesHome, userHome(), env.getenv); h != "" {
		want := filepath.Join(h, "safe-update.json")
		if !strings.EqualFold(filepath.Clean(want), filepath.Clean(opt.JSONPath)) {
			opt.JSONPath = want
			return config.Load(opt)
		}
	}
	return cfg, nil
}

// openLog writes to <home>/logs/safe-update.log when the home is known.
func openLog(cfg *config.Config, env *environment) (*slog.Logger, func()) {
	level, _ := logx.ParseLevel(cfg.Logging.Level)
	userHome, _ := os.UserHomeDir()
	red := logx.NewRedactor(userHome, env.getenv("LOCALAPPDATA"))
	home := env.home(cfg)
	if home == "" {
		return slog.New(logx.NewHandler(io.Discard, level, red)), func() {}
	}
	dir := filepath.Join(home, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return slog.New(logx.NewHandler(io.Discard, level, red)), func() {}
	}
	f, err := logx.OpenFile(filepath.Join(dir, "safe-update.log"), int64(cfg.Logging.MaxSizeMB)<<20, cfg.Logging.Keep)
	if err != nil {
		fmt.Fprintf(env.stderr, "note: log file unavailable (%v)\n", err)
		return slog.New(logx.NewHandler(io.Discard, level, red)), func() {}
	}
	return slog.New(logx.NewHandler(f, level, red)), func() { _ = f.Close() }
}

// untestedGate asks once on platforms where nothing was run for real
// (macOS, Linux). --accept-untested skips it; --unattended without it stops.
func untestedGate(ctx context.Context, cfg *config.Config, env *environment) error {
	p := env.platform
	if p.Tested || cfg.Run.AcceptUntested {
		return nil
	}
	msg := fmt.Sprintf("hermes-safe-update has only been tested on Windows. On %s it is untested: "+
		"it may close, update or relaunch Hermes incorrectly. Proceed at your own risk.", p.OS)
	if cfg.Run.Unattended {
		return fmt.Errorf("%w: %s Pass --accept-untested to run unattended", apperr.ErrUntestedDeclined, msg)
	}
	r := ui.NewPlain(env.stderr, p.Console)
	a, err := r.Prompt(ctx, ui.PromptSpec{
		Text:         msg + " Press Y to continue, N to stop.",
		Keys:         []rune{'y', 'n'},
		Timeout:      cfg.Tunables.ConfirmTimeout(),
		AbortOnCtrlC: true,
	})
	if err != nil {
		return err
	}
	if a.Key != 'y' {
		return fmt.Errorf("%w (pass --accept-untested to skip this question)", apperr.ErrUntestedDeclined)
	}
	return nil
}
