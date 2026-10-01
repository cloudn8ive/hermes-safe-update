package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
)

// MirrorState is the "mirror" object of safe-update.json.
type MirrorState struct {
	Path  string `json:"path"`
	SetUp string `json:"set_up"` // ISO 8601, local time with offset (Python format)
}

// MachineState is safe-update.json: facts about this machine written by the
// tool (never synced between machines). The existing Python format is kept;
// unknown top-level fields survive a load/save round trip.
type MachineState struct {
	Mirror      *MirrorState
	MirrorOffer string // "declined" = never offer the mirror again

	extra map[string]json.RawMessage
}

const mirrorOfferDeclined = "declined"

// MirrorOfferDeclined reports the user's "N, never ask again" answer.
func (s MachineState) MirrorOfferDeclined() bool { return s.MirrorOffer == mirrorOfferDeclined }

// DeclineMirrorOffer records "never ask again".
func (s *MachineState) DeclineMirrorOffer() { s.MirrorOffer = mirrorOfferDeclined }

// LoadMachineState reads path. A missing file is an empty state; a corrupt
// file is an error naming the path (the caller decides whether to go on).
func LoadMachineState(path string) (MachineState, error) {
	var st MachineState
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read machine state %q: %w", path, err)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 {
		return st, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return st, fmt.Errorf("machine state %q is not a JSON object: %w", path, err)
	}
	if m, ok := raw["mirror"]; ok {
		var ms MirrorState
		if err := json.Unmarshal(m, &ms); err != nil {
			return st, fmt.Errorf("machine state %q: field mirror: %w", path, err)
		}
		st.Mirror = &ms
		delete(raw, "mirror")
	}
	if m, ok := raw["mirror_offer"]; ok {
		_ = json.Unmarshal(m, &st.MirrorOffer)
		delete(raw, "mirror_offer")
	}
	st.extra = raw
	return st, nil
}

// SaveMachineState writes path atomically in the Python layout (indent 1).
// If the file on disk exists but does not parse (a truncated write, a hand
// edit gone wrong), it is first renamed to <path>.corrupt-<UTC stamp> so the
// old content is never silently lost.
func SaveMachineState(path string, st MachineState) error {
	if err := setAsideIfCorrupt(path); err != nil {
		return err
	}
	out := map[string]any{}
	for k, v := range st.extra {
		out[k] = v
	}
	if st.Mirror != nil {
		out["mirror"] = st.Mirror
	}
	if st.MirrorOffer != "" {
		out["mirror_offer"] = st.MirrorOffer
	}
	data, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		return fmt.Errorf("encode machine state: %w", err)
	}
	data = append(data, '\n')
	return fsx.WriteFileAtomic(path, data, 0o600)
}

// setAsideIfCorrupt renames an existing, unparsable machine-state file to
// <path>.corrupt-<UTC stamp>. A missing or valid file is left alone.
func setAsideIfCorrupt(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil // missing (or unstat-able: the write below will report it)
	}
	if _, err := LoadMachineState(path); err == nil {
		return nil
	}
	aside := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := fsx.Rename(path, aside); err != nil {
		return fmt.Errorf("keep corrupt machine state %q aside: %w", path, err)
	}
	return nil
}
