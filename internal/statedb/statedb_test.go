package statedb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeDB(t *testing.T, path string, rows int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE messages(id INTEGER PRIMARY KEY, body TEXT)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec("INSERT INTO messages(body) VALUES (?)", strings.Repeat("x", 200)); err != nil {
			t.Fatal(err)
		}
	}
}

func countRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func freeOf(n uint64) func(string) (uint64, error) {
	return func(string) (uint64, error) { return n, nil }
}

func req(src DB, dir, stamp string) BackupReq {
	return BackupReq{Src: src, Dir: dir, Stamp: stamp, Free: freeOf(100e9), MinFree: 10e9}
}

func TestFindAndOver(t *testing.T) {
	home := t.TempDir()
	write := func(p string, n int) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "state.db"), 300)
	write(filepath.Join(home, "profiles", "a", "state.db"), 100)
	write(filepath.Join(home, "profiles", "b", "state.db"), 900)
	if err := os.MkdirAll(filepath.Join(home, "profiles", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(home, "profiles", "c", "other.db"), 5)
	dbs := Find(home)
	var names []string
	for _, d := range dbs {
		names = append(names, d.Profile)
	}
	if strings.Join(names, ",") != "a,default,b" {
		t.Errorf("found %v (smallest first)", names)
	}
	over := Over(dbs, 300)
	if len(over) != 1 || over[0].Profile != "b" {
		t.Errorf("over 300 = %+v (the limit itself is not over)", over)
	}
	if got := Find(filepath.Join(home, "nothing")); len(got) != 0 {
		t.Errorf("missing home: %v", got)
	}
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 2048: "2.0 KB", 5 << 20: "5.0 MB", 4*(1<<30) + (1 << 29): "4.5 GB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestBackupMakesVerifiedCopyAndLeavesSourceAlone(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "state.db")
	makeDB(t, src, 200)
	before := sha(t, src)
	out := filepath.Join(dir, "backups", "safe-update-statedb")
	res, err := Backup(context.Background(), req(DB{Profile: "default", Path: src, Size: 1}, out, "20261110-100000"))
	if err != nil || res.Skipped != "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if filepath.Base(res.Path) != "default-20261110-100000.db" || countRows(t, res.Path) != 200 {
		t.Errorf("copy = %+v", res)
	}
	if sha(t, src) != before {
		t.Error("the source changed")
	}
	if ents, _ := os.ReadDir(out); len(ents) != 1 {
		t.Errorf("dir = %v (no partial left)", ents)
	}
}

func TestBackupWorksWhileAnotherConnectionHasTheDatabaseOpenForWriting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "state.db")
	makeDB(t, src, 50)
	holder, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holder.SetMaxOpenConns(1)
	tx, err := holder.Begin() // an open write transaction: the gateway in the middle of a turn
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO messages(body) VALUES ('uncommitted')"); err != nil {
		t.Fatal(err)
	}
	res, err := Backup(context.Background(), req(DB{Profile: "p", Path: src}, filepath.Join(dir, "b"), "20261110-100000"))
	if err != nil || res.Skipped != "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if n := countRows(t, res.Path); n != 50 {
		t.Errorf("copy has %d rows, want the 50 committed ones", n)
	}
	if err := tx.Commit(); err != nil {
		t.Errorf("the holder could not commit after the copy: %v", err)
	}
}

