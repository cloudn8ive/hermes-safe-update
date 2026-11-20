package statedb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// Verdict is the answer to "does `hermes sessions optimize-storage` have work
// on this database?".
type Verdict int

const (
	// Unknown: the probe could not tell (an error, a locked file, a schema it
	// does not know). The update flow then behaves as it did before the probe
	// existed and offers the compaction: a wasted question costs a minute, a
	// wrongly hidden one costs a database that stays over Hermes' limit.
	Unknown Verdict = iota
	// NoWork: Hermes would say "already on the compact layout".
	NoWork
	// Work: Hermes has an index layout, an interrupted run or a leftover to
	// deal with.
	Work
)

func (v Verdict) String() string {
	switch v {
	case NoWork:
		return "no work"
	case Work:
		return "work"
	}
	return "unknown"
}

// Compaction is the verdict with the reason that decided it.
type Compaction struct {
	Verdict Verdict
	Reason  string
}

// probeTimeout bounds the whole probe of one database: three small queries
// and an EXISTS each on two tables.
const probeTimeout = 20 * time.Second

// CompactionWork mirrors Hermes' fts_optimize_available() (v0.23.1,
// hermes_state_search.py:592-604) on a database file, read-only: the file is
// opened mode=ro with query_only on, nothing is ever written, and a locked or
// odd database is Unknown. The pieces and their sources are listed in
// hermes/contracts.go.
func CompactionWork(ctx context.Context, path string) Compaction {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	db, err := sql.Open("sqlite", dsn(path, "mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(1)"))
	if err != nil {
		return unknown("cannot open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return compactionWork(ctx, db)
}

func unknown(format string, a ...any) Compaction {
	return Compaction{Unknown, fmt.Sprintf(format, a...)}
}

func compactionWork(ctx context.Context, db *sql.DB) Compaction {
	// What the file holds: tables by name, and the stored definitions of the
	// two index tables.
	rows, err := db.QueryContext(ctx, "SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return unknown("cannot read the schema: %v", err)
	}
	tables := map[string]string{}
	var trash bool
	for rows.Next() {
		var typ, name, def string
		if err := rows.Scan(&typ, &name, &def); err != nil {
			rows.Close()
			return unknown("cannot read the schema: %v", err)
		}
		tables[name] = def
		// _has_fts_trash: name LIKE 'fts_v22_trash\_%' ESCAPE '\' (a
		// case-insensitive prefix match in SQLite).
		if len(name) >= len(hermes.FTSTrashPrefix) && strings.EqualFold(name[:len(hermes.FTSTrashPrefix)], hermes.FTSTrashPrefix) {
			trash = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return unknown("cannot read the schema: %v", err)
	}
	rows.Close()

	if _, ok := tables[hermes.MessagesTable]; !ok {
		return unknown("no %s table: not a Hermes session database", hermes.MessagesTable)
	}
	if _, ok := tables[hermes.StateMetaTable]; !ok {
		return unknown("no %s table: not a Hermes session database", hermes.StateMetaTable)
	}
	has := func(key string) (bool, error) {
		var one int
		err := db.QueryRowContext(ctx, "SELECT 1 FROM "+hermes.StateMetaTable+" WHERE key = ? LIMIT 1", key).Scan(&one)
		if err == sql.ErrNoRows {
			return false, nil
		}
		return err == nil, err
	}
	// No base index. Hermes' _demote_legacy_fts_to_trash commits the removal of
	// the index from sqlite_master, the fts_v22_trash_* renames and
	// fts_rebuild_high_water first and recreates the tables afterwards; a crash
	// in between leaves exactly this (no index, marker, trash), and the trash
	// survives the next open's rebuild, so Hermes has work. Without them the
	// index is simply off (or absent) and the next open recreates it.
	if _, ok := tables[hermes.FTSBaseTable]; !ok {
		hw, err := has(hermes.FTSRebuildHighWater)
		if err != nil {
			return unknown("cannot read %s: %v", hermes.StateMetaTable, err)
		}
		if hw || trash {
			return Compaction{Work, "an interrupted optimize left its marker or old index tables without the index"}
		}
		return Compaction{NoWork, "no full-text index"}
	}
	// The index was detached after corruption: Hermes rebuilds it on a later
	// open and its answer meanwhile is not the layout's.
	if stale, err := has(hermes.FTSStaleKey); err != nil {
		return unknown("cannot read %s: %v", hermes.StateMetaTable, err)
	} else if stale {
		return unknown("the full-text index is marked stale")
	}

	stored := map[string]string{}
	for _, t := range []string{hermes.FTSBaseTable, hermes.FTSTrigramTable} {
		if def, ok := tables[t]; ok {
			stored[t] = def
		}
	}
	drift, unrecognised := hermes.FTSLayoutDrift(stored)
	if len(drift) > 0 {
		return Compaction{Work, "older search-index layout: " + strings.Join(drift, ", ")}
	}
	if hw, err := has(hermes.FTSRebuildHighWater); err != nil {
		return unknown("cannot read %s: %v", hermes.StateMetaTable, err)
	} else if hw {
		return Compaction{Work, "an interrupted optimize left " + hermes.FTSRebuildHighWater}
	}
	if trash {
		return Compaction{Work, "old index tables (" + hermes.FTSTrashPrefix + "*) await their teardown"}
	}
	// _fts_external_index_empty_with_messages: rows in messages, none in the
	// index's docsize table. A missing table is "not this failure class" in
	// Hermes; any other error (a lock) is not an answer here.
	empty, err := externalIndexEmpty(ctx, db, tables)
	if err != nil {
		return unknown("cannot read the index: %v", err)
	}
	if empty {
		return Compaction{Work, "the search index is empty but messages has rows"}
	}
	// Hermes 0.23.0 also counts a CJK backfill on a host that can tokenize
	// it; whether this one can is not knowable from the file.
	for _, k := range []string{hermes.FTSCJKHighWater, hermes.FTSCJKStaleKey} {
		if cjk, err := has(k); err != nil {
			return unknown("cannot read %s: %v", hermes.StateMetaTable, err)
		} else if cjk {
			return unknown("%s is set (Hermes 0.23.0 may count it as work)", k)
		}
	}
	if len(unrecognised) > 0 {
		return unknown("the layout of %s is not one this Safe Update knows", strings.Join(unrecognised, ", "))
	}
	return Compaction{NoWork, "search index already on the compact layout"}
}

func externalIndexEmpty(ctx context.Context, db *sql.DB, tables map[string]string) (bool, error) {
	var any int
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+hermes.MessagesTable+")").Scan(&any); err != nil {
		return false, err
	}
	if any == 0 {
		return false, nil
	}
	if _, ok := tables[hermes.FTSDocsizeTable]; !ok {
		return false, nil
	}
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+hermes.FTSDocsizeTable+")").Scan(&any); err != nil {
		return false, err
	}
	return any == 0, nil
}
