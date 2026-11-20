package statedb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The DDL below is Hermes' own, copied from the tags named at each constant
// (hermes_state_common.py FTS_SQL / FTS_TRIGRAM_SQL / LEGACY_*, and the two
// registry entries of hermes_state_fts.py). The views are shortened (their
// SELECT is not looked at by the probe); the CREATE VIRTUAL TABLE text keeps
// Hermes' own spacing, because the probe compares what SQLite stored.

const ddlBase = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT);
CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, tool_name TEXT, tool_calls TEXT);
CREATE TABLE state_meta (key TEXT PRIMARY KEY, value TEXT);
`

// v0.23.1 FTS_SQL / FTS_TRIGRAM_SQL: the compact layout.
const ddlCompact = `
CREATE VIEW IF NOT EXISTS messages_fts_src AS SELECT id, content, tool_name, tool_calls FROM messages;
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    tool_name,
    tool_calls,
    content='messages_fts_src',
    content_rowid='id'
);
CREATE VIEW IF NOT EXISTS messages_fts_trigram_src AS SELECT m.id, m.role, m.content, m.tool_name FROM messages AS m WHERE m.role <> 'tool';
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts_trigram USING fts5(
    content,
    tool_name,
    content='messages_fts_trigram_src',
    content_rowid='id',
    tokenize='trigram'
);
`

// v0.23.1 LEGACY_FTS_SQL / LEGACY_FTS_TRIGRAM_SQL (also v0.21.6): the inline
// layout of every release before the compact one.
const ddlLegacy = `
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts_trigram USING fts5(
    content,
    tokenize='trigram'
);
`

// FTS_VTABLE_LAYOUTS["messages_fts_trigram"][2] (v0.22.7, v0.23.1): the
// compact base index with a trigram index that still carries tool_calls.
const ddlTrigramToolCalls = `
CREATE VIEW IF NOT EXISTS messages_fts_src AS SELECT id, content, tool_name, tool_calls FROM messages;
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    tool_name,
    tool_calls,
    content='messages_fts_src',
    content_rowid='id'
);
CREATE VIEW IF NOT EXISTS messages_fts_trigram_src AS SELECT id, content, tool_name, tool_calls FROM messages;
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts_trigram USING fts5(
    content,
    tool_name,
    tool_calls,
    content='messages_fts_trigram_src',
    content_rowid='id',
    tokenize='trigram'
);
`

func mkDB(t *testing.T, ddl string, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, s := range append([]string{ddlBase, ddl}, stmts...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return path
}

const (
	oneMessage = `INSERT INTO messages (id, session_id, role, content) VALUES (1, 's', 'user', 'hello world')`
	// an indexed row: the compact index gets its docsize row
	indexRow = `INSERT INTO messages_fts(rowid, content, tool_name, tool_calls) VALUES (1, 'hello world', NULL, NULL)`
)

func probe(t *testing.T, path string) Compaction {
	t.Helper()
	return CompactionWork(context.Background(), path)
}

func want(t *testing.T, got Compaction, v Verdict, reasonHas string) {
	t.Helper()
	if got.Verdict != v || !strings.Contains(got.Reason, reasonHas) {
		t.Errorf("got %v %q, want %v containing %q", got.Verdict, got.Reason, v, reasonHas)
	}
}

func TestProbeCompactLayoutIsNoWork(t *testing.T) {
	want(t, probe(t, mkDB(t, ddlCompact, oneMessage, indexRow)), NoWork, "compact layout")
}

func TestProbeCompactLayoutWithoutMessagesIsNoWork(t *testing.T) {
	want(t, probe(t, mkDB(t, ddlCompact)), NoWork, "compact layout")
}

func TestProbeLegacyInlineLayoutIsWork(t *testing.T) {
	path := mkDB(t, ddlLegacy, oneMessage, `INSERT INTO messages_fts(rowid, content) VALUES (1, 'hello world')`)
	want(t, probe(t, path), Work, "older search-index layout: messages_fts, messages_fts_trigram")
}

func TestProbeLegacyTrigramOnlyIsWork(t *testing.T) {
	// compact base index, inline trigram index (fts5(content,tokenize='trigram'))
	path := mkDB(t, strings.Split(ddlCompact, "CREATE VIEW IF NOT EXISTS messages_fts_trigram_src")[0]+
		"CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,tokenize='trigram');", oneMessage, indexRow)
	want(t, probe(t, path), Work, "older search-index layout: messages_fts_trigram")
}

func TestProbeV23TrigramStillCarryingToolCallsIsWork(t *testing.T) {
	path := mkDB(t, ddlTrigramToolCalls, oneMessage, indexRow)
	want(t, probe(t, path), Work, "older search-index layout: messages_fts_trigram")
}

func TestProbeInterruptedOptimizeMarkerIsWork(t *testing.T) {
	path := mkDB(t, ddlCompact, oneMessage, indexRow, `INSERT INTO state_meta VALUES ('fts_rebuild_high_water', '12')`)
	want(t, probe(t, path), Work, "interrupted optimize")
}

func TestProbeTrashTablesAreWork(t *testing.T) {
	path := mkDB(t, ddlCompact, oneMessage, indexRow, `CREATE TABLE fts_v22_trash_messages_fts_data (id INTEGER PRIMARY KEY, block BLOB)`)
	want(t, probe(t, path), Work, "await their teardown")
}

func TestProbeTrashLikeNameOnlyMatchesThePrefix(t *testing.T) {
	// "_" is escaped in Hermes' LIKE: "fts-v22-trash-x" and a mid-name hit are not trash
	for _, name := range []string{"fts-v22-trash-x", "my_fts_v22_trash_x", "fts_v22_trash"} {
		path := mkDB(t, ddlCompact, oneMessage, indexRow, `CREATE TABLE "`+name+`" (a)`)
		want(t, probe(t, path), NoWork, "compact layout")
	}
}

func TestProbeEmptyExternalIndexWithMessagesIsWork(t *testing.T) {
	want(t, probe(t, mkDB(t, ddlCompact, oneMessage)), Work, "empty")
}

func TestProbeNoFullTextIndexIsNoWork(t *testing.T) {
	want(t, probe(t, mkDB(t, "", oneMessage)), NoWork, "no full-text index")
}

func TestProbeInterruptedDemoteIsWorkWithoutTheIndex(t *testing.T) {
	// _demote_legacy_fts_to_trash commits the index's removal, the trash
	// renames and the marker, and recreates the tables afterwards; a crash in
	// between leaves no messages_fts. Hermes still has work (the trash).
	drop := `DROP TABLE messages_fts`
	trash := `CREATE TABLE fts_v22_trash_messages_fts_data (id INTEGER PRIMARY KEY, block BLOB)`
	marker := `INSERT INTO state_meta VALUES ('fts_rebuild_high_water', '12')`
	for name, c := range map[string]struct {
		stmts []string
		v     Verdict
	}{
		"trash and marker": {[]string{trash, marker}, Work},
		"trash only":       {[]string{trash}, Work},
		"marker only":      {[]string{marker}, Work},
		"neither":          {nil, NoWork},
	} {
		t.Run(name, func(t *testing.T) {
			path := mkDB(t, ddlCompact, append([]string{oneMessage, indexRow, drop}, c.stmts...)...)
			if got := probe(t, path); got.Verdict != c.v {
				t.Errorf("got %v %q, want %v", got.Verdict, got.Reason, c.v)
			}
		})
	}
}

func TestProbeStaleIndexIsUnknown(t *testing.T) {
	path := mkDB(t, ddlCompact, oneMessage, indexRow, `INSERT INTO state_meta VALUES ('fts_stale', '1')`)
	want(t, probe(t, path), Unknown, "stale")
}

func TestProbeBaseIndexReadingMessagesIsRealignedNotWork(t *testing.T) {
	// Hermes: "the next read-write open rebuilds it, so it is never opt-in
	// work" (base_index_realigned_on_open). Registry entry 3 of messages_fts.
	path := mkDB(t, `CREATE VIRTUAL TABLE messages_fts USING fts5(content,tool_name,tool_calls,content='messages',content_rowid='id');`, oneMessage,
		`INSERT INTO messages_fts(rowid, content, tool_name, tool_calls) VALUES (1, 'hello world', NULL, NULL)`)
	want(t, probe(t, path), NoWork, "compact layout")
}

func TestProbeUnrecognisedLayoutIsUnknown(t *testing.T) {
	// a definition a newer Hermes might create: neither in the registry nor
	// shaped like an old one
	path := mkDB(t, `CREATE VIEW messages_fts_src AS SELECT id, content, tool_name, tool_calls FROM messages;
