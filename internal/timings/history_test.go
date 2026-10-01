package timings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bp(b bool) *bool { return &b }

func rec(kind string, ok *bool, steps ...string) Record {
	r := Record{Started: "2026-10-01T10:00:00", Kind: kind, OK: ok, Total: 1}
	for _, s := range steps {
		r.Steps = append(r.Steps, StepSeconds{Key: s, Seconds: 1.5})
	}
	return r
}

func TestLoadHistoryMissingIsEmpty(t *testing.T) {
	h, err := LoadHistory(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || len(h) != 0 {
		t.Fatalf("h=%v err=%v", h, err)
	}
}

func TestLoadHistoryCorruptIsEmptyWithError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(p, []byte("[{trunc"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHistory(p)
	if err == nil || len(h) != 0 || !strings.Contains(err.Error(), "t.json") {
		t.Fatalf("h=%v err=%v", h, err)
	}
}

func TestLoadHistoryReadsPythonFormatAndSkipsBadRecords(t *testing.T) {
	// Python wrote indent=1 records; legacy fields are tolerated, non-object
	// entries are skipped instead of losing the whole file.
	py := `[
 {"started": "2026-09-30T08:00:00", "kind": "update", "ok": true, "total": 610.2,
  "steps": {"local": 1.1, "remote": 70.0, "desktop": 200.5, "verify": 4.0},
  "commits": 12, "pydeps": false, "npm": true, "source": "api",
  "cond": {"source": "api", "mirror": false, "npm": true, "pydeps": false, "commits": 12},
  "seeded_from_log": true},
 7,
 {"started": "2026-09-30T09:00:00", "kind": "check", "ok": true, "total": 80, "steps": {"local": 1}}
]`
	p := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(p, []byte(py), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHistory(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Steps[2].Key != "desktop" || h[0].Cond == nil || h[0].Cond.Source != "api" || *h[0].Commits != 12 {
		t.Fatalf("got %+v", h)
	}
}

func TestSaveHistoryAppendsAtomicallyIndent1(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "t.json")
	if err := SaveHistory(p, rec(KindCheck, bp(true), "local"), 40); err != nil {
		t.Fatal(err)
	}
	if err := SaveHistory(p, rec(KindUpdate, bp(true), "local", "verify"), 40); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "[\n {\n  \"started\"") {
		t.Errorf("want indent=1 layout, got %q", string(b[:min(40, len(b))]))
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) != 2 || raw[1]["kind"] != "update" {
		t.Fatalf("raw=%v err=%v", raw, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestSaveHistorySkipsRecordWithoutSteps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.json")
	if err := SaveHistory(p, rec(KindUpdate, nil), 40); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file written for an empty record: %v", err)
	}
}

func TestSaveHistoryRetentionKeepsFullUpdatesSeparately(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.json")
	keep := 3
	// 4 full updates, then 5 checks/short updates interleaved
	var want []string
	for i := 0; i < 4; i++ {
		r := rec(KindUpdate, bp(true), "local", "desktop")
		r.Started = "full-" + string(rune('a'+i))
		if err := SaveHistory(p, r, keep); err != nil {
			t.Fatal(err)
		}
		c := rec(KindCheck, bp(true), "local")
		c.Started = "other-" + string(rune('a'+i))
		if err := SaveHistory(p, c, keep); err != nil {
			t.Fatal(err)
		}
	}
	short := rec(KindUpdate, bp(false), "local", "remote") // not full: no desktop/verify
	short.Started = "other-e"
	if err := SaveHistory(p, short, keep); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHistory(p)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range h {
		got = append(got, r.Started)
	}
	want = []string{"full-b", "full-c", "other-c", "full-d", "other-d", "other-e"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("kept %v, want %v (original order, separate pools)", got, want)
	}
}

func TestSaveHistoryReplacesCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(p, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveHistory(p, rec(KindCheck, bp(true), "local"), 40); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHistory(p)
	if err != nil || len(h) != 1 {
		t.Fatalf("h=%v err=%v", h, err)
	}
}
