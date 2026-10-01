package timings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
)

// Owner: I1b. Read and write logs/safe-update-timings.json.
//
// Contract (python-behaviour.md §4.2):
//   - Load: missing file = empty history; unreadable/corrupt = empty history
//     plus a returned error the caller logs as a note (history never blocks
//     a run). Records written by the Python tool are accepted; records
//     without "cond" are "condition unknown" (do NOT port KNOWN_COND).
//   - Save: append rec, then trim: full update records (kind "update" with a
//     "desktop" or "verify" step) keep the newest keep; all other records the
//     newest keep separately; original order preserved; write atomically
//     (fsx.WriteFileAtomic, D6), indent 1. Skip saving when rec has no steps.
//   - Plain mode saves too (D5).

// LoadHistory reads path. A single malformed record is skipped, not the
// whole file.
func LoadHistory(path string) ([]Record, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read timings %q: %w", path, err)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("timings %q is not a JSON list: %w", path, err)
	}
	out := make([]Record, 0, len(raw))
	for _, m := range raw {
		var r Record
		if json.Unmarshal(m, &r) != nil || r.Kind == "" {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// IsFull reports whether r is a full update (it got as far as the build or
// the verification), which has its own retention pool.
func (r Record) IsFull() bool {
	if r.Kind != KindUpdate {
		return false
	}
	_, d := r.Steps.Get("desktop")
	_, v := r.Steps.Get("verify")
	return d || v
}

// SaveHistory appends rec to path and applies retention.
func SaveHistory(path string, rec Record, keep int) error {
	if len(rec.Steps) == 0 {
		return nil
	}
	hist, _ := LoadHistory(path) // corrupt history is replaced, never fatal
	hist = trim(append(hist, rec), keep)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(hist); err != nil {
		return fmt.Errorf("encode timings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("timings dir: %w", err)
	}
	return fsx.WriteFileAtomic(path, bytes.TrimRight(buf.Bytes(), "\n"), 0o644)
}

// trim keeps the newest keep records of each pool, in original order.
func trim(h []Record, keep int) []Record {
	if keep <= 0 {
		return h
	}
	var fullN, otherN int
	for _, r := range h {
		if r.IsFull() {
			fullN++
		} else {
			otherN++
		}
	}
	dropFull, dropOther := max(0, fullN-keep), max(0, otherN-keep)
	out := make([]Record, 0, len(h))
	for _, r := range h {
		if r.IsFull() {
			if dropFull > 0 {
				dropFull--
				continue
			}
		} else if dropOther > 0 {
			dropOther--
			continue
		}
		out = append(out, r)
	}
	return out
}