CREATE VIRTUAL TABLE messages_fts USING fts5(content,tool_name,tool_calls,role,content='messages_fts_src',content_rowid='id');`, oneMessage,
		`INSERT INTO messages_fts(rowid, content, tool_name, tool_calls, role) VALUES (1, 'hello world', NULL, NULL, 'user')`)
	want(t, probe(t, path), Unknown, "not one this Safe Update knows")
}

func TestProbeCJKMarkersAreUnknown(t *testing.T) {
	for _, k := range []string{"fts_cjk_rebuild_high_water", "fts_cjk_stale"} {
		path := mkDB(t, ddlCompact, oneMessage, indexRow, `INSERT INTO state_meta VALUES ('`+k+`', '1')`)
		want(t, probe(t, path), Unknown, k)
	}
}

func TestProbeWorkBeatsUnknownMarkers(t *testing.T) {
	// a legacy layout is work whatever else is set
	path := mkDB(t, ddlLegacy, oneMessage, `INSERT INTO state_meta VALUES ('fts_cjk_stale', '1')`)
	want(t, probe(t, path), Work, "older search-index layout")
}

func TestProbeNotAHermesDatabaseIsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, _ := sql.Open("sqlite", path)
	if _, err := db.Exec(`CREATE TABLE foo (a)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	want(t, probe(t, path), Unknown, "not a Hermes session database")

	// messages but no state_meta
	path = mkDB(t, "")
	db, _ = sql.Open("sqlite", path)
	if _, err := db.Exec(`DROP TABLE state_meta`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	want(t, probe(t, path), Unknown, "state_meta")
}

