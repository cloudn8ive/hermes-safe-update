package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/settings"
)

// newSettings builds the settings service for a located install.
func newSettings(cfg *config.Config, env *environment, in hermes.Install) *settings.Manager {
	return settings.New(settings.Deps{
		Install: in, Settings: cfg.Settings, Procs: env.platform.Procs,
		DesktopMarkers: env.platform.Paths.DesktopProcessMarkers(), Runner: env.run(), Now: env.now,
	})
}

// settingsFor locates the install for the settings commands. They work on
// any install kind that has a userData dir (they never run `hermes update`).
func settingsFor(ctx context.Context, cfg *config.Config, env *environment) (*settings.Manager, error) {
	in, err := newLocator(cfg, env).Locate(ctx)
	if err != nil && !errors.Is(err, apperr.ErrLauncherMissing) {
		return nil, err
	}
	if in.Home == "" {
		return nil, fmt.Errorf("%w: the Hermes home could not be determined", apperr.ErrLauncherMissing)
	}
	m := newSettings(cfg, env, in)
	// No update runs here: before and after are the current version.
	v := hermes.CurrentVersion(ctx, env.run(), in.Launcher)
	m.SetHermesVersions(v, v)
	return m, nil
}

func printReport(env *environment, rep settings.Report) {
	if rep.Line != "" {
		fmt.Fprintln(env.stdout, rep.Line)
	}
	if rep.BackupID != "" {
		fmt.Fprintf(env.stdout, "backup: %s\n", rep.BackupID)
	}
	for _, k := range rep.Conflicts {
		fmt.Fprintf(env.stdout, "conflict: %s\n", k)
	}
}

func migrateSettings(ctx context.Context, o options, cfg *config.Config, env *environment, log *slog.Logger) error {
	m, err := settingsFor(ctx, cfg, env)
	if err != nil {
		return err
	}
	if o.dryRun {
		p, err := m.Plan(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.stdout, "from %s to %s (%s)\n", p.Source, p.Target.URL, p.Target.Reason)
		counts := map[settings.Action]int{}
		for _, k := range p.Keys {
			counts[k.Action]++
			fmt.Fprintf(env.stdout, "  %-9s %s\n", k.Action, k.Key)
		}
		fmt.Fprintf(env.stdout, "would write %d key(s); nothing was changed (dry run)\n", p.Writes())
		return nil
	}
	rep, err := m.Migrate(ctx)
	printReport(env, rep)
	if err != nil {
		log.Warn("migrate-settings failed", slog.String("error", err.Error()))
	}
	return err
}

func backupSettings(ctx context.Context, _ options, cfg *config.Config, env *environment, _ *slog.Logger) error {
	m, err := settingsFor(ctx, cfg, env)
	if err != nil {
		return err
	}
	b, err := m.Backup(ctx, "manual")
	if err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "backup: %s\n%s\n", b.ID, b.Path)
	if n, err := m.Prune(cfg.Settings.BackupRetention); err == nil && n > 0 {
		fmt.Fprintf(env.stdout, "removed %d older backup(s) (keeping %d)\n", n, cfg.Settings.BackupRetention)
	}
	return nil
}

func revertSettings(ctx context.Context, o options, cfg *config.Config, env *environment, _ *slog.Logger) error {
	m, err := settingsFor(ctx, cfg, env)
	if err != nil {
		return err
	}
	rep, err := m.Revert(ctx, o.backupID)
	printReport(env, rep)
	return err
}

func listBackups(ctx context.Context, _ options, cfg *config.Config, env *environment, _ *slog.Logger) error {
	m, err := settingsFor(ctx, cfg, env)
	if err != nil {
		return err
	}
	bs, err := m.ListBackups()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bs) == 0 {
		fmt.Fprintln(env.stdout, "no settings backups yet")
		return nil
	}
	for _, b := range bs {
		fmt.Fprintf(env.stdout, "%s  %s  %s\n", b.ID, b.Created.UTC().Format("2006-01-02 15:04 UTC"), b.Manifest.Reason)
	}
	return nil
}

func init() {
	commands["migrate-settings"] = migrateSettings
	commands["backup-settings"] = backupSettings
	commands["revert-settings"] = revertSettings
	commands["list-backups"] = listBackups
}
