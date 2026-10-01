package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/cua"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/gc"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
	"github.com/cloudn8ive/hermes-safe-update/internal/update"
)

// appUserModelID groups Safe Update and Update Check windows on the taskbar
// (python-behaviour §6.3).
const appUserModelID = "HermesAgent.SafeUpdate"

func (env *environment) run() execx.Runner {
	if env.runner == nil {
		env.runner = execx.New()
	}
	return env.runner
}

func (env *environment) exe() string {
	if env.self != "" {
		return env.self
	}
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func (env *environment) home(cfg *config.Config) string {
	if h := config.ExpandPath(cfg.Paths.HermesHome, userHome(), env.getenv); h != "" {
		return h
	}
	h, _ := env.platform.Paths.HermesHome(pathEnv(env))
	return h
}

func userHome() string { h, _ := os.UserHomeDir(); return h }

// locator honours config overrides and the test environment.
func newLocator(cfg *config.Config, env *environment) *hermes.RealLocator {
	return &hermes.RealLocator{Paths: env.platform.Paths, Run: env.run(), Cfg: cfg.Paths, Env: pathEnv(env)}
}

// wireInstall builds the install-dependent services once the install is
// located (update.Deps.Wire).
func wireInstall(cfg *config.Config, env *environment, log *slog.Logger) func(hermes.Install, *update.Deps) {
	return func(in hermes.Install, d *update.Deps) {
		p := env.platform
		r := env.run()
		markers := p.Paths.DesktopProcessMarkers()
		d.Classify = func(proc platform.Process) string { return hermes.Classify(proc, in.Home, in.Checkout, markers) }
		d.Sessions = &hermes.SQLiteSessions{Home: in.Home, Now: env.now}
		gw := &hermes.CLIGateway{Home: in.Home, Launcher: in.Launcher, Python: in.Python, Checkout: in.Checkout,
			Run: r, Procs: p.Procs, GOOS: p.OS}
		if env.sleep != nil {
			gw.Sleep = func(dur time.Duration) { _ = env.sleep(context.Background(), dur) }
		}
		d.Gateway = gw
		d.Repo = &hermes.GitRepo{Git: in.Git, Checkout: in.Checkout, Run: r}
		d.Verifier = &hermes.CLIVerifier{Launcher: in.Launcher, Run: r}
		d.Marker = &hermes.Marker{Home: in.Home, Pid: os.Getpid(), Procs: p.Procs, Now: env.now}
		d.GC = gc.New(gc.Deps{Runner: r, Python: in.Python, Home: in.Home, Checkout: in.Checkout, Applicable: p.OS == "windows"})
		d.CUA = cua.New(cua.Deps{Runner: r, Launcher: in.Launcher, Autostart: p.Autostart, Home: in.Home,
			Log: func(s string) { log.Info(s) }})
		d.Mirror = newMirror(cfg, env, in, log)
		d.Hooks = &update.ExecHooks{Runner: r, Hooks: cfg.Hooks, Dir: in.Home, Procs: p.Procs}
		d.SpawnMirrorSetup = func(ctx context.Context) error { return spawnMirrorSetup(env.exe()) }
		d.LogPath = filepath.Join(in.Home, "logs", "safe-update.log")
		d.TimingsPath = filepath.Join(in.Home, "logs", "safe-update-timings.json")
		d.Settings = newSettings(cfg, env, in)
		_ = p.Console.SetIdentity(appUserModelID, []string{in.Desktop, filepath.Join(in.Checkout, "apps", "desktop", "assets", "icon.ico")})
	}
}

func newMirror(cfg *config.Config, env *environment, in hermes.Install, log *slog.Logger) *mirror.Svc {
	d := mirror.Deps{
		Runner: env.run(), Git: in.Git, Launcher: in.Launcher, Home: in.Home, Checkout: in.Checkout,
		MachinePath: filepath.Join(in.Home, "safe-update.json"), Cfg: cfg.Mirror, Disk: env.platform.Disk,
		Exe: env.exe(), Alive: env.platform.Procs.Alive,
		Log:     func(s string) { log.Info(s) },
		Out:     func(s string) { fmt.Fprintln(env.stdout, s) },
		APIBase: env.apiBase,
		Now:     env.now,
	}
	if env.sleep != nil {
		d.Sleep = func(ctx context.Context, dur time.Duration) { _ = env.sleep(ctx, dur) }
	}
	return mirror.New(d)
}

// runFlow is the handler of `run` and `check`.
func runFlow(mode update.Mode) func(context.Context, options, *config.Config, *environment, *slog.Logger) error {
	return func(ctx context.Context, o options, cfg *config.Config, env *environment, log *slog.Logger) error {
		home := env.home(cfg)
		if home == "" {
			return fmt.Errorf("%w: the Hermes home could not be determined", apperr.ErrLauncherMissing)
		}
		var r ui.Renderer
		if env.newUI != nil {
			r = env.newUI(cfg, env.stderr)
		} else {
			r = ui.New(cfg, env.platform, env.stderr, env.getenv)
		}
		defer r.Close()
		d := update.Deps{
			Config: cfg, Platform: env.platform, Runner: env.run(), Log: log, UI: r, Clock: env.clock, Sleep: env.sleep,
			Locator: newLocator(cfg, env), Wire: wireInstall(cfg, env, log), Mode: mode, SelfPID: env.selfPID, Getenv: env.getenv,
			TimingsPath: filepath.Join(home, "logs", "safe-update-timings.json"),
			LogPath:     filepath.Join(home, "logs", "safe-update.log"),
		}
		return update.Execute(ctx, update.NewRun(d), update.Flow(mode))
	}
}

// ---- mirror commands ----

// exitError carries an exact exit code (mirror setup's own table) and an
// optional silent flag (cron refresh prints nothing).
type exitError struct {
	code   int
	err    error
	silent bool
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func mirrorFor(cfg *config.Config, env *environment, log *slog.Logger) (*mirror.Svc, error) {
	in, err := newLocator(cfg, env).Locate(context.Background())
	if err != nil && !errors.Is(err, apperr.ErrLauncherMissing) {
		return nil, err
	}
	if in.Home == "" {
		return nil, fmt.Errorf("%w: the Hermes home could not be determined", apperr.ErrLauncherMissing)
	}
	return newMirror(cfg, env, in, log), nil
}

func mirrorStatus(_ context.Context, _ options, cfg *config.Config, env *environment, log *slog.Logger) error {
	m, err := mirrorFor(cfg, env, log)
	if err != nil {
		return err
	}
	st := m.Status()
	switch {
	case st.OK:
		fmt.Fprintf(env.stdout, "mirror: %s\n", st.ConfiguredPath)
		fmt.Fprintf(env.stdout, "last refresh: %s\n", mirror.AgeText(st.LastOK, time.Now()))
		if st.LastError != "" {
			fmt.Fprintf(env.stdout, "last error: %s\n", st.LastError)
		}
		job := "none"
		if st.JobExists {
			job = "paused"
			if st.JobActive {
				job = "active (every " + cfg.Mirror.CronSchedule + ")"
			}
		}
		fmt.Fprintf(env.stdout, "refresh job: %s\n", job)
	case st.ConfiguredPath != "":
		fmt.Fprintf(env.stdout, "mirror: not found at %s (updates check GitHub directly)\n", st.ConfiguredPath)
	default:
		fmt.Fprintln(env.stdout, "mirror: none (optional; run `hermes-safe-update mirror setup`)")
	}
	return nil
}

func mirrorRefresh(ctx context.Context, _ options, cfg *config.Config, env *environment, log *slog.Logger) error {
	m, err := mirrorFor(cfg, env, log)
	if err != nil {
		return &exitError{code: 1, err: err, silent: true}
	}
	if code := m.CronRefresh(ctx); code != 0 {
		return &exitError{code: code, err: errors.New("mirror refresh failed (see logs/update-mirror.log)"), silent: true}
	}
	return nil
}

func mirrorSetup(ctx context.Context, o options, cfg *config.Config, env *environment, log *slog.Logger) error {
	m, err := mirrorFor(cfg, env, log)
	if err != nil {
		return err
	}
	if !cfg.Run.Yes && !cfg.Run.Unattended && env.platform.Console.IsTerminal() {
		where, gb, why := o.mirrorPath, 0.0, "your choice"
		if where == "" {
			where, gb, why = m.Location(ctx)
		}
		if where == "" {
			return &exitError{code: 2, err: fmt.Errorf("no place for a mirror: %s", why)}
		}
		text := fmt.Sprintf("Set up a local update mirror at %s (%s)? One-time download, then a script-only Hermes job keeps it current. Press Y to continue, N to stop.", where, why)
		if gb > 0 {
			text = fmt.Sprintf("Set up a local update mirror at %s (about %.1f GB; %s)? One-time download, then a script-only Hermes job keeps it current. Press Y to continue, N to stop.", where, gb, why)
		}
		a, err := ui.NewPlain(env.stderr, env.platform.Console).Prompt(ctx, ui.PromptSpec{Text: text, Keys: []rune{'y', 'n'}, AbortOnCtrlC: true})
		if err != nil {
			return err
		}
		if a.Key != 'y' {
			return &exitError{code: apperr.Cancelled, err: apperr.ErrCancelled}
		}
	}
	err = m.Setup(ctx, o.mirrorPath)
	if o.pause {
		fmt.Fprintln(env.stderr, "Press Enter to close this window.")
		_, _ = fmt.Fscanln(os.Stdin)
	}
	if err != nil {
		return &exitError{code: mirror.SetupExitCode(err), err: err}
	}
	return nil
}
