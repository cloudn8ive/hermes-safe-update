package hermes

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The busy check takes the freshest of a session's heartbeat and its newest
// message (Hermes: `_sql_session_last_active`), ignoring cells outside the
// trusted epoch window, and falls back to started_at only when neither is
// trusted. All databases below are synthetic.

const garbage = 5e+246 // a double salvaged from a damaged page

type freshSess struct {
	id      string
	source  string
	last    any // nil = NULL heartbeat
	started any
	ended   any
	msgs    []any // message timestamps
}

func buildFresh(t *testing.T, ddl string, rows ...freshSess) string {
	t.Helper()
	home := t.TempDir()
	db := makeStateDB(t, filepath.Join(home, "state.db"), ddl, nil, false)
	for _, r := range rows {
		var src any
		if r.source != "" {
			src = r.source
		}
		if _, err := db.Exec(`insert into sessions (id, title, last_activity_at, ended_at, source, started_at) values (?,?,?,?,?,?)`,
			r.id, "t-"+r.id, r.last, r.ended, src, r.started); err != nil {
			t.Fatal(err)
		}
		for _, ts := range r.msgs {
			if _, err := db.Exec(`insert into messages (session_id, role, timestamp) values (?, 'user', ?)`, r.id, ts); err != nil {
				t.Fatal(err)
			}
		}
	}
	return home
}

func busyIDs(t *testing.T, home string) map[string]time.Duration {
	t.Helper()
	got, err := newSess(home).Busy(context.Background(), 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]time.Duration{}
	for _, s := range got {
		m[s.ID] = s.Idle
	}
	return m
}

func TestBusyUsesTheFreshestOfHeartbeatAndMessages(t *testing.T) {
	now := nowEpoch()
	home := buildFresh(t, sessionsDDL,
		// the heartbeat has not beaten yet but a turn is running
		freshSess{id: "null-beat-fresh-msg", last: nil, started: now - 3600, msgs: []any{now - 2000, now - 20}},
		// the heartbeat lags the newest message
		freshSess{id: "stale-beat-fresh-msg", last: now - 900, started: now - 3600, msgs: []any{now - 45}},
		// the heartbeat alone (as before)
		freshSess{id: "fresh-beat-no-msg", last: now - 10, started: now - 3600},
		// nothing recent anywhere
		freshSess{id: "idle", last: now - 900, started: now - 3600, msgs: []any{now - 800, now - 700}},
		freshSess{id: "idle-null-beat", last: nil, started: now - 3600, msgs: []any{now - 700}},
		freshSess{id: "no-signals-at-all"},
		// a message of ANOTHER session must not wake this one
		freshSess{id: "other-session-active", last: now - 900, started: now - 3600},
		freshSess{id: "chatty-neighbour", last: now - 5, started: now - 100, msgs: []any{now - 5}},
		// cron and ended sessions never count, whatever their messages say
		freshSess{id: "cron", source: "cron", last: nil, msgs: []any{now - 1}},
		freshSess{id: "ended", last: now - 5, ended: now - 1, msgs: []any{now - 1}},
	)
	got := busyIDs(t, home)
	want := map[string]time.Duration{
		"null-beat-fresh-msg":  20 * time.Second,
		"stale-beat-fresh-msg": 45 * time.Second,
		"fresh-beat-no-msg":    10 * time.Second,
		"chatty-neighbour":     5 * time.Second,
	}
	if len(got) != len(want) {
		t.Fatalf("busy = %v, want %v", got, want)
	}
	for id, idle := range want {
		if g, ok := got[id]; !ok || g != idle {
			t.Errorf("%s: busy=%v idle=%v, want idle %v (all: %v)", id, ok, g, idle, got)
		}
	}
}

func TestBusyIgnoresTimestampsOutsideTheEpochWindow(t *testing.T) {
	now := nowEpoch()
	home := buildFresh(t, sessionsDDL,
		// a garbage heartbeat must not pin the session as busy for ever
		freshSess{id: "garbage-beat", last: garbage, started: now - 3600, msgs: []any{now - 900}},
		freshSess{id: "garbage-msg", last: now - 900, started: now - 3600, msgs: []any{garbage, now - 800}},
		freshSess{id: "negative-beat", last: -5.0, started: now - 3600},
		freshSess{id: "garbage-started", last: nil, started: garbage},
		// ... but a trusted fresh cell next to a garbage one still counts
		freshSess{id: "garbage-beat-fresh-msg", last: garbage, started: now - 3600, msgs: []any{now - 15}},
		freshSess{id: "garbage-msg-fresh-beat", last: now - 25, started: now - 3600, msgs: []any{garbage}},
	)
	got := busyIDs(t, home)
	want := map[string]time.Duration{
		"garbage-beat-fresh-msg": 15 * time.Second,
		"garbage-msg-fresh-beat": 25 * time.Second,
	}
	if len(got) != len(want) {
		t.Fatalf("busy = %v, want %v", got, want)
	}
	for id, idle := range want {
		if g, ok := got[id]; !ok || g != idle {
			t.Errorf("%s: busy=%v idle=%v, want idle %v", id, ok, g, idle)
		}
	}
}

