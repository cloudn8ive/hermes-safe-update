package config

import (
	"fmt"
	"strings"
)

// Problem is one invalid value. Line is 0 when the value did not come from
// the YAML file (a default or a flag).
type Problem struct {
	Path string
	Line int
	Msg  string
}

// ValidationError lists every problem found, so users fix them in one pass.
type ValidationError struct {
	File     string
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid configuration:")
	for _, p := range e.Problems {
		b.WriteString("\n  ")
		switch {
		case e.File != "" && p.Line > 0:
			fmt.Fprintf(&b, "%s:%d: ", e.File, p.Line)
		case p.Line == 0:
			b.WriteString("(flag or default) ")
		}
		fmt.Fprintf(&b, "%s: %s", p.Path, p.Msg)
	}
	return b.String()
}

// Unwrap lets errors.Is(err, ErrInvalid) work.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// Validate checks ranges, enums and patterns of a fully merged File.
func (f File) Validate() error {
	var ps []Problem
	add := func(path, format string, a ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, a...)})
	}
	pos := func(path string, v int) {
		if v <= 0 {
			add(path, "must be greater than 0 (got %d)", v)
		}
	}
	nonNeg := func(path string, v int) {
		if v < 0 {
			add(path, "must not be negative (got %d)", v)
		}
	}

	t := f.Tunables
	if t.MinFreeGB < 0 {
		add("tunables.min_free_gb", "must not be negative (got %g)", t.MinFreeGB)
	}
	pos("tunables.idle_window_s", t.IdleWindowS)
	pos("tunables.idle_poll_s", t.IdlePollS)
	pos("tunables.idle_wait_max_s", t.IdleWaitMaxS)
	pos("tunables.confirm_timeout_s", t.ConfirmTimeoutS)
	pos("tunables.graceful_close_s", t.GracefulCloseS)
	pos("tunables.update_timeout_s", t.UpdateTimeoutS)
	pos("tunables.silent_warn_s", t.SilentWarnS)
	pos("tunables.verify_wait_s", t.VerifyWaitS)
	pos("tunables.marker_refresh_s", t.MarkerRefreshS)
	pos("tunables.estimate_window", t.EstimateWindow)
	pos("tunables.history_keep", t.HistoryKeep)
	if t.EstimateOverrunK <= 0 {
		add("tunables.estimate_overrun_k", "must be greater than 0")
	}
	if t.EstimateClassDamp < 0 || t.EstimateClassDamp > 1 {
		add("tunables.estimate_class_damp", "must be between 0 and 1")
	}
	if t.EstimateClassMin <= 0 || t.EstimateClassMax < t.EstimateClassMin {
		add("tunables.estimate_class_min", "need 0 < estimate_class_min <= estimate_class_max")
	}
	for k, v := range t.StepSeconds {
		nonNeg("tunables.step_seconds."+k, v)
	}

	th := f.Theme
	nonNeg("theme.margin", th.Margin)
	pos("theme.label_width", th.LabelWidth)
	pos("theme.status_width", th.StatusWidth)
	if th.MaxWidth < 40 {
		add("theme.max_width", "must be at least 40 (got %d)", th.MaxWidth)
	}
	pos("theme.bar_band", th.BarBand)
	for i := 0; i < 3; i++ {
		if th.BarFrom[i] < 0 || th.BarFrom[i] > 255 {
			add(fmt.Sprintf("theme.bar_from[%d]", i), "must be 0-255")
		}
		if th.BarTo[i] < 0 || th.BarTo[i] > 255 {
			add(fmt.Sprintf("theme.bar_to[%d]", i), "must be 0-255")
		}
	}
	for g, h := range th.Hues {
		if h.Bright < 0 || h.Bright > 255 || h.Dark < 0 || h.Dark > 255 {
			add("theme.hues."+g, "bright and dark must be 0-255")
		}
	}

	m := f.Mirror
	pos("mirror.offer_timeout_s", m.OfferTimeoutS)
	if m.KeepFreeGB < 0 {
		add("mirror.keep_free_gb", "must not be negative")
	}
	if m.Growth < 0 {
		add("mirror.growth", "must not be negative")
	}
	if strings.TrimSpace(m.CronSchedule) == "" {
		add("mirror.cron_schedule", "must not be empty")
	}

	s := f.Settings
	pos("settings.backup_retention", s.BackupRetention)
	if s.Origin != "" && !strings.HasPrefix(s.Origin, "http://") && !strings.HasPrefix(s.Origin, "https://") && !strings.HasPrefix(s.Origin, "file://") {
		add("settings.origin", "must start with http://, https:// or file:// (got %q)", s.Origin)
	}
	for i, p := range s.KeepNew {
		if err := ValidatePattern(p); err != nil {
			add(fmt.Sprintf("settings.keep_new[%d]", i), "%v", err)
		}
	}
	for i, p := range s.Union {
		if err := ValidatePattern(p); err != nil {
			add(fmt.Sprintf("settings.union[%d]", i), "%v", err)
		}
	}

	names := map[string]bool{}
	for i, h := range f.Hooks {
		base := fmt.Sprintf("hooks[%d]", i)
		if strings.TrimSpace(h.Name) == "" {
			add(base+".name", "must not be empty")
		} else if names[h.Name] {
			add(base+".name", "duplicate hook name %q", h.Name)
		}
		names[h.Name] = true
		switch h.When {
		case HookPreClose, HookPostUpdate, HookPostRelaunch:
		default:
			add(base+".when", "must be one of pre-close, post-update, post-relaunch (got %q)", h.When)
		}
		if len(h.Run) == 0 || strings.TrimSpace(h.Run[0]) == "" {
			add(base+".run", "must be a non-empty argv list, e.g. [\"powershell.exe\", \"-File\", \"x.ps1\"]")
		}
		nonNeg(base+".timeout_s", h.TimeoutS)
	}

	switch f.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		add("logging.level", "must be one of debug, info, warn, error (got %q)", f.Logging.Level)
	}
	nonNeg("logging.max_size_mb", f.Logging.MaxSizeMB)
	nonNeg("logging.keep", f.Logging.Keep)

	if len(ps) == 0 {
		return nil
	}
	return &ValidationError{Problems: ps}
}
