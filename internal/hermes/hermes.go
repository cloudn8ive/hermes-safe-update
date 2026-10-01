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
	Python   string // Hermes' managed Python ("" = not found: D7 degrade)
	Git      string // bundled git, else PATH git (D4); "" = none
	Electron string // apps/desktop/node_modules/electron/dist/<leaf>
	Desktop  string // source-built desktop executable
	UserData string // Electron userData of the desktop app
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
)

// Gateway reads and controls the Hermes gateway through the launcher.
type Gateway interface {
	State() GatewayState
	// Probe runs the pause-gate probe under Hermes' managed Python
	// (Windows only; other OSes return GateUnknown, "not needed").
	Probe(ctx context.Context) (GateResult, string)
	Stop(ctx context.Context) error  // `gateway stop`, then `--all`, then force
	Start(ctx context.Context) error // `gateway start --all`
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
	// DiffKinds classifies HEAD..origin/main by exit codes only (rule 18).
	DiffKinds(ctx context.Context) (npm, pydeps *bool)
}

// Verifier runs `hermes --run-module hermes_cli.desktop_update_verify` (D17).
type Verifier interface {
	VerifyDesktop(ctx context.Context) (ok bool, output string)
}