func TestProbeGarbageAndMissingFilesAreUnknownAndNothingIsCreated(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "state.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("not sqlite ", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := probe(t, garbage); got.Verdict != Unknown {
		t.Errorf("garbage: %v %q", got.Verdict, got.Reason)
	}
	missing := filepath.Join(dir, "none.db")
	if got := probe(t, missing); got.Verdict != Unknown {
		t.Errorf("missing: %v %q", got.Verdict, got.Reason)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("the probe created files: %v", ents)
	}
}

func TestProbeLockedDatabaseIsUnknown(t *testing.T) {
	path := mkDB(t, ddlCompact, oneMessage, indexRow)
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	conn, err := w.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// rollback-journal mode: an exclusive transaction blocks readers
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	start := time.Now()
	got := probe(t, path)
	if got.Verdict != Unknown {
		t.Errorf("locked: %v %q", got.Verdict, got.Reason)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the probe waited %v on a lock", d)
	}
}

func TestProbeLockedWALDatabaseIsUnknown(t *testing.T) {
	// A WAL database only blocks readers while a connection holds it in
	// exclusive locking mode (no shared wal-index): that is the locked case.
	path, w := mkWAL(t, ddlCompact, oneMessage, indexRow)
	conn, err := w.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, s := range []string{"PRAGMA locking_mode=EXCLUSIVE", "BEGIN EXCLUSIVE"} {
		if _, err := conn.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	start := time.Now()
	got := probe(t, path)
	if got.Verdict != Unknown {
		t.Errorf("locked WAL: %v %q", got.Verdict, got.Reason)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the probe waited %v on a lock", d)
	}
}

func sum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// mkWAL builds a WAL-mode database and keeps a writer connection open (closed
// at test end) with autocheckpoint off, so the -wal holds frames the main file
// does not, as with a running gateway.
func mkWAL(t *testing.T, stmts ...string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	w.SetMaxOpenConns(1)
	for _, s := range append([]string{"PRAGMA journal_mode=wal", "PRAGMA wal_autocheckpoint=0", ddlBase}, stmts...) {
		if _, err := w.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("no frames in the -wal: %v", err)
	}
	return path, w
}

func TestProbeNeverWritesWAL(t *testing.T) {
	for name, ddl := range map[string]string{"compact": ddlCompact, "legacy": ddlLegacy} {
		t.Run(name, func(t *testing.T) {
			path, _ := mkWAL(t, ddl, oneMessage)
			dbBefore, walBefore := sum(t, path), sum(t, path+"-wal")
			st1, _ := os.Stat(path + "-wal")
			for i := 0; i < 2; i++ {
				probe(t, path)
			}
			// -shm is a SQLite wal-index, not data: not asserted
			if sum(t, path) != dbBefore {
				t.Error("the database file changed")
			}
			st2, err := os.Stat(path + "-wal")
			if err != nil {
				t.Fatalf("the -wal is gone: %v", err)
			}
			if st1.Size() != st2.Size() || sum(t, path+"-wal") != walBefore {
				t.Errorf("the -wal changed: %d -> %d bytes", st1.Size(), st2.Size())
			}
		})
	}
}

func TestProbeNeverWrites(t *testing.T) {
	for _, mode := range []string{"delete"} {
		for name, ddl := range map[string]string{"compact": ddlCompact, "legacy": ddlLegacy} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				path := mkDB(t, ddl, oneMessage, "PRAGMA journal_mode="+mode)
				before := sum(t, path)
				st1, _ := os.Stat(path)
				for i := 0; i < 2; i++ {
					probe(t, path)
				}
				if sum(t, path) != before {
					t.Error("the database file changed")
				}
				st2, _ := os.Stat(path)
				if !st2.ModTime().Equal(st1.ModTime()) || st1.Size() != st2.Size() {
					t.Errorf("mtime/size changed: %v %d -> %v %d", st1.ModTime(), st1.Size(), st2.ModTime(), st2.Size())
				}
				if mode == "delete" {
					if _, err := os.Stat(path + "-wal"); err == nil {
						t.Error("a -wal file appeared")
					}
					if _, err := os.Stat(path + "-journal"); err == nil {
						t.Error("a -journal file appeared")
					}
				}
			})
		}
	}
}

