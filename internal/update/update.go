// Package update is the orchestrated flow: pre-flight, wait for idle,
// confirm, close, `hermes update`, verify, settings migration, relaunch,
// housekeeping, hooks, summary (python-behaviour §2, DECISIONS D1-D18).
// The flow is a list of Stage values; each stage reads and writes the run's
// Facts. All OS, Hermes and UI access goes through the interfaces in Deps,
// so the whole flow is testable with fakes.
//
// Ownership: this file (types/interfaces) = T1; everything else = I1b.
package update

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/cua"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/gc"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/settings"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// Deps are the collaborators of a run. Optional ones may be nil (the stage
// then shows an info row and moves on): Mirror, GC, CUA, Settings, Hooks.
type Deps struct {
	Config   *config.Config
	Platform *platform.Platform
	Runner   execx.Runner
	Log      *slog.Logger
	UI       ui.Renderer
	Clock    timings.Clock
	Sleep    func(ctx context.Context, d time.Duration) error // injectable; returns ctx.Err()

	Locator  hermes.Locator
	Sessions hermes.Sessions
	Gateway  hermes.Gateway
	Repo     hermes.Repo
	Verifier hermes.Verifier

	Mirror   mirror.Service
	GC       gc.Collector
	CUA      cua.Refresher
	Settings settings.Service
	Hooks    HookRunner

	// Marker is the update-in-progress marker shared with Hermes.
	Marker Marker
	// Classify maps a process to "desktop", "gateway", "backend", "kernel"
	// or "" (hermes.Classify bound to the home and desktop markers).
	Classify func(p platform.Process) string
	// SelfPID is this process's pid (0 = os.Getpid()); tests set it so the
	// fake process table can place the updater in a parent chain.
	SelfPID int
	// Getenv reads the updater's environment for the inside-a-chat check
	// (nil = no environment signal, so tests never see the real one).
	Getenv func(string) string
	// SpawnMirrorSetup starts `mirror setup` in its own window (the offer
	// never blocks the run); nil = print the command instead.
	SpawnMirrorSetup func(ctx context.Context) error
	// Wire, when set, completes the install-dependent deps (Gateway, Repo,
	// Verifier, Marker, Classify, GC, CUA, Settings, Hooks) once the
	// install is located. Tests pre-wire Deps and leave it nil.
	Wire func(in hermes.Install, d *Deps)
	// FileExists defaults to os.Stat; tests replace it.
	FileExists func(path string) bool

	Title       string // header / console title
	Mode        Mode
	TimingsPath string // <home>/logs/safe-update-timings.json
	LogPath     string // shown in the summary's Log row
}

// Marker is the `.hermes-update-in-progress` file (`<pid>\n<epoch>\n`).
type Marker interface {
	// Holder returns the pid of a live process holding the marker; stale is
	// true when a marker exists but its owner is dead or unreadable.
	Holder() (pid int, stale bool)
	// Claim writes our pid and the current time.
	Claim() error
	// Refresh rewrites the timestamp while the marker is still ours.
	Refresh() error
	// Release deletes the marker only if it is ours.
	Release() (released bool, err error)
}

// Facts collect what a run learned; stages fill them, the summary reads them.
type Facts struct {
	Install       hermes.Install
	HeadFull      string
	Check         hermes.UpdateCheck
	Procs         map[string]int // class -> count (desktop, gateway, backend, kernel)
	Gate          hermes.GateResult
	GateInfo      string
	GateStopped   bool
	GateLine      string
	DesktopWas    bool // desktop was running before close (relaunch only then)
	Versions      [2]string
	VersionBefore string // `hermes --version` read in pre-flight (settings backup manifest)
	Estimate      time.Duration
	TClose        time.Time
	TReopen       time.Time
	UpdateRC      int
	UpdateOutput  string
	Verified      bool // never flipped by later stages (rule 8)
	VerifyLine    string
	Settings      *settings.Report
	GC            *gc.Result
	CUA           *cua.Result
	HookRows      []ui.Row
	Warnings      int

	HeadShort   string
	DiskLow     bool
	GateProbed  bool
	Busy        []hermes.Session
	Closed      bool // the close stage stopped the desktop (relaunch is due if DesktopWas)
	Relaunched  bool
	GCPreview   *gc.Result
	CUAStatus   *cua.Status
	MirrorState *mirror.Status
}

// Run is the state of one invocation shared by its stages.
type Run struct {
	Deps
	Facts Facts
	Est   *timings.Estimator
	runState

	started      time.Time
	early        chan earlyResult // process list + gate probe, run beside the update check
	claimed      bool
	stopRefresh  func()
	finalized    bool
	timingsSaved bool
}

// Stage is one step of the flow. Key matches a timings.Step key (the footer
// advances on it); Run returns nil to continue, an apperr sentinel to stop
// with that exit code. Skip, when set, is evaluated first.
type Stage struct {
	Key  string
	Run  func(ctx context.Context, r *Run) error
	Skip func(r *Run) bool
}

// Estimate is the stage's current time estimate.
func (s Stage) Estimate(r *Run) time.Duration {
	if r == nil || r.Est == nil {
		return 0
	}
	return r.Est.Estimate(s.Key)
}

// HookRunner runs the user's hooks for one point of the flow. A hook's
// failure or timeout is a WARN row; it never changes the verified result.
type HookRunner interface {
	Run(ctx context.Context, when config.HookWhen, env map[string]string) []HookOutcome
}

// HookOutcome is one hook's result.
type HookOutcome struct {
	Name     string
	ExitCode int
	TimedOut bool
	LastLine string // summary row value
	Took     time.Duration
}

// Mode selects the flow.
type Mode int

const (
	ModeUpdate Mode = iota
	ModeCheck
)
