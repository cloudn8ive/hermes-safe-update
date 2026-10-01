// Package config loads the tool's settings: built-in defaults, the
// human-edited safe-update.yaml, the machine state safe-update.json, a few
// environment variables and the command-line flags, in that order of
// precedence (later wins). It is a leaf package: it imports nothing internal
// except fsx.
package config

import "time"

// File is the schema of safe-update.yaml. Every field has a default (see
// Defaults); a YAML file only needs the keys it changes. Maps merge key by
// key with the defaults, lists replace the default list.
type File struct {
	Paths    Paths    `yaml:"paths"`
	Tunables Tunables `yaml:"tunables"`
	Theme    Theme    `yaml:"theme"`
	Mirror   Mirror   `yaml:"mirror"`
	Settings Settings `yaml:"settings"`
	Hooks    []Hook   `yaml:"hooks"`
	Logging  Logging  `yaml:"logging"`
}

// Paths overrides auto-detected locations. Empty = detect. `~` and `$VAR` /
// `%VAR%` are expanded in these fields only (see ExpandPath).
type Paths struct {
	HermesHome string `yaml:"hermes_home"` // default: %LOCALAPPDATA%\hermes or ~/.hermes
	Checkout   string `yaml:"checkout"`    // default: <hermes_home>/hermes-agent
	UserData   string `yaml:"user_data"`   // default: %APPDATA%\Hermes, ~/Library/Application Support/Hermes, ~/.config/Hermes
}

// Tunables are the numbers of the update flow. Seconds are plain integers
// with an `_s` suffix (they mirror the Python constants one to one).
type Tunables struct {
	MinFreeGB       float64 `yaml:"min_free_gb"`       // pre-flight disk floor on the Hermes home drive (decimal GB)
	IdleWindowS     int     `yaml:"idle_window_s"`     // a session counts as busy if active within this window
	IdlePollS       int     `yaml:"idle_poll_s"`       // re-check interval while waiting for idle sessions
	IdleWaitMaxS    int     `yaml:"idle_wait_max_s"`   // give up waiting for idle sessions after this long
	ConfirmTimeoutS int     `yaml:"confirm_timeout_s"` // final Y confirmation auto-aborts after this (--delay)
	GracefulCloseS  int     `yaml:"graceful_close_s"`  // wait for the desktop app to close before forcing it
	UpdateTimeoutS  int     `yaml:"update_timeout_s"`  // hard cap for `hermes update`
	SilentWarnS     int     `yaml:"silent_warn_s"`     // note "no output for N s" once after this much silence
	VerifyWaitS     int     `yaml:"verify_wait_s"`     // wait for the gateway to report the new code
	MarkerRefreshS  int     `yaml:"marker_refresh_s"`  // rewrite the update marker this often

	EstimateWindow    int     `yaml:"estimate_window"`     // mean of the last N matching runs
	EstimateOverrunK  float64 `yaml:"estimate_overrun_k"`  // remaining = k * elapsed once a step overruns
	EstimateClassDamp float64 `yaml:"estimate_class_damp"` // damping of the network/machine speed factor
	EstimateClassMin  float64 `yaml:"estimate_class_min"`  // lower clamp of that factor
	EstimateClassMax  float64 `yaml:"estimate_class_max"`  // upper clamp of that factor
	HistoryKeep       int     `yaml:"history_keep"`        // timing records kept per pool

	// StepSeconds are the seed estimates per stage key, used until history exists.
	StepSeconds map[string]int `yaml:"step_seconds"`
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }

// IdleWindow and the other helpers return the tunables as durations.
func (t Tunables) IdleWindow() time.Duration     { return secs(t.IdleWindowS) }
func (t Tunables) IdlePoll() time.Duration       { return secs(t.IdlePollS) }
func (t Tunables) IdleWaitMax() time.Duration    { return secs(t.IdleWaitMaxS) }
func (t Tunables) ConfirmTimeout() time.Duration { return secs(t.ConfirmTimeoutS) }
func (t Tunables) GracefulClose() time.Duration  { return secs(t.GracefulCloseS) }
func (t Tunables) UpdateTimeout() time.Duration  { return secs(t.UpdateTimeoutS) }
func (t Tunables) SilentWarn() time.Duration     { return secs(t.SilentWarnS) }
func (t Tunables) VerifyWait() time.Duration     { return secs(t.VerifyWaitS) }
func (t Tunables) MarkerRefresh() time.Duration  { return secs(t.MarkerRefreshS) }

// Hue is one stage's colour pair (xterm-256 indices) and icon.
type Hue struct {
	Bright int    `yaml:"bright"`
	Dark   int    `yaml:"dark"`
	Icon   string `yaml:"icon"`
}

