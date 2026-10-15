package hermes

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, name "sqlite"
)

const (
	stateDBFile   = "state.db"
	rootProfile   = "hermes"
	sqliteBusyMs  = 5000
	titleMaxRunes = 50
	errTextMax    = 60
	// Hermes' trusted-timestamp window (hermes_cli/timefmt.py EPOCH_MIN /
	// EPOCH_MAX, `_SQL_IN_WINDOW` in hermes_state_common.py): a cell outside
	// it (garbage doubles salvaged from a damaged page, 5e+246) is ignored.
	epochMin = "0.0"
	epochMax = "4200000000.0"
	// busySessionsQL lists the open, non-cron sessions whose freshest sign of
	// life is newer than ?. Like Hermes' own `_sql_session_last_active`, that
	// is the later of last_activity_at (a heartbeat rate-limited to ~60 s, so
	// it can lag a newer message and is NULL on a session that has not beaten
	// yet) and the newest messages.timestamp of the session, and only when
	// neither is trusted started_at. A missing table or column fails the
	// query, and a failure counts as busy.
	busySessionsQL = `select id, title, fresh from (
 select s.id as id, coalesce(s.title,'') as title,
  coalesce(
   (select max(v) from (
     select case when s.last_activity_at between ` + epochMin + ` and ` + epochMax + ` then s.last_activity_at end as v
     union all
     select max(m.timestamp) from messages m
      where m.session_id = s.id and m.timestamp between ` + epochMin + ` and ` + epochMax + `
    ) where v is not null),
   case when s.started_at between ` + epochMin + ` and ` + epochMax + ` then s.started_at end
  ) as fresh
 from sessions s
 where s.ended_at is null and coalesce(s.source,'') != 'cron'
) where fresh > ?`
)

// SQLiteSessions implements Sessions over the Hermes state.db files. It only
// ever reads: connections are opened mode=ro with query_only on, and a
// failure for any database counts as busy (never guess idle).
type SQLiteSessions struct {
	Home string           // HERMES_HOME
	Now  func() time.Time // default time.Now
}

var _ Sessions = (*SQLiteSessions)(nil)

// NewSessions returns the real session detector for a Hermes home.
func NewSessions(home string) *SQLiteSessions { return &SQLiteSessions{Home: home} }

func (s *SQLiteSessions) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type stateHome struct{ profile, db string }

// stateDBs lists the root home and every profiles/* directory that has a
// state.db, root first, profiles sorted by name.
func (s *SQLiteSessions) stateDBs() []stateHome {
	var out []stateHome
	if p := filepath.Join(s.Home, stateDBFile); isFile(p) {
		out = append(out, stateHome{rootProfile, p})
	}
	entries, _ := os.ReadDir(filepath.Join(s.Home, "profiles"))
	var profiles []stateHome
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p := filepath.Join(s.Home, "profiles", e.Name(), stateDBFile); isFile(p) {
			profiles = append(profiles, stateHome{e.Name(), p})
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].profile < profiles[j].profile })
	return append(out, profiles...)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// dsn builds the read-only SQLite URI for a file path. The path is
// slash-converted and percent-escaped (a literal ? # % would otherwise end
// the path or be decoded).
func dsn(path string) string {
	p := filepath.ToSlash(path)
	u := url.URL{Path: p}
	esc := u.EscapedPath()
	if !strings.HasPrefix(esc, "/") {
		esc = "/" + esc // Windows drive path: file:///C:/...
	}
	q := fmt.Sprintf("mode=ro&_pragma=busy_timeout(%d)&_pragma=query_only(1)", sqliteBusyMs)
	return "file://" + esc + "?" + q
}

func (s *SQLiteSessions) open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Busy implements Sessions (python-behaviour §2.6).
func (s *SQLiteSessions) Busy(ctx context.Context, window time.Duration) ([]Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := s.now()
	cutoff := float64(now.Unix()) - window.Seconds()
	var out []Session
	for _, h := range s.stateDBs() {
		rows, err := s.busyIn(ctx, h, now, cutoff)
		if cerr := ctx.Err(); cerr != nil {
			return out, cerr
		}
		if err != nil {
			out = append(out, Session{
				Profile: h.profile,
				ID:      h.profile + ": state.db unreadable",
				Title:   truncRunes(err.Error(), errTextMax),
			})
			continue
		}
		out = append(out, rows...)
	}
	return out, nil
}

func (s *SQLiteSessions) busyIn(ctx context.Context, h stateHome, now time.Time, cutoff float64) (_ []Session, err error) {
	db, err := s.open(h.db)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := db.Close(); err == nil {
			err = cerr
		}
	}()
	qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(qctx, busySessionsQL, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	nowS := float64(now.Unix())
	for rows.Next() {
		var id, title string
		var last sql.NullFloat64
		if err := rows.Scan(&id, &title, &last); err != nil {
			return nil, err
		}
		la := nowS
		if last.Valid {
			la = last.Float64
		}
		out = append(out, Session{
			Profile: h.profile, ID: id, Title: truncRunes(title, titleMaxRunes),
			Idle: time.Duration(int64(nowS-la)) * time.Second,
		})
	}
	return out, rows.Err()
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