func TestBackupKeepsNewestOnePerProfile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	makeDB(t, a, 5)
	makeDB(t, b, 6)
	out := filepath.Join(dir, "out")
	for _, st := range []string{"20261101-100000", "20261105-100000"} {
		if _, err := Backup(context.Background(), req(DB{Profile: "p", Path: a}, out, st)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Backup(context.Background(), req(DB{Profile: "q", Path: b}, out, "20261101-100000")); err != nil {
		t.Fatal(err)
	}
	// a stranger file and a profile whose name starts like p must survive
	for _, f := range []string{"notes.txt", "p2-20261101-100000.db"} {
		if err := os.WriteFile(filepath.Join(out, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Backup(context.Background(), req(DB{Profile: "p", Path: a}, out, "20261110-100000"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Replaced) != 1 || res.Replaced[0] != "p-20261105-100000.db" {
		t.Errorf("replaced = %v", res.Replaced)
	}
	ents, _ := os.ReadDir(out)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	got := strings.Join(names, ",")
	if got != "notes.txt,p-20261110-100000.db,p2-20261101-100000.db,q-20261101-100000.db" {
		t.Errorf("dir = %s", got)
	}
}

func TestBackupSkipsWhenItWouldLeaveLessThanTheFloor(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.db")
	makeDB(t, src, 5)
	out := filepath.Join(dir, "out")
	// an older good copy must survive a skipped run
	if _, err := Backup(context.Background(), req(DB{Profile: "p", Path: src}, out, "20261101-100000")); err != nil {
		t.Fatal(err)
	}
	for name, free := range map[string]uint64{"floor": 12e9, "less than the copy": 3e9} {
		r := req(DB{Profile: "p", Path: src, Size: 5e9}, out, "20261110-100000")
		r.Free = freeOf(free)
		res, err := Backup(context.Background(), r)
		if err != nil || res.Skipped == "" || res.Path != "" {
			t.Errorf("%s: res = %+v err = %v", name, res, err)
		}
		if !strings.Contains(res.Skipped, "must stay free") {
			t.Errorf("%s: reason = %q", name, res.Skipped)
		}
	}
	if ents, _ := os.ReadDir(out); len(ents) != 1 {
		t.Errorf("dir = %v", ents)
	}
	// an unreadable free-space figure is a skip, not a copy
	r := req(DB{Profile: "p", Path: src, Size: 5}, out, "20261110-100000")
	r.Free = func(string) (uint64, error) { return 0, errors.New("no") }
	if res, err := Backup(context.Background(), r); err != nil || res.Skipped == "" {
		t.Errorf("free error: %+v %v", res, err)
	}
}

func TestBackupQuickCheckFailureDeletesTheCopyAndKeepsTheOldOne(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.db")
	makeDB(t, src, 5)
	out := filepath.Join(dir, "out")
	if _, err := Backup(context.Background(), req(DB{Profile: "p", Path: src}, out, "20261101-100000")); err != nil {
		t.Fatal(err)
	}
	r := req(DB{Profile: "p", Path: src}, out, "20261110-100000")
	r.Check = func(context.Context, string) error { return errors.New("quick_check: row 3 missing") }
	res, err := Backup(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "quick_check") || res.Path != "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	ents, _ := os.ReadDir(out)
	if len(ents) != 1 || ents[0].Name() != "p-20261101-100000.db" {
		t.Errorf("dir = %v: the failed copy must be gone and the old one kept", ents)
	}
}

func TestBackupOfAMissingOrBrokenSourceFails(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if _, err := Backup(context.Background(), req(DB{Profile: "p", Path: filepath.Join(dir, "none.db")}, out, "20261110-100000")); err == nil {
		t.Error("a missing source made a copy")
	}
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not a database ", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), req(DB{Profile: "p", Path: junk}, out, "20261110-100000")); err == nil {
		t.Error("a broken source made a copy")
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Errorf("leftovers: %v", ents)
	}
}

func TestBackupCancelledLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.db")
	makeDB(t, src, 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(dir, "out")
	if _, err := Backup(ctx, req(DB{Profile: "p", Path: src}, out, "20261110-100000")); err == nil {
		t.Error("cancelled copy succeeded")
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Errorf("leftovers: %v", ents)
	}
}

func TestQuickCheckCatchesDamage(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "g.db")
	makeDB(t, good, 300)
	if err := QuickCheck(context.Background(), good); err != nil {
		t.Errorf("good db: %v", err)
	}
	// a merged (VACUUM INTO) copy, then overwrite part of a page
	c := filepath.Join(dir, "c.db")
	if err := vacuumInto(context.Background(), good, c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096; i < 4096+200 && i < len(data); i++ {
		data[i] = 0xFF
	}
	bad := filepath.Join(dir, "b.db")
	if err := os.WriteFile(bad, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := QuickCheck(context.Background(), bad); err == nil {
		t.Error("damaged copy passed")
	}
}

func TestSafeNameKeepsProfileNamesInsideTheFolder(t *testing.T) {
	for in, want := range map[string]string{"work": "work", "../evil": "...evil", "profiles/default": "profiles.default", "a b": "a_b", "": "profile"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func sha(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// the -wal file is part of the state
	if w, err := os.ReadFile(p + "-wal"); err == nil {
		b = append(b, w...)
	}
	return sha256.Sum256(b)
}

// An index that disagrees with its table makes quick_check answer with rows
// that are not "ok" (not with a query error): that path must fail the copy.
func TestQuickCheckReportsDamageToAnIndex(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE TABLE m(id INTEGER PRIMARY KEY, body TEXT)", "CREATE INDEX m_body ON m(body)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 400; i++ {
		if _, err := db.Exec("INSERT INTO m(body) VALUES (?)", strings.Repeat(string(rune('a'+i%26)), 40)+string(rune('a'+i%7))); err != nil {
			t.Fatal(err)
		}
	}
	var root int
	if err := db.QueryRow("SELECT rootpage FROM sqlite_master WHERE name='m_body'").Scan(&root); err != nil {
		t.Fatal(err)
	}
	db.Close()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	const ps = 4096
	off := (root - 1) * ps
	// blank the cell content area of the index root page (keep its header)
	for i := off + ps - 600; i < off+ps; i++ {
		data[i] = 0
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	err = QuickCheck(context.Background(), src)
	if err == nil {
		t.Fatal("a damaged index passed quick_check")
	}
	if !strings.HasPrefix(err.Error(), "quick_check:") {
		t.Errorf("error = %.80q: want the quick_check rows, not a query error", err.Error())
	}
}
