// Package timings holds the step model shared by the update flow and the
// UI, the timing-history file (logs/safe-update-timings.json, existing
// Python format) and the estimator that turns history into the progress bar
// and "about N min left" (python-behaviour.md §4.2, §5.10).
//
// File ownership: types.go = T1; history.go (+tests) = I1b; estimate.go
// (+tests) = I2.
package timings

import "time"

// Step describes one stage key of a run.
type Step struct {
	Key      string // "remote", "desktop", ...
	Group    string // banner group: "Check", "Close Hermes", "Update", "Build", "Finish"
	Label    string // "Checking for updates"
	DefaultS int    // seed estimate in seconds; 0 = waits on the user (shown, never estimated)
	Cond     string // condition name the estimate depends on: "source", "mirror", "npm", "pydeps"; "" = none
	Class    string // "network", "machine" or "" (speed-factor class)
}

// Conditions known for this run (nil pointer = unknown). Mirrors the
// "cond" object of a record.
type Conditions struct {
	Source  string `json:"source,omitempty"` // "mirror" | "api" | "git"
	Mirror  *bool  `json:"mirror,omitempty"`
	NPM     *bool  `json:"npm,omitempty"`
	PyDeps  *bool  `json:"pydeps,omitempty"`
	Commits *int   `json:"commits,omitempty"`
}

// Kind of run.
const (
	KindCheck  = "check"
	KindUpdate = "update"
)

// Record is one run in safe-update-timings.json (Python format, indent=1).
// Unknown fields of old records are tolerated on read and not rewritten.
type Record struct {
	Started string         `json:"started"` // local time "2006-01-02T15:04:05"
	Kind    string         `json:"kind"`
	OK      *bool          `json:"ok"`
	Total   float64        `json:"total"` // seconds, 1 decimal
	Steps   OrderedSeconds `json:"steps"` // insertion order = step-list order
	Commits *int           `json:"commits,omitempty"`
	PyDeps  *bool          `json:"pydeps,omitempty"`
	NPM     *bool          `json:"npm,omitempty"`
	Source  string         `json:"source,omitempty"`
	Cond    *Conditions    `json:"cond,omitempty"`
}

// StepSeconds is one key/duration pair.
type StepSeconds struct {
	Key     string
	Seconds float64
}

// OrderedSeconds keeps the JSON object's key order, because the estimator
// treats the last step of a failed run as cut short.
type OrderedSeconds []StepSeconds

// StartedLayout is the timestamp layout of Record.Started.
const StartedLayout = "2006-01-02T15:04:05"

// Clock is injected so tests never sleep.
type Clock interface{ Now() time.Time }

// SystemClock is the real clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }
