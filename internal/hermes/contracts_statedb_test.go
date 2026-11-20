package hermes

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompactCapable(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.22.7":                 false,
		"0.22.7":                  false,
		"v0.21.6":                 false,
		"v0.23.0":                 true,
		"v0.23.1":                 true,
		"v0.23.0+5.g1234567":      true,
		"v0.23":                   true,
		"v0.24.0":                 true,
		"v1.0.0":                  true,
		"v0.22.7+300.gabcdef0":    false, // derived from an older release
		"v0.23.0-dev":             true,
		"":                        false,
		"unknown":                 false,
		"v-0.23.1":                false,
		"hermes 0.23.1":           false,
		"  v0.23.1 ":              true,
		"v0.9.99":                 false,
		"v99999999999999999999.0": false, // does not parse as an int: not capable
	} {
		if got := CompactCapable(v); got != want {
			t.Errorf("CompactCapable(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestParseSnapshotSkips(t *testing.T) {
	out := "◆ Pre-update snapshot: 20261110-090000\r\n" +
		"  ⚠ Snapshot: skipping state.db (1.2 GB exceeds 1.0 GB limit)\r\n" +
		"  ⚠ Skipping snapshot prune: DB file(s) skipped for size: state.db\r\n" +
		"! Snapshot: skipping state.db (4.6 GB exceeds 1.0 GB limit)\n" +
		"! Skipping snapshot prune: DB file(s) skipped for size: state.db\n"
	skips, prune := ParseSnapshotSkips(out)
	want := []SnapshotSkip{{"state.db", "1.2 GB", "1.0 GB"}, {"state.db", "4.6 GB", "1.0 GB"}}
	if !reflect.DeepEqual(skips, want) || !prune {
		t.Errorf("skips = %+v prune = %v", skips, prune)
	}
	if s, p := ParseSnapshotSkips("Snapshot: ok\nupdate complete"); len(s) != 0 || p {
		t.Errorf("false positive: %+v %v", s, p)
	}
	// the prune line alone is a signal too
	if s, p := ParseSnapshotSkips("! Skipping snapshot prune: DB file(s) skipped for size: state.db"); len(s) != 0 || !p {
		t.Errorf("prune alone: %+v %v", s, p)
	}
}

func TestOptimizeStorageContract(t *testing.T) {
	if got := strings.Join(OptimizeStorageArgv(""), " "); got != "sessions optimize-storage --yes" {
		t.Errorf("default = %q", got)
	}
	if got := strings.Join(OptimizeStorageArgv("default"), " "); got != "sessions optimize-storage --yes" {
		t.Errorf("default = %q", got)
	}
	if got := strings.Join(OptimizeStorageArgv("work"), " "); got != "-p work sessions optimize-storage --yes" {
		t.Errorf("profile = %q", got)
	}
	for _, a := range OptimizeStorageArgv("x") {
		if a == "--force" {
			t.Error("--force must never be passed")
		}
	}
	if !HasOptimizeStorage(0, "usage: hermes sessions optimize-storage [-h]") || HasOptimizeStorage(2, "usage: hermes sessions optimize-storage") || HasOptimizeStorage(0, "usage: hermes sessions") {
		t.Error("HasOptimizeStorage")
	}
	ref := OptimizeRefusal("noise\n⚠ Refusing `hermes sessions optimize-storage`: another process is using C:\\h\\state.db.\n  pid 1 python.exe gateway run\n")
	if !strings.HasPrefix(ref, "Refusing `hermes sessions optimize-storage`") && !strings.Contains(ref, "pid 1") {
		t.Errorf("refusal = %q", ref)
	}
	if OptimizeRefusal("all fine") != "" {
		t.Error("false refusal")
	}
	if !OptimizeAlreadyCompact("Search index is already on the compact layout — nothing to do.") || OptimizeAlreadyCompact("done") {
		t.Error("already compact")
	}
	if !OptimizeNoDisk("⚠ Not enough free disk to complete safely.") || OptimizeNoDisk("done") {
		t.Error("no disk")
	}
}

func TestSnapshotLimitIsHermes1GiB(t *testing.T) {
	if SnapshotMaxFileSize != 1<<30 {
		t.Errorf("limit = %d", SnapshotMaxFileSize)
	}
}

// Fixtures: Hermes v0.23.1, hermes_cli/sessions_cmd.py, end of
// _cmd_optimize_storage. Exit code is 0 in both.
func TestOptimizeVacuumSkippedMatchesBothHermesLines(t *testing.T) {
	inUse := "✓ Search index optimized.\n  Database size: 4600.0 MB → 4590.0 MB\n" +
		"  (VACUUM skipped: another process is using the database. Re-run `hermes sessions optimize` with it closed to reclaim freed space.)\n"
	failed := "✓ Search index optimized.\n  (VACUUM was skipped or failed — run `hermes sessions optimize` later to reclaim freed space.)\r\n"
	for name, out := range map[string]string{"in use": inUse, "failed": failed} {
		got := OptimizeVacuumSkipped(out)
		if !strings.HasPrefix(got, "(VACUUM ") || strings.ContainsAny(got, "\r\n") {
			t.Errorf("%s: got %q", name, got)
		}
	}
	for _, ok := range []string{"✓ Search index optimized.\n  Database size: 4600.0 MB → 2000.0 MB\n", "", "VACUUM is a statement"} {
		if got := OptimizeVacuumSkipped(ok); got != "" {
			t.Errorf("false match on %q: %q", ok, got)
		}
	}
}

// PROFILE_ID_RE of hermes_constants.py (v0.23.1).
func TestValidProfileIDIsHermesPROFILE_ID_RE(t *testing.T) {
	good := []string{"a", "0", "work", "a_b", "x1-2_3", strings.Repeat("a", 64)}
	bad := []string{"", "A", "a b", "a.b", "-x", "_x", "../x", "a/b", "é", strings.Repeat("a", 65), "a\n"}
	for _, n := range good {
		if !ValidProfileID(n) {
			t.Errorf("%q should be a profile id", n)
		}
	}
	for _, n := range bad {
		if ValidProfileID(n) {
			t.Errorf("%q should not be a profile id", n)
		}
	}
}
