package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeStoreFile writes one fake LevelDB file holding the given raw text and
// sets its modification time.
func writeStoreFile(t *testing.T, userData, name, text string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(userData, "Local Storage", "leveldb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestScanOriginsFindsCandidatesWithRecency(t *testing.T) {
	ud := t.TempDir()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	writeStoreFile(t, ud, "000005.ldb", "META:file://\x00x_file://\x00\x01k", t0)
	writeStoreFile(t, ud, "000010.ldb", "_http://127.0.0.1:47891\x00\x01k _file://\x00\x01j", t0.Add(time.Hour))
	writeStoreFile(t, ud, "000011.log", "META:http://127.0.0.1:51234 https://example.com:443", t0.Add(2*time.Hour))
	// a stored VALUE that mentions a dev-server URL is not an origin
	writeStoreFile(t, ud, "000012.log", `{"link":"http://127.0.0.1:5173/x"}`, t0.Add(3*time.Hour))
	got, err := scanOrigins(filepath.Join(ud, "Local Storage"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{
		"file://":                t0.Add(time.Hour),
		"http://127.0.0.1:47891": t0.Add(time.Hour),
		"http://127.0.0.1:51234": t0.Add(2 * time.Hour),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if !got[k].Equal(v) {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
}

func TestScanOriginsMissingStore(t *testing.T) {
	_, err := scanOrigins(filepath.Join(t.TempDir(), "Local Storage"))
	if err == nil || !strings.Contains(err.Error(), "leveldb") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultPortOrigin(t *testing.T) {
	co := t.TempDir()
	p := filepath.Join(co, "apps", "desktop", "electron", "renderer-server.ts")
	if _, err := defaultPortOrigin(co); err == nil {
		t.Error("missing source: want error")
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("import x\r\nconst DEFAULT_PORT = 47891\r\n"), 0o644)
	got, err := defaultPortOrigin(co)
	if err != nil || got != "http://127.0.0.1:47891" {
		t.Errorf("got %q, %v", got, err)
	}
	_ = os.WriteFile(p, []byte("const PORT = 1\n"), 0o644)
	if _, err := defaultPortOrigin(co); err == nil {
		t.Error("no DEFAULT_PORT: want error")
	}
}

func TestChooseTarget(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	def := "http://127.0.0.1:47891"
	tests := []struct {
		name     string
		override string
		def      string
		found    map[string]time.Time
		want     string
		reason   string
	}{
		{"override wins", "http://127.0.0.1:9", def, map[string]time.Time{def: t0}, "http://127.0.0.1:9", "--origin"},
		{"default port", "", def, map[string]time.Time{"file://": t0, def: t0}, def, "DEFAULT_PORT"},
		{"default not yet in store", "", def, map[string]time.Time{"file://": t0}, def, "DEFAULT_PORT"},
		{"newer fallback port wins", "", def, map[string]time.Time{def: t0, "http://127.0.0.1:51234": t0.Add(time.Minute)}, "http://127.0.0.1:51234", "fallback port 51234"},
		{"older fallback port loses", "", def, map[string]time.Time{def: t0.Add(time.Minute), "http://127.0.0.1:51234": t0}, def, "DEFAULT_PORT"},
		{"fallback port when default absent", "", def, map[string]time.Time{"file://": t0, "http://127.0.0.1:51234": t0}, "http://127.0.0.1:51234", "fallback port 51234"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseTarget(tc.override, tc.def, tc.found)
			if got.URL != tc.want || !strings.Contains(got.Reason, tc.reason) {
				t.Errorf("got %+v, want %s (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

func TestChooseSource(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	keys := map[string]int{"file://": 5, "http://127.0.0.1:47891": 3, "http://127.0.0.1:51234": 2, "http://127.0.0.1:1": 0}
	rec := map[string]time.Time{"file://": t0, "http://127.0.0.1:47891": t0.Add(time.Hour), "http://127.0.0.1:51234": t0.Add(2 * time.Hour), "http://127.0.0.1:1": t0.Add(3 * time.Hour)}
	if got := chooseSource("http://127.0.0.1:51234", keys, rec, nil); got != "http://127.0.0.1:47891" {
		t.Errorf("most recent non-target origin with keys: got %q", got)
	}
	if got := chooseSource("http://127.0.0.1:47891", map[string]int{"http://127.0.0.1:47891": 3}, rec, nil); got != "" {
		t.Errorf("only target: got %q", got)
	}
}

// After LevelDB compaction several origins share one .ldb, so their
// recency ties; the stale file:// must not win over the previous port.
func TestChooseSourceTieBreak(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	const (
		file = "file://"
		p1   = "http://127.0.0.1:47891"
		p2   = "http://127.0.0.1:49000"
		tgt  = "http://127.0.0.1:51234"
	)
	tests := []struct {
		name string
		keys map[string]int
		prev map[string]bool // origins that were a migration target before
		want string
	}{
		{"http beats file:// without a marker", map[string]int{file: 40, p1: 60}, nil, p1},
		{"previous migration target beats file://", map[string]int{file: 40, p1: 60}, map[string]bool{p1: true}, p1},
		{"previous migration target beats another http origin",
			map[string]int{file: 40, p1: 60, p2: 10}, map[string]bool{p2: true}, p2},
		{"lexical among equals", map[string]int{p1: 1, p2: 1}, nil, p1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := map[string]time.Time{}
			for o := range tc.keys {
				rec[o] = t0
			}
			if got := chooseSource(tgt, tc.keys, rec, tc.prev); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// recency still decides first: a newer file:// wins over an older target
	rec := map[string]time.Time{file: t0.Add(time.Hour), p1: t0}
	if got := chooseSource(tgt, map[string]int{file: 1, p1: 1}, rec, map[string]bool{p1: true}); got != file {
		t.Errorf("recency first: got %q", got)
	}
}

func TestMarkerPreviousTargetsAndSourceHash(t *testing.T) {
	now := time.Date(2026, 10, 1, 3, 15, 0, 0, time.UTC)
	m := &marker{Migrations: map[string]markerEntry{}}
	m.record("file://", "http://127.0.0.1:47891", "abc", now)
	if !reflect.DeepEqual(m.targets(), map[string]bool{"http://127.0.0.1:47891": true}) {
		t.Errorf("targets = %v", m.targets())
	}
	if !m.sourceUnchanged("file://", "http://127.0.0.1:47891", "abc") {
		t.Error("same hash: want unchanged")
	}
	if m.sourceUnchanged("file://", "http://127.0.0.1:47891", "def") {
		t.Error("other hash: want changed")
	}
	if m.sourceUnchanged("file://", "http://127.0.0.1:51234", "abc") {
		t.Error("other pair: want changed")
	}
	// a marker written before hashes existed never counts as unchanged
	m.Migrations["x -> y"] = markerEntry{First: "t", Last: "t", Runs: 1}
	if m.sourceUnchanged("x", "y", "") {
		t.Error("legacy entry without hash: want changed")
	}
}

func TestDumpHashIsCanonical(t *testing.T) {
	a := dumpHash(map[string]string{"a": "1", "b": "2"})
	b := dumpHash(map[string]string{"b": "2", "a": "1"})
	if a != b || a == "" {
		t.Errorf("order-dependent or empty: %q %q", a, b)
	}
	if dumpHash(map[string]string{"a": "1", "b": "3"}) == a {
		t.Error("value change not detected")
	}
	// key/value boundaries must not collide
	if dumpHash(map[string]string{"ab": "c"}) == dumpHash(map[string]string{"a": "bc"}) {
		t.Error("boundary collision")
	}
}

func TestMarkerFirstRunThenLater(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 3, 15, 0, 0, time.UTC)
	m, err := loadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.firstRun("file://", "http://127.0.0.1:47891") {
		t.Error("empty marker: want first run")
	}
	m.record("file://", "http://127.0.0.1:47891", "h1", now)
	if err := m.save(dir); err != nil {
		t.Fatal(err)
	}
	m2, err := loadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2.firstRun("file://", "http://127.0.0.1:47891") {
		t.Error("after record: want later run")
	}
	if !m2.firstRun("file://", "http://127.0.0.1:51234") {
		t.Error("a new target origin is a new origin change: want first run")
	}
	m2.record("file://", "http://127.0.0.1:47891", "h2", now.Add(time.Hour))
	e := m2.Migrations["file:// -> http://127.0.0.1:47891"]
	if e.First != "2026-10-01T03:15:00Z" || e.Last != "2026-10-01T04:15:00Z" || e.Runs != 2 || e.SourceSHA256 != "h2" {
		t.Errorf("entry = %+v", e)
	}
	if !reflect.DeepEqual(m2.Version, markerVersion) {
		t.Errorf("version %v", m2.Version)
	}
}

func TestMarkerCorruptIsError(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, markerFile), []byte("{nope"), 0o644)
	if _, err := loadMarker(dir); err == nil || !strings.Contains(err.Error(), markerFile) {
		t.Errorf("err = %v", err)
	}
}

// RT-4F: a build without renderer-server.ts whose main.ts loads the
// renderer from disk targets file://; anything unrecognised is an error.
func TestDefaultPortOriginWithoutRendererServer(t *testing.T) {
	co := t.TempDir()
	main := filepath.Join(co, "apps", "desktop", "electron", "main.ts")
	if _, err := defaultPortOrigin(co); err == nil || !strings.Contains(err.Error(), "pass --origin") {
		t.Errorf("no files: err = %v", err)
	}
	_ = os.MkdirAll(filepath.Dir(main), 0o755)
	cases := []struct {
		name, src, want string
	}{
		{"file load", "return `${pathToFileURL(resolveRendererIndex()).toString()}?win=quick#/`", "file://"},
		{"file load with fallback arg", "pathToFileURL(rendererIndex || resolveRendererIndex()).toString()", "file://"},
		{"mentions it only in a comment", "// renderer-server.ts is gone\nconst u = pathToFileURL(resolveRendererIndex())", "file://"},
		{"imports renderer-server", "import { s } from './renderer-server'\npathToFileURL(resolveRendererIndex())", ""},
		{"no file load", "const x = 1", ""},
		{"only a definition", "function resolveRendererIndex() {}", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.WriteFile(main, []byte(tc.src), 0o644)
			got, err := defaultPortOrigin(co)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("want an error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestChooseTargetFileOrigin(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// an old 127.0.0.1 origin in the store, even a newer one, is never preferred over file://
	found := map[string]time.Time{"file://": t0, "http://127.0.0.1:47891": t0.Add(time.Hour)}
	got := chooseTarget("", "file://", found)
	if got.URL != "file://" || !strings.Contains(got.Reason, "no renderer-server.ts in this build") {
		t.Errorf("got %+v", got)
	}
	// and the source is the 127.0.0.1 origin that holds keys
	src := chooseSource("file://", map[string]int{"file://": 9, "http://127.0.0.1:47891": 3}, found, map[string]bool{"http://127.0.0.1:47891": true})
	if src != "http://127.0.0.1:47891" {
		t.Errorf("source = %q", src)
	}
	if got := chooseTarget("http://127.0.0.1:9", "file://", found); got.URL != "http://127.0.0.1:9" {
		t.Errorf("--origin must win: %+v", got)
	}
}