// Colors are SGR parameter strings ("38;5;80"), not escape sequences.
type Colors struct {
	Accent  string `yaml:"accent"`
	OK      string `yaml:"ok"`
	Warn    string `yaml:"warn"`
	Err     string `yaml:"err"`
	Dim     string `yaml:"dim"`
	Faint   string `yaml:"faint"`
	Soft    string `yaml:"soft"`
	Label   string `yaml:"label"`
	Token   string `yaml:"token"`
	Neutral string `yaml:"neutral"`
	Track   string `yaml:"track"`
	Prompt  string `yaml:"prompt"`
}

// Glyphs default to characters the Windows Consolas font has.
type Glyphs struct {
	OK       string `yaml:"ok"`        // √
	Err      string `yaml:"err"`       // ×
	Warn     string `yaml:"warn"`      // !
	Info     string `yaml:"info"`      // i
	Command  string `yaml:"command"`   // »
	Active   string `yaml:"active"`    // ►
	Pending  string `yaml:"pending"`   // ○
	Dot      string `yaml:"dot"`       // ●
	Bar      string `yaml:"bar"`       // █
	Rule     string `yaml:"rule"`      // ━ (stage banners)
	RuleThin string `yaml:"rule_thin"` // ─ (sections, footer)
	Sep      string `yaml:"sep"`       // ·
	Gutter   string `yaml:"gutter"`    // │
	Arrow    string `yaml:"arrow"`     // →
	Title    string `yaml:"title"`     // ♦
	Section  string `yaml:"section"`   // ▸ (default section icon)
}

// Theme is the console look. Defaults reproduce the Python updater's screens.
type Theme struct {
	Margin       int               `yaml:"margin"`
	LabelWidth   int               `yaml:"label_width"`
	StatusWidth  int               `yaml:"status_width"`
	MaxWidth     int               `yaml:"max_width"`
	BarBand      int               `yaml:"bar_band"`
	BarFrom      [3]int            `yaml:"bar_from"`
	BarTo        [3]int            `yaml:"bar_to"`
	DateTopRight bool              `yaml:"date_top_right"`
	Taskbar      bool              `yaml:"taskbar"`
	Flash        bool              `yaml:"flash"`
	TitleUpdate  string            `yaml:"title_update"`
	TitleCheck   string            `yaml:"title_check"`
	Colors       Colors            `yaml:"colors"`
	Glyphs       Glyphs            `yaml:"glyphs"`
	Hues         map[string]Hue    `yaml:"hues"`          // per stage group
	SectionIcons map[string]string `yaml:"section_icons"` // section title prefix -> icon
}

// Mirror configures the optional local git mirror. The mirror's location is
// machine state (safe-update.json); Path here is only a fallback.
type Mirror struct {
	Path          string  `yaml:"path"`
	Offer         bool    `yaml:"offer"`
	OfferTimeoutS int     `yaml:"offer_timeout_s"`
	CronSchedule  string  `yaml:"cron_schedule"`
	JobName       string  `yaml:"job_name"`
	KeepFreeGB    float64 `yaml:"keep_free_gb"`
	Growth        float64 `yaml:"growth"`
}

// Settings configures the desktop settings backup and migration.
type Settings struct {
	AutoMigrate     bool     `yaml:"auto_migrate"`     // run after every verified update
	BackupRetention int      `yaml:"backup_retention"` // newest N backups kept
	BackupDir       string   `yaml:"backup_dir"`       // default <hermes_home>/backups
	Origin          string   `yaml:"origin"`           // default: detected from the installed build
	KeepNew         []string `yaml:"keep_new"`         // live-state keys: the new origin's value stays
	Union           []string `yaml:"union"`            // session-keyed maps/pair lists: union, new wins per entry
}

// HookWhen names the point in the flow at which a hook runs.
type HookWhen string

const (
	HookPreClose     HookWhen = "pre-close"
	HookPostUpdate   HookWhen = "post-update"
	HookPostRelaunch HookWhen = "post-relaunch"
)

// DefaultHookTimeoutS applies when a hook has no timeout_s.
const DefaultHookTimeoutS = 300

// Hook is a user command run at a fixed point of the flow. Its last output
// line becomes its summary-card row; a non-zero exit is a WARN only.
type Hook struct {
	Name     string   `yaml:"name"`
	When     HookWhen `yaml:"when"`
	Run      []string `yaml:"run"` // argv; no shell unless argv[0] is one
	TimeoutS int      `yaml:"timeout_s"`
	Dir      string   `yaml:"dir"` // working directory; default the Hermes home
}

// Timeout returns the hook's timeout as a duration.
func (h Hook) Timeout() time.Duration { return secs(h.TimeoutS) }

// Logging configures the log file.
type Logging struct {
	Level     string `yaml:"level"` // debug | info | warn | error
	MaxSizeMB int    `yaml:"max_size_mb"`
	Keep      int    `yaml:"keep"`
}

// HooksFor returns the hooks for one point of the flow, in file order.
func (f File) HooksFor(w HookWhen) []Hook {
	var out []Hook
	for _, h := range f.Hooks {
		if h.When == w {
			out = append(out, h)
		}
	}
	return out
}
