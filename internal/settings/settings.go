// Package settings backs up, migrates, verifies and reverts the Hermes
// desktop's UI settings (Chromium localStorage, keyed by origin). It never
// edits LevelDB itself: the embedded migrator JS runs on Hermes' own
// bundled Electron against a COPY of the store (brief "Settings migration
// (spec)", python-behaviour §6.4).
//
// Ownership: this file (interfaces/types) = T1; everything else = I4.
package settings

import (
	"context"
	"time"
)

// Origin is one localStorage origin found in the store.
type Origin struct {
	URL  string // "file://", "http://127.0.0.1:47891"
	Keys int
}

// Target says which origin to migrate into and why (D9).
type Target struct {
	URL    string
	Reason string // "DEFAULT_PORT in renderer-server.ts", "--origin", "store already uses fallback port 51234"
}

// Action of one key in a plan.
type Action string

const (
	ActAdd       Action = "add"      // only on the old side: copy
	ActSame      Action = "same"     // identical
	ActKeepNew   Action = "keep-new" // live-state key: new stays
	ActUnion     Action = "union"    // session map/pair list: union, new wins per entry
	ActOldWins   Action = "old-wins" // plain pref, first migration after an origin change
	ActNewWins   Action = "new-wins" // plain pref, later runs (per-origin marker)
	ActOnlyInNew Action = "only-new" // untouched
)

// KeyPlan is the decision for one key. Values are never logged or put in
// reports, only key names (they can hold session ids).
type KeyPlan struct {
	Key      string
	Action   Action
	Conflict bool // both sides had different values
}

// Plan is a full migration plan from Source to Target.
type Plan struct {
	Source   string
	Target   Target
	Keys     []KeyPlan
	FirstRun bool // no marker for this origin change yet: plain prefs old-wins (D10)
	// SourceUnchanged: this source->target pair migrated before and the
	// source still holds exactly what was migrated then, so nothing is
	// written (Keys is empty); removals made in the target since stay.
	SourceUnchanged bool
}

// Writes counts keys the migration would write (add + old-wins + union).
func (p Plan) Writes() int {
	n := 0
	for _, k := range p.Keys {
		switch k.Action {
		case ActAdd, ActOldWins, ActUnion:
			n++
		}
	}
	return n
}

// Backup is one settings backup on disk.
type Backup struct {
	ID       string // "settings-20261001-031500"
	Path     string
	Created  time.Time
	Manifest Manifest
}

// Manifest is manifest.json in a backup.
type Manifest struct {
	Version       int               `json:"version"`
	Created       string            `json:"created"` // RFC 3339 UTC
	Reason        string            `json:"reason"`  // "before migration", "manual", "before revert"
	HermesBefore  string            `json:"hermes_before,omitempty"`
	HermesAfter   string            `json:"hermes_after,omitempty"`
	Files         map[string]string `json:"files"`           // relative path -> sha256
	KeysPerOrigin map[string]int    `json:"keys_per_origin"` // origin -> key count
}

// Report is the outcome of migrate/revert, shown on the summary card.
type Report struct {
	Changed   bool
	BackupID  string
	Plan      Plan
	Conflicts []string // key names
	Line      string   // one-line summary row
}

// Service is what internal/update and the CLI call. Every mutating method
// refuses with apperr.ErrHermesRunning while the desktop app runs, and
// takes a backup first.
type Service interface {
	// SetHermesVersions sets the "Hermes version before/after" the backup
	// manifests record (empty = unknown).
	SetHermesVersions(before, after string)
	DetectOrigins(ctx context.Context) ([]Origin, error)
	DetectTarget(ctx context.Context) (Target, error)
	Backup(ctx context.Context, reason string) (Backup, error)
	ListBackups() ([]Backup, error)
	// Plan computes the migration without changing anything (--dry-run).
	Plan(ctx context.Context) (Plan, error)
	// Migrate: backup -> copy -> Electron migrate on the copy -> re-plan in a
	// fresh process (add 0 / overwrite 0) -> swap -> verify live -> rollback
	// on any failure. No-op (Changed=false) when nothing is orphaned.
	Migrate(ctx context.Context) (Report, error)
	// Revert restores backup id ("" = newest) and verifies the hashes.
	Revert(ctx context.Context, id string) (Report, error)
	// Prune keeps the newest retention backups.
	Prune(retention int) (removed int, err error)
}
