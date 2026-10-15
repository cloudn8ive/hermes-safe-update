// Package hermes knows the Hermes install: where it is and what kind it is,
// versions and HEAD, the gateway state, busy sessions (read-only SQLite),
// the Windows gateway pause-gate probe, desktop build verification, and
// every piece of Hermes output wording the tool depends on (contracts.go,
// D14). It never changes the install; internal/update does.
//
// Ownership: this file (interfaces/types) = T1; everything else in the
// package = I1a. Request interface changes via the I1a/I1b handoff.
package hermes

import (
	"context"
	"time"
)

// InstallKind classifies how Hermes is installed (coordinator note R1).
type InstallKind string

const (
	KindCheckout InstallKind = "checkout" // managed source checkout: the only kind we update
	KindMacApp   InstallKind = "macos-app"
	KindMSIX     InstallKind = "msix"
	KindAppImage InstallKind = "appimage"
	KindDocker   InstallKind = "docker"
	KindNix      InstallKind = "nix"
	KindUnknown  InstallKind = "unknown"
)

// Install is the resolved layout. Paths are absolute; a missing optional
// item is "".
type Install struct {
	Kind     InstallKind
	Home     string // HERMES_HOME (default profile)
	Checkout string // <home>/hermes-agent
	Launcher string // hermes CLI launcher (verified to exist for KindCheckout)
	// LauncherPrefix, when set, replaces the launcher: no launcher works, and
	// Hermes is run through its managed Python as "<python> -I -c <bootstrap>"
	// (Launcher is then only the name callers pass, and may not exist).
	LauncherPrefix []string
	Python         string // Hermes' managed Python ("" = not found: D7 degrade)
	Git            string // bundled git, else PATH git (D4); "" = none
	Electron       string // apps/desktop/node_modules/electron/dist/<leaf>
	Desktop        string // source-built desktop executable
	UserData       string // Electron userData of the desktop app
	// StepAside is the user-facing advice when Kind != KindCheckout
	// ("Hermes.app updates itself: use Check for Updates in the app").
	StepAside string
}

// LocalStorageDir is <userData>/Local Storage (the whole tree is backed up).
func (i Install) LocalStorageDir() string { return joinPath(i.UserData, "Local Storage") }

// MarkerPath is the update-in-progress marker shared with Hermes.
func (i Install) MarkerPath() string { return joinPath(i.Home, MarkerFile) }

// Locator finds the install. Overrides from config.Paths win over detection.
type Locator interface {
	Locate(ctx context.Context) (Install, error)
}

// LauncherResolver is the optional part of a Locator that finds the launcher
// of an install again (the update can replace or delete it): the program to
// run and, for the launcher-free route, the command prefix that stands in for
// it.
type LauncherResolver interface {
	ResolveLauncher(ctx context.Context, in Install) (launcher string, prefix []string, err error)
}

// Session is one busy session (or an unreadable DB, which counts as busy).
type Session struct {
	Profile string        // "hermes" for the root home, else the profile name
	ID      string        // session id, or "<profile>: state.db unreadable"
	Title   string        // first 50 chars; for unreadable DBs the error (60 chars)
	Idle    time.Duration // time since last activity
}

// Sessions reports busy sessions across all profiles (python-behaviour §2.6).
type Sessions interface {
	Busy(ctx context.Context, window time.Duration) ([]Session, error)
}

// GatewayState is gateway_state.json (missing/invalid = zero value).
type GatewayState struct {
	CodeSHA string `json:"code_sha"`
	PID     int    `json:"pid"`
}

// GateResult of the Windows pause-gate probe.
type GateResult int

const (
	GateUnknown GateResult = iota // probe unavailable / errored (D7)
	GateOK
	GateBlocked
	// GateNotApplicable: no probe exists on this OS (macOS/Linux), which is
	// not a failure and not "unknown".
	GateNotApplicable
)

// Gateway reads and controls the Hermes gateway through the launcher.
type Gateway interface {
	State() GatewayState
	// Probe runs the pause-gate probe under Hermes' managed Python
	// (Windows only; other OSes return GateNotApplicable).
	Probe(ctx context.Context) (GateResult, string)
	Stop(ctx context.Context) error // `gateway stop`, then `--all`, then force
	// StopStale stops the one gateway with that pid (`gateway stop`, then a
	// kill of that pid): never `--all`, never another process.
	StopStale(ctx context.Context, pid int) error
	// StartAfterStale starts the gateway StopStale stopped, even though other
	// gateways (other profiles) are running: a plain `gateway start`, never
	// `--all`. It succeeds only when a new gateway process appears.
	StartAfterStale(ctx context.Context, stalePID int) error
	Start(ctx context.Context) error // `gateway start --all` (Windows), `gateway start` (macOS/Linux)
}

// UpdateCheck is the result of the availability chain mirror -> API -> git.
type UpdateCheck struct {
	Behind  *bool  // nil = the check itself failed
	Commits *int   // nil = unknown
	Source  string // "mirror" | "api" | "git"
	NPM     *bool  // Node packages change (nil = unknown)
	PyDeps  *bool  // Python dependencies change
	Detail  string // log line
}

// Repo answers git questions about the checkout (bundled git first, D4).
type Repo interface {
	Head(ctx context.Context) (short, full string, err error)
	DirtyCount(ctx context.Context) (int, error)
	// DiffKinds classifies HEAD..ref by exit codes only (rule 18). ref is
	// MainRef, a release tag ("refs/tags/vX.Y.Z") or a full commit sha;
	// anything else is unknown.
	DiffKinds(ctx context.Context, ref string) (npm, pydeps *bool)
	// ResolveRelease finds a release's commit locally: the tag when it is
	// here (and matches the commit Hermes named), else the commit starting
	// with sha12. ref feeds DiffKinds, sha (40 hex) CommitCount. ("", "") =
	// not available locally or ambiguous: the prediction is unknown.
	ResolveRelease(ctx context.Context, tag, sha12 string) (ref, sha string)
	// CommitCount is `git rev-list --count from..to` (ok=false when git
	// cannot say, e.g. an object is missing). Read-only.
	CommitCount(ctx context.Context, from, to string) (n int, ok bool)
	// IsAncestor is `git merge-base --is-ancestor ancestor descendant`
	// (descendant: a full sha or "HEAD"); ok=false when git cannot say.
	// Decides whether updating to a release would move the checkout back.
	IsAncestor(ctx context.Context, ancestor, descendant string) (yes, ok bool)
	// IsShallow reports a shallow clone (`rev-parse --is-shallow-repository`);
	// ok=false when git cannot say.
	IsShallow(ctx context.Context) (shallow, ok bool)
}

// Verifier runs `hermes --run-module hermes_cli.desktop_update_verify` (D17).
type Verifier interface {
	VerifyDesktop(ctx context.Context) (ok bool, output string)
}
