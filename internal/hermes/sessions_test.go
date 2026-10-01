package hermes

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type sessRow struct {
	id, title, source string
	last              float64
	ended             *float64
}

func f64(v float64) *float64 { return &v }

const sessionsDDL = `create table sessions (id text primary key, title text, last_activity_at real, ended_at real, source text)`

// makeStateDB writes a synthetic state.db and returns an open writer
// connection (closed at test end) so WAL tests can keep it open.
func makeStateDB(t *testing.T, path string, ddl string, rows []sessRow, wal bool) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if wal {
		if _, err := db.Exec(`pragma journal_mode=wal`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var ended any
		if r.ended != nil {
			ended = *r.ended
		}
		var src any
		if r.source != "" {
			src = r.source
		}
		if _, err := db.Exec(`insert into sessions values (?,?,?,?,?)`, r.id, r.title, r.last, ended, src); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func newSess(home string) *SQLiteSessions {
	return &SQLiteSessions{Home: home, Now: func() time.Time { return testNow }}
}

func nowEpoch() float64 { return float64(testNow.Unix()) }

func TestBusyIdleBusyCronEnded(t *testing.T) {
	home := t.TempDir()
	long := strings.Repeat("x", 80)
	makeStateDB(t, filepath.Join(home, "state.db"), sessionsDDL, []sessRow{
		{id: "s-busy", title: long, last: nowEpoch() - 30},
		{id: "s-idle", title: "old", last: nowEpoch() - 600},
		{id: "s-cron", title: "job", last: nowEpoch() - 5, source: "cron"},
		{id: "s-ended", title: "done", last: nowEpoch() - 5, ended: f64(nowEpoch() - 1)},
		{id: "s-cli", title: "", last: nowEpoch() - 100, source: "cli"},
	}, false)
	got, err := newSess(home).Busy(context.Background(), 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions: %+v", len(got), got)
	}
	byID := map[string]Session{}
	for _, s := range got {
		byID[s.ID] = s
	}
	b := byID["s-busy"]
	if b.Profile != "hermes" || b.Idle != 30*time.Second || len([]rune(b.Title)) != 50 {
		t.Errorf("busy = %+v", b)
	}
	if byID["s-cli"].Idle != 100*time.Second {
		t.Errorf("cli = %+v", byID["s-cli"])
	}
}

func TestBusyWindowBoundaryIsExclusive(t *testing.T) {
	home := t.TempDir()
	makeStateDB(t, filepath.Join(home, "state.db"), sessionsDDL, []sessRow{
		{id: "edge", last: nowEpoch() - 180},
		{id: "inside", last: nowEpoch() - 179},
	}, false)
	got, _ := newSess(home).Busy(context.Background(), 180*time.Second)
	if len(got) != 1 || got[0].ID != "inside" {
		t.Fatalf("got %+v", got)
	}
}

func TestBusyAllProfiles(t *testing.T) {
	home := t.TempDir()
	makeStateDB(t, filepath.Join(home, "state.db"), sessionsDDL, nil, false)
	makeStateDB(t, filepath.Join(home, "profiles", "worker", "state.db"), sessionsDDL,
		[]sessRow{{id: "w1", title: "t", last: nowEpoch() - 10}}, false)
	// A profile directory without state.db is skipped.
	if err := os.MkdirAll(filepath.Join(home, "profiles", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := newSess(home).Busy(context.Background(), 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Profile != "worker" || got[0].ID != "w1" {
		t.Fatalf("got %+v", got)
	}
}

func TestBusyNoDatabasesIsIdle(t *testing.T) {
	got, err := newSess(t.TempDir()).Busy(context.Background(), 180*time.Second)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestBusyMissingColumnCountsAsBusy(t *testing.T) {
	home := t.TempDir()
	makeStateDB(t, filepath.Join(home, "profiles", "p1", "state.db"),
		`create table sessions (id text, title text, last_activity_at real, ended_at real)`, nil, false)
	got, err := newSess(home).Busy(context.Background(), 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "p1: state.db unreadable" || got[0].Title == "" || len([]rune(got[0].Title)) > 60 || got[0].Idle != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestBusyCorruptFileCountsAsBusy(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "state.db"), []byte(strings.Repeat("not a database ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := newSess(home).Busy(context.Background(), 180*time.Second)
	if len(got) != 1 || got[0].ID != "hermes: state.db unreadable" {
		t.Fatalf("got %+v", got)
	}
}

func TestBusyReadsWALWhileWriterOpenWithoutWriting(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "state.db")
	w := makeStateDB(t, path, sessionsDDL, []sessRow{{id: "live", title: "in wal", last: nowEpoch() - 5}}, true)
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Skipf("no -wal file while the writer is open: %v", err)
	}
	before, _ := os.ReadFile(path)
	got, err := newSess(home).Busy(context.Background(), 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "live" {
		t.Fatalf("WAL content not seen: %+v", got)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("main database file changed by a read-only check")
	}
	// The writer can still write: we did not hold a lock.
	if _, err := w.Exec(`insert into sessions values ('more','',?,null,null)`, nowEpoch()); err != nil {
		t.Errorf("writer blocked after check: %v", err)
	}
}

func TestBusyNeverWrites(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "state.db")
	makeStateDB(t, path, sessionsDDL, nil, false)
	s := newSess(home)
	db, err := s.open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`insert into sessions values ('x','',1,null,null)`); err == nil {
		t.Fatal("insert succeeded on a read-only connection")
	}
}

func TestBusyOddPathCharacters(t *testing.T) {
	home := filepath.Join(t.TempDir(), "a b#c%d&e")
	makeStateDB(t, filepath.Join(home, "state.db"), sessionsDDL,
		[]sessRow{{id: "odd", last: nowEpoch() - 1}}, false)
	got, _ := newSess(home).Busy(context.Background(), 180*time.Second)
	if len(got) != 1 || got[0].ID != "odd" {
		t.Fatalf("got %+v", got)
	}
}

func TestBusyContextCancelled(t *testing.T) {
	home := t.TempDir()
	makeStateDB(t, filepath.Join(home, "state.db"), sessionsDDL, nil, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newSess(home).Busy(ctx, time.Minute); err == nil {
		t.Fatal("want error on cancelled context")
	}
}
