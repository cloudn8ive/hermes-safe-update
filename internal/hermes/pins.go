package hermes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Plugins pinned with --ref (Hermes v0.22.7 and v0.23.0; Safe Update v0.8.1).
//
// `hermes plugins install <name> --ref <sha>` puts a catalog plugin on a
// commit of the user's choosing. Hermes v0.23.0 leaves such a plugin alone in
// the no-name `hermes plugins update` and in the gateway's auto-apply
// (plugins_auto_update.held_with_ref); the named form `hermes plugins update
// <name>`, which Safe Update uses, moves it off the pin. `plugins
// check-updates --json` has no field for it, so Safe Update reads the record
// the installer keeps, read-only:
//
//	<HERMES_HOME>/plugins/.install-metadata.json   (same shape in v0.22.7 and v0.23.0)
//	{ "<name>": { "pinned": bool, "revision": "<sha>", "source": "...",
//	              "catalog": { "name", "repo", "tier", "sha", "pin" } } }
//
// A catalog install records "pinned": true too (it is installed at the
// reviewed catalog pin); what marks a --ref install is "catalog.pin" being
// empty (the commit is off the reviewed pin). Hermes' own predicate is
// held_with_ref: catalog block + pinned is True + empty catalog.pin. A
// non-catalog (git URL) install with "pinned": true was installed with --ref
// too; Hermes refuses to update it ("is pinned; reinstall with an explicit
// --ref").
//
// Fail toward not touching: a plugin is "not pinned" only when its record is
// positively recognised as unpinned. A file that cannot be read, a record
// that is missing or has a shape this code does not know, are "unknown" and
// the plugin is skipped with a note.

// PinKind is what the install record says about one plugin.
type PinKind int

const (
	// PinNone: the record is recognised and the plugin is not held.
	PinNone PinKind = iota
	// PinRef: held with --ref (catalog install off the reviewed pin).
	PinRef
	// PinGit: a git install pinned to a revision (Hermes refuses to update it).
	PinGit
	// PinUnknown: the record could not be read or is not a known shape.
	PinUnknown
)

// PluginPin is the verdict for one plugin.
type PluginPin struct {
	Kind PinKind
	// Ref is the pinned revision (PinRef, PinGit); empty when the record has none.
	Ref string
	// Why is the reason for PinUnknown, for the log and the note.
	Why string
}

// Held reports whether Safe Update must not update the plugin.
func (p PluginPin) Held() bool { return p.Kind != PinNone }

// PinStore is the parsed record file.
type PinStore struct {
	rows map[string]json.RawMessage
	err  string // the whole file is unusable
	// Absent: the file does not exist (Hermes reads that as an empty record).
	Absent bool
}

// InstallMetadataPath is the record file under a Hermes home.
func InstallMetadataPath(home string) string {
	return filepath.Join(home, "plugins", ".install-metadata.json")
}

// ReadPinStore reads the record file of a Hermes home, read-only. It never
// fails: an unusable file is a store whose every answer is PinUnknown.
func ReadPinStore(home string) PinStore {
	b, err := os.ReadFile(InstallMetadataPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return PinStore{Absent: true}
	}
	if err != nil {
		return PinStore{err: "the install record cannot be read (" + err.Error() + ")"}
	}
	return ParsePinStore(b)
}

// ParsePinStore parses the bytes of the record file (a UTF-8 BOM is
// tolerated, as Hermes reads it with utf-8-sig).
func ParsePinStore(b []byte) PinStore {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var rows map[string]json.RawMessage
	if err := json.Unmarshal(b, &rows); err != nil || rows == nil {
		return PinStore{err: "the install record is not a JSON object"}
	}
	return PinStore{rows: rows}
}

// pinRecord is the part of a record this code reads. Pointers tell absent
// from present-with-the-wrong-type (json.RawMessage keeps the type check ours).
type pinRecord struct {
	Pinned   json.RawMessage `json:"pinned"`
	Revision json.RawMessage `json:"revision"`
	Catalog  json.RawMessage `json:"catalog"`
	// CatalogName is the older (package-manager era) top-level field.
	CatalogName json.RawMessage `json:"catalog_name"`
}

type pinCatalog struct {
	Pin json.RawMessage `json:"pin"`
	SHA json.RawMessage `json:"sha"`
}

// Pin answers for one plugin. class is the "class" of its check-updates row
// ("catalog" or "git").
func (s PinStore) Pin(name, class string) PluginPin {
	unknown := func(format string, a ...any) PluginPin {
		return PluginPin{Kind: PinUnknown, Why: fmt.Sprintf(format, a...)}
	}
	if s.err != "" {
		return unknown("%s", s.err)
	}
	if s.Absent {
		return unknown("the plugin has no install record (%s does not exist)", filepath.Join("plugins", ".install-metadata.json"))
	}
	raw, ok := s.rows[name]
	if !ok {
		return unknown("the plugin has no install record")
	}
	var rec pinRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return unknown("its install record is not an object")
	}
	if class == "" { // the apply-time re-check does not have the row: go by the record's shape
		class = "git"
		if len(rec.Catalog) > 0 || len(rec.CatalogName) > 0 {
			class = "catalog"
		}
	}
	pinned, perr := optBool(rec.Pinned)
	if perr != "" {
		return unknown("its install record has a %q field that is not true or false", "pinned")
	}
	rev, _ := optString(rec.Revision)
	switch class {
	case "catalog":
		if len(rec.CatalogName) > 0 && len(rec.Catalog) == 0 {
			return unknown("its install record is in the older package-manager format (Hermes converts it on its next update)")
		}
		var blk pinCatalog
		if len(rec.Catalog) == 0 || json.Unmarshal(rec.Catalog, &blk) != nil || rec.Catalog[0] != '{' {
			return unknown("its install record has no catalog block")
		}
		pin, ok := optString(blk.Pin)
		if !ok {
			return unknown("its install record has a catalog pin that is not text")
		}
		if pinned == pinTrue && pin == "" {
			if rev == "" {
				rev, _ = optString(blk.SHA)
			}
			return PluginPin{Kind: PinRef, Ref: rev}
		}
		return PluginPin{}
	case "git":
		if len(rec.Catalog) > 0 {
			return unknown("a git plugin whose install record has a catalog block")
		}
		if pinned == pinTrue {
			return PluginPin{Kind: PinGit, Ref: rev}
		}
		return PluginPin{}
	}
	return unknown("plugins of class %q are not updated by Safe Update", class)
}

type pinTri int

const (
	pinAbsent pinTri = iota
	pinFalse
	pinTrue
)

// optBool reads a JSON bool that may be absent (then pinAbsent, which Hermes
// treats as not pinned: it tests `is True`). Anything but true/false/null/
// absent is an error.
func optBool(r json.RawMessage) (pinTri, string) {
	switch string(bytes.TrimSpace(r)) {
	case "", "null":
		return pinAbsent, ""
	case "true":
		return pinTrue, ""
	case "false":
		return pinFalse, ""
	}
	return pinAbsent, "bad"
}

// optString reads a JSON string that may be absent or null (then "").
func optString(r json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(r)
	if len(t) == 0 || string(t) == "null" {
		return "", true
	}
	var s string
	if json.Unmarshal(t, &s) != nil {
		return "", false
	}
	return s, true
}

// ShortRef is a pinned revision as shown to the user: 8 characters, "?" when
// the record has none.
func ShortRef(ref string) string {
	if ref == "" {
		return "?"
	}
	return ref[:min(8, len(ref))]
}