func TestProbeSeesWalFramesNotYetCheckpointed(t *testing.T) {
	// A WAL database with a holder (a writer connection kept open) and the
	// interrupted-optimize marker only in the -wal: the probe must read it.
	path := filepath.Join(t.TempDir(), "state.db")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	for _, s := range []string{"PRAGMA journal_mode=wal", "PRAGMA wal_autocheckpoint=0", ddlBase, ddlCompact, oneMessage, indexRow,
		`INSERT INTO state_meta VALUES ('fts_rebuild_high_water', '1')`} {
		if _, err := w.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("no frames in the -wal: %v", err)
	}
	want(t, probe(t, path), Work, "interrupted optimize")
}

func TestProbeNeedsBothHermesTables(t *testing.T) {
	// each table alone is missing in turn: "not a Hermes database" names it
	for _, drop := range []string{"messages", "state_meta"} {
		path := mkDB(t, ddlCompact)
		db, _ := sql.Open("sqlite", path)
		// the view reads messages: drop it first when messages goes
		if _, err := db.Exec(`DROP VIEW messages_fts_src; DROP VIEW messages_fts_trigram_src; DROP TABLE ` + drop); err != nil {
			t.Fatal(err)
		}
		db.Close()
		want(t, probe(t, path), Unknown, "no "+drop+" table")
	}
}

func TestProbeUnreadableDocsizeIsUnknownNotNoWork(t *testing.T) {
	// the index's docsize table cannot be read (here: rewritten to a layout its
	// pages do not have): Hermes' except-clause covers only a missing table, so
	// this is not an answer
	path := mkDB(t, ddlCompact, oneMessage, indexRow)
	db, _ := sql.Open("sqlite", path)
	db.SetMaxOpenConns(1)
	for _, s := range []string{"PRAGMA writable_schema=ON",
		`UPDATE sqlite_master SET sql='CREATE TABLE ''messages_fts_docsize''(id TEXT PRIMARY KEY, sz BLOB) WITHOUT ROWID' WHERE name='messages_fts_docsize'`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	got := probe(t, path)
	if got.Verdict != Unknown || !strings.Contains(got.Reason, "cannot read the index") {
		t.Errorf("got %v %q", got.Verdict, got.Reason)
	}
}
