// Package cua refreshes the cua-driver (Computer Use) after an update when
// `hermes update` deferred it, and re-points its Windows logon task with a
// single UAC prompt only when the task targets an old binary
// (python-behaviour §2.11). --unattended / --no-elevate never prompt (D16).
// On macOS/Linux the logon-task part is N/A (info row).
//
// Ownership: this file = T1; everything else = I3.
package cua

import "context"

// TaskStatus of the logon task.
type TaskStatus int

const (
	TaskNone    TaskStatus = iota // no task: on-demand mode
	TaskCurrent                   // targets the current binary
	TaskStale                     // targets another binary
	TaskUnknown                   // could not tell
)

// Status is the read-only view for `check`.
type Status struct {
	Task    TaskStatus
	TaskExe string
	Current string // current cua-driver binary from `hermes computer-use status`
	Text    string // row text: "none (on-demand)", "current", "stale (<a> vs pin <b>)"
}

// Options for Refresh.
type Options struct {
	UpdateOutput string // raw `hermes update` output; refresh only if it deferred
	Unattended   bool
	NoElevate    bool
}

// Result of Refresh.
type Result struct {
	Ran  bool
	OK   bool
	Line string // summary row ("0.3.1, logon task current")
}

// Refresher is used by internal/update.
type Refresher interface {
	Status(ctx context.Context) Status
	Refresh(ctx context.Context, o Options) Result
}
