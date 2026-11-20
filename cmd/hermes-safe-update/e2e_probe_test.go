package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	_ "modernc.org/sqlite"
)

// Hermes v0.23.1 FTS_SQL / FTS_TRIGRAM_SQL (compact) and LEGACY_FTS_SQL /
// LEGACY_FTS_TRIGRAM_SQL (inline, every release before), with the tables
// around them (the busy check reads sessions); see internal/statedb/probe_test.go.
const probeBase = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT, source TEXT, started_at REAL, last_activity_at REAL, ended_at REAL);
CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, tool_name TEXT, tool_calls TEXT, timestamp REAL NOT NULL DEFAULT 0);
INSERT INTO sessions VALUES ('s', 'x', 'desktop', 1, 1, 1);
CREATE TABLE state_meta (key TEXT PRIMARY KEY, value TEXT);
INSERT INTO messages (id, session_id, role, content) VALUES (1, 's', 'user', 'hello');
`
const probeCompact = `
CREATE VIEW messages_fts_src AS SELECT id, content, tool_name, tool_calls FROM messages;
CREATE VIRTUAL TABLE messages_fts USING fts5(
    content,
    tool_name,
    tool_calls,
    content='messages_fts_src',
    content_rowid='id'
);
CREATE VIEW messages_fts_trigram_src AS SELECT id, content, tool_name FROM messages;
CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(
    content,
    tool_name,
    content='messages_fts_trigram_src',
    content_rowid='id',
    tokenize='trigram'
);
INSERT INTO messages_fts(rowid, content, tool_name, tool_calls) VALUES (1, 'hello', NULL, NULL);
`
const probeLegacy = `
CREATE VIRTUAL TABLE messages_fts USING fts5(
    content
);
CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(
    content,
    tokenize='trigram'
);
INSERT INTO messages_fts(rowid, content) VALUES (1, 'hello');
`

func writeProbeDB(t *testing.T, path, ddl string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path) // the world has small stand-ins
	for _, ext := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(path + ext)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(probeBase + ddl); err != nil {
		t.Fatal(err)
	}
}

// realProbe makes the fake service run the real probe on the files at its
// databases' paths (their sizes stay scripted).
func (s *e2eSD) realProbe() { s.real = true }

func probeWorld(t *testing.T, defaultDDL, benchDDL string) (*e2eWorld, *e2eSD) {
	t.Helper()
	w := newWorld(t)
	w.version = "v0.23.1"
	sd := newE2ESD(w.home)
	sd.realProbe()
	for _, d := range sd.dbs {
		ddl := defaultDDL
		if d.Profile == "work" {
			ddl = benchDDL
		}
		writeProbeDB(t, d.Path, ddl)
	}
	w.sd = sd
	return w, sd
}

func TestE2EProbeCompactDatabasesAskNothingAndKeepTheirCopy(t *testing.T) {
	w, sd := probeWorld(t, probeCompact, probeCompact)
	w.con.script(conStep{key: 'y'})
	w.vt = true
	w.cli()
	w.wantCode(apperr.OK)
	w.noScreen("Compact the session database")
	w.wantScreen("Hermes is updated and verified")
	if len(sd.copied) != 2 || w.ran(w.launcher, "sessions", "optimize-storage", "--yes") {
		t.Errorf("copied %v, optimize ran: %v", sd.copied, w.ran(w.launcher, "sessions", "optimize-storage", "--yes"))
	}
}

func TestE2EProbeLegacyDatabaseIsOfferedAndCompactedTheCompactOneIsNot(t *testing.T) {
	w, sd := probeWorld(t, probeLegacy, probeCompact) // default legacy, work compact
	w.optimize = map[string]execx.Result{"sessions optimize-storage --yes": {Output: "✓ Search index optimized."}}
	w.con.script(conStep{key: 'y'}, conStep{key: 'y', then: func(*e2eWorld) { sd.resize("default", 500<<20) }})
	w.vt = true
	w.cli()
	w.wantCode(apperr.OK)
	w.wantScreen("Compact the session database(s) before updating? default 1.2 GB", "Hermes is updated and verified")
	if !w.ran(w.launcher, "sessions", "optimize-storage", "--yes") || w.ran(w.launcher, "-p", "work", "sessions", "optimize-storage", "--yes") {
		t.Errorf("calls: %v", w.Calls())
	}
}

func TestE2EProbeCheckRowForCompactDatabases(t *testing.T) {
	w, sd := probeWorld(t, probeCompact, probeCompact)
	before := w.storeBytes()
	w.cli("--plain", "check")
	w.wantCode(apperr.OK)
	w.wantScreen("state.db", "work 4.5 GB, default 1.2 GB over Hermes' 1 GiB snapshot limit (search index already compact; Safe Update keeps its own copy)")
	w.noScreen("offers to compact")
	w.nothingChanged(before)
	if len(sd.copied) != 0 {
		t.Errorf("check copied %v", sd.copied)
	}
}
