// Package mirror is the optional local bare git mirror of the Hermes repo
// that lets updates copy new commits before Hermes closes
// (python-behaviour §6.1). Every failure falls back to GitHub. Rules: never
// change the checkout's origin URL; the insteadOf rewrite is passed with -c
// to our own git calls only, never to `hermes update`.
//
// Ownership: this file = T1; everything else = I3.
package mirror

import (
	"context"
	"time"
)

// Status is read-only (no git, no network).
type Status struct {
	ConfiguredPath string
	OK             bool // a valid bare repo at the path
	LastOK         time.Time
	LastError      string
	JobExists      bool // Hermes cron job present
	JobActive      bool
	Declined       bool // "never offer"
}

// Complete means nothing to offer: mirror ok and its refresh job active.
func (s Status) Complete() bool { return s.OK && s.JobActive }

// SeedResult of copying origin/main from the mirror into the checkout.
type SeedResult struct {
	Used     bool
	Reason   string // "ok" or why not used
	Commits  *int
	Complete bool // proven: nothing left to download from GitHub
	Took     time.Duration
	// Refreshed: this Seed fetched GitHub into the mirror and it worked.
	Refreshed bool
	// Current: GitHub's API named the very tip the mirror had when it was
	// used (false when the API could not be asked).
	Current bool
}

// Newer is the answer to "has main moved past this commit?".
type Newer struct {
	Ahead   bool // main is past the commit
	Commits *int // how many commits, when the API could count them
}

// Compare is the GitHub REST compare result.
type Compare struct {
	Commits       int
	Files         []string
	FilesComplete bool // < 300 files: a missing file proves "unchanged"
}

// Service is used by internal/update (check chain) and the `mirror` CLI.
type Service interface {
	Status() Status
	// Seed copies new commits from the mirror into checkout (never raises).
	Seed(ctx context.Context, checkout string) SeedResult
	// APICompare asks GitHub for base...main; nil when unavailable.
	APICompare(ctx context.Context, base string) *Compare
	// NewerThan asks, within a few seconds and without touching the
	// checkout or the mirror, whether main moved past head (a full sha):
	// the GitHub API first, then `git ls-remote`. ok=false when neither
	// could answer. checkout may be empty.
	NewerThan(ctx context.Context, checkout, head string) (n Newer, ok bool)
	// Setup clones the mirror (to dir, "" = choose a location), records it in
	// safe-update.json and ensures the refresh cron job.
	Setup(ctx context.Context, dir string) error
	// Refresh fetches into the mirror; silent mode for cron (exit codes only).
	Refresh(ctx context.Context) error
	// Location proposes where a mirror would go and why ("" = nowhere).
	Location(ctx context.Context) (path string, sizeGB float64, why string)
	// Decline records "never offer again".
	Decline() error
}
