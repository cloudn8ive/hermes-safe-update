package statedb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// makeJournalDB makes a database in the given journal mode with n rows,
// closed again (a WAL database is checkpointed on close).
func makeJournalDB(t *testing.T, path, mode string, n int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode("+mode+")")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE messages(id INTEGER PRIMARY KEY, body TEXT)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec("INSERT INTO messages(body) VALUES (?)", strings.Repeat("y", 1000)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Item 4: in a journal mode other than WAL, the copy's read lock would block
// the writers of a running Hermes for the whole copy.
func TestBackupSkipsANonWALDatabaseWhileAHolderRuns(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "state.db")
	makeJournalDB(t, src, "delete", 20)
	out := filepath.Join(dir, "out")

	r := req(DB{Profile: "default", Path: src}, out, "20261110-100000")
	r.HolderRuns = true
	res, err := Backup(context.Background(), r)
	if err != nil || res.Path != "" || !strings.Contains(res.Skipped, "journal mode is delete") {
		t.Fatalf("holder + delete mode: res = %+v err = %v", res, err)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Errorf("a skipped copy left %v", ents)
	}

	// no holder: nothing can be blocked, the copy is made
	r.HolderRuns = false
	if res, err := Backup(context.Background(), r); err != nil || res.Skipped != "" || countRows(t, res.Path) != 20 {
		t.Fatalf("no holder: res = %+v err = %v", res, err)
	}
}

func TestBackupOfAWALDatabaseGoesOnWhileAHolderRuns(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "state.db")
	makeJournalDB(t, src, "wal", 20)
	r := req(DB{Profile: "default", Path: src}, filepath.Join(dir, "out"), "20261110-100000")
	r.HolderRuns = true
	res, err := Backup(context.Background(), r)
	if err != nil || res.Skipped != "" || countRows(t, res.Path) != 20 {
		t.Fatalf("holder + wal: res = %+v err = %v", res, err)
	}
	if mode, err := JournalMode(context.Background(), src); err != nil || mode != "wal" {
		t.Errorf("JournalMode = %q, %v", mode, err)
	}
}

// Item 6/9: committed frames still in the -wal (the holder has not
// checkpointed) are part of the copy, and the -wal counts in the disk need.
func TestBackupCopiesFramesThatAreOnlyInTheWALAndCountsItsSize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "state.db")
	holder, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src)+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holder.SetMaxOpenConns(1)
	if _, err := holder.Exec("CREATE TABLE messages(id INTEGER PRIMARY KEY, body TEXT)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := holder.Exec("INSERT INTO messages(body) VALUES (?)", strings.Repeat("w", 500)); err != nil {
			t.Fatal(err)
		}
	}
	// the main file knows no rows; every committed one is in the -wal
	dbs := Find(dir) // only home/state.db is listed
	if len(dbs) != 1 || dbs[0].WAL == 0 {
		t.Fatalf("Find = %+v, want the -wal size", dbs)
	}
	d := dbs[0]
	if want := uint64(d.Size + d.WAL + WALAllowance); d.CopyNeed() != want {
		t.Errorf("CopyNeed = %d, want size+wal+allowance = %d", d.CopyNeed(), want)
	}
	r := req(d, filepath.Join(dir, "out"), "20261110-100000")
	r.HolderRuns = true
	res, err := Backup(context.Background(), r)
	if err != nil || res.Skipped != "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if n := countRows(t, res.Path); n != 40 {
		t.Errorf("the copy holds %d rows, want the 40 committed ones from the -wal", n)
	}

	// free space that covers the file but not the -wal plus the allowance is a skip;
	// exactly enough is not.
	need := d.CopyNeed()
	r.Stamp = "20261110-100001"
	r.Free = freeOf(need + r.MinFree - 1)
	if res, _ := Backup(context.Background(), r); res.Skipped == "" {
		t.Error("one byte short of size+wal+allowance+floor: the copy was made")
	}
	r.Free = freeOf(need + r.MinFree)
	if res, err := Backup(context.Background(), r); err != nil || res.Skipped != "" {
		t.Errorf("exactly size+wal+allowance+floor: res = %+v err = %v", res, err)
	}
}

// Item 9: cancel from another goroutine once the .partial exists.
func TestBackupCancelledMidCopyRemovesThePartialAndKeepsTheOldCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "state.db")
	makeJournalDB(t, src, "wal", 120000) // ~120 MB: the copy takes a moment
	out := filepath.Join(dir, "out")
	if res, err := Backup(context.Background(), req(DB{Profile: "p", Path: src}, out, "20261101-100000")); err != nil || res.Skipped != "" {
		t.Fatalf("first copy: %+v %v", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := make(chan bool, 1)
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if m, _ := filepath.Glob(filepath.Join(out, "*.partial")); len(m) > 0 {
				seen <- true
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
		seen <- false
	}()
	_, err := Backup(ctx, req(DB{Profile: "p", Path: src}, out, "20261110-100000"))
	if !<-seen {
		t.Fatal("the .partial never appeared")
	}
	if err == nil {
		t.Fatal("a copy cancelled while it ran reported success (the copy is too small for this test to catch it)")
	}
	ents, _ := os.ReadDir(out)
	if len(ents) != 1 || ents[0].Name() != "p-20261101-100000.db" {
		var n []string
		for _, e := range ents {
			n = append(n, e.Name())
		}
		t.Errorf("dir after the cancel = %v, want only the older good copy", n)
	}
}

// Item 12: a folder whose name is not a Hermes profile id is left out and listed.
func TestFindLeavesOutFoldersThatAreNotProfileIDs(t *testing.T) {
	home := t.TempDir()
	mk := func(name string) {
		p := filepath.Join(home, "profiles", name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "state.db"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"ok-1", "a b", "A", "a_b", "a.b", "-x"} {
		mk(n)
	}
	var got []string
	for _, d := range Find(home) {
		got = append(got, d.Profile)
	}
	if strings.Join(got, ",") != "a_b,ok-1" {
		t.Errorf("Find = %v, want only the valid ids a_b and ok-1 (the root has no file here)", got)
	}
	ign := Ignored(home)
	if len(ign) != 4 {
		t.Errorf("Ignored = %v, want the four invalid ones", ign)
	}
	// the rule is Hermes' (contracts.go)
	if hermes.ValidProfileID("a b") || !hermes.ValidProfileID("a_b") {
		t.Error("ValidProfileID")
	}
}

// Item 10: a legacy profiles/default folder is not the root.
func TestLegacyProfilesDefaultHasItsOwnLabelAndFile(t *testing.T) {
	home := t.TempDir()
	for _, p := range []string{filepath.Join(home, "state.db"), filepath.Join(home, "profiles", "default", "state.db")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dbs := Find(home)
	if len(dbs) != 2 {
		t.Fatalf("Find = %+v", dbs)
	}
	labels := map[string]bool{}
	for _, d := range dbs {
		labels[d.Profile] = true
	}
	if !labels[DefaultProfile] || !labels[LegacyDefaultProfile] {
		t.Errorf("labels = %v, want default and %s", labels, LegacyDefaultProfile)
	}
	if safeName(DefaultProfile) == safeName(LegacyDefaultProfile) {
		t.Error("both would write the same copy file name and the rotation would delete one with the other")
	}
}