// started_at is Hermes' fallback for a session with no trusted heartbeat and
// no trusted message; it never overrides an older heartbeat or message.
func TestBusyFallsBackToStartedAtOnlyWithoutOtherSignals(t *testing.T) {
	now := nowEpoch()
	home := buildFresh(t, sessionsDDL,
		freshSess{id: "just-created", last: nil, started: now - 12},
		freshSess{id: "just-created-garbage-beat", last: garbage, started: now - 12},
		freshSess{id: "old-start", last: nil, started: now - 3600},
		freshSess{id: "stale-beat-fresh-start", last: now - 900, started: now - 30},
	)
	got := busyIDs(t, home)
	want := map[string]time.Duration{"just-created": 12 * time.Second, "just-created-garbage-beat": 12 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("busy = %v, want %v", got, want)
	}
	for id, idle := range want {
		if g, ok := got[id]; !ok || g != idle {
			t.Errorf("%s: busy=%v idle=%v, want idle %v", id, ok, g, idle)
		}
	}
}

func TestBusyMessageWindowBoundaryIsExclusive(t *testing.T) {
	now := nowEpoch()
	home := buildFresh(t, sessionsDDL,
		freshSess{id: "edge", last: nil, msgs: []any{now - 180}},
		freshSess{id: "inside", last: nil, msgs: []any{now - 179}},
	)
	got := busyIDs(t, home)
	if _, ok := got["edge"]; ok || len(got) != 1 {
		t.Errorf("busy = %v, want only 'inside'", got)
	}
}

// Without idx_messages_session (an older schema) the check still works: it
// must not name an index.
func TestBusyWorksWithoutTheMessagesIndex(t *testing.T) {
	ddl := `create table sessions (id text primary key, title text, last_activity_at real, ended_at real, source text, started_at real);
create table messages (id integer primary key autoincrement, session_id text not null, role text, timestamp real not null)`
	now := nowEpoch()
	home := buildFresh(t, ddl, freshSess{id: "s", last: nil, msgs: []any{now - 7}})
	if got := busyIDs(t, home); len(got) != 1 || got["s"] != 7*time.Second {
		t.Errorf("busy = %v", got)
	}
}

// A schema the check cannot read counts as busy (never guess idle): no
// messages table, no timestamp column, no started_at column.
func TestBusyUnreadableSchemaCountsAsBusy(t *testing.T) {
	now := nowEpoch()
	for name, ddl := range map[string]string{
		"no messages table": `create table sessions (id text primary key, title text, last_activity_at real, ended_at real, source text, started_at real)`,
		"no timestamp column": `create table sessions (id text primary key, title text, last_activity_at real, ended_at real, source text, started_at real);
create table messages (id integer primary key, session_id text not null)`,
		"no session_id column": `create table sessions (id text primary key, title text, last_activity_at real, ended_at real, source text, started_at real);
create table messages (id integer primary key, timestamp real)`,
		"no started_at column": `create table sessions (id text primary key, title text, last_activity_at real, ended_at real, source text);
create table messages (id integer primary key, session_id text not null, timestamp real not null)`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			db := makeStateDB(t, filepath.Join(home, "state.db"), ddl, nil, false)
			if _, err := db.Exec(`insert into sessions (id, title, last_activity_at) values ('idle', 'x', ?)`, now-3600); err != nil {
				t.Fatal(err)
			}
			got, err := newSess(home).Busy(context.Background(), 180*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].ID != "hermes: state.db unreadable" || got[0].Title == "" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestBusyQueryIsOneReadOnlyStatement(t *testing.T) {
	for _, bad := range []string{"insert ", "update ", "delete ", "drop ", "create ", "pragma", "indexed by"} {
		if strings.Contains(strings.ToLower(busySessionsQL), bad) {
			t.Errorf("busy query contains %q", bad)
		}
	}
}
