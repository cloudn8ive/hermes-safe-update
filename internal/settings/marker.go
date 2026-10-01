package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
)

// markerFile records which origin changes were already migrated (D10: plain
// prefs old-wins only on the first migration after an origin change). It
// lives in the backup directory and is never pruned.
const markerFile = "settings-migrations.json"

const markerVersion = 1

type markerEntry struct {
	First string `json:"first"` // RFC 3339 UTC
	Last  string `json:"last"`
	Runs  int    `json:"runs"`
	// SourceSHA256 is dumpHash of the source origin as last migrated. A later
	// run whose source still hashes the same has nothing new to bring over,
	// so it writes nothing (keys the user removed in the target stay removed).
	SourceSHA256 string `json:"source_sha256,omitempty"`
}

type marker struct {
	Version    int                    `json:"version"`
	Migrations map[string]markerEntry `json:"migrations"` // "<source> -> <target>"
}

func markerKey(source, target string) string { return source + " -> " + target }

// dumpHash is a canonical sha256 of one origin's keys and values (sorted by
// key, length-prefixed so boundaries cannot collide). Only the hash is
// stored, never the values.
func dumpHash(kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s%d:%s", len(k), k, len(kv[k]), kv[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadMarker reads the marker; missing = empty, corrupt = error naming it
// (a silently empty marker would re-apply old-wins over newer prefs).
func loadMarker(dir string) (*marker, error) {
	p := filepath.Join(dir, markerFile)
	m := &marker{Version: markerVersion, Migrations: map[string]markerEntry{}}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", p, err)
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("%q is corrupt (fix or delete it): %w", p, err)
	}
	if m.Migrations == nil {
		m.Migrations = map[string]markerEntry{}
	}
	return m, nil
}

func (m *marker) firstRun(source, target string) bool {
	_, done := m.Migrations[markerKey(source, target)]
	return !done
}

// sourceUnchanged: this pair migrated before and the source still has the
// same content (an entry without a hash, from an older marker, never counts).
func (m *marker) sourceUnchanged(source, target, hash string) bool {
	e, ok := m.Migrations[markerKey(source, target)]
	return ok && e.SourceSHA256 != "" && e.SourceSHA256 == hash
}

// targets returns every origin that was migrated into before.
func (m *marker) targets() map[string]bool {
	out := map[string]bool{}
	for k := range m.Migrations {
		if i := strings.LastIndex(k, " -> "); i >= 0 {
			out[k[i+len(" -> "):]] = true
		}
	}
	return out
}

func (m *marker) record(source, target, sourceHash string, now time.Time) {
	k := markerKey(source, target)
	e := m.Migrations[k]
	stamp := now.UTC().Format(time.RFC3339)
	if e.First == "" {
		e.First = stamp
	}
	e.Last = stamp
	e.Runs++
	e.SourceSHA256 = sourceHash
	m.Migrations[k] = e
}

func (m *marker) save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", dir, err)
	}
	m.Version = markerVersion
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, markerFile), append(b, '\n'), 0o644)
}
