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

// NewerRelease is the answer to "has a release newer than this commit been
// published?" on a release channel.
type NewerRelease struct {
	Ahead   bool   // the latest published release is past the commit
	Tag     string // that release's tag ("v0.22.2"), set whenever the API answered
	Commits *int   // how many commits it is past the commit (Ahead only)
}

// ReleaseSeed is the result of SeedRelease: the usual seed facts about the
// checkout plus the commit the release resolved to.
type ReleaseSeed struct {
	SeedResult
	// TargetSHA is the full sha of the release commit now in the checkout
	// ("" when the seed was not used).
	TargetSHA string
}

// Compare is the GitHub REST compare result.
type Compare struct {
	Commits       int
	Files         []string
	FilesComplete bool // < 300 files: a missing file proves "unchanged"
	// Status is GitHub's word for the comparison: ahead, identical, behind or
	// diverged. Files are only the base-to-target changes for ahead and
	// identical; for the other two they are empty and FilesComplete is false.
	Status string
}

// Service is used by internal/update (check chain) and the `mirror` CLI.
type Service interface {
	Status() Status
	// Seed copies new commits from the mirror into checkout (never raises).
	Seed(ctx context.Context, checkout string) SeedResult
	// SeedRelease does the same for a release channel: the commit of release
	// tag (vX.Y.Z, strict) that Hermes named by sha12 reaches the checkout
	// from the mirror, which is refreshed first when it lacks the tag. The
	// checkout's origin stays GitHub. On a release channel Seed is not used.
	SeedRelease(ctx context.Context, checkout, tag, sha12 string) ReleaseSeed
	// APICompare asks GitHub for base...main; nil when unavailable.
	APICompare(ctx context.Context, base string) *Compare
	// APICompareTo asks GitHub for base...target, where base is a full sha
	// and target a strict release tag or a full sha; nil when unavailable.
	APICompareTo(ctx context.Context, base, target string) *Compare
	// NewerThan asks, within a few seconds and without touching the
	// checkout or the mirror, whether main moved past head (a full sha):
	// the GitHub API first, then `git ls-remote`. ok=false when neither
	// could answer. checkout may be empty.
	NewerThan(ctx context.Context, checkout, head string) (n Newer, ok bool)
	// NewerRelease asks, within a few seconds, whether the latest published
	// release (GitHub releases/latest: strict vX.Y.Z, not a draft or
	// prerelease) is past head, a full sha. API only; ok=false when it
	// cannot tell.
	NewerRelease(ctx context.Context, head string) (n NewerRelease, ok bool)
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
