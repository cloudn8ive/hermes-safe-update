// Package statedb looks at Hermes' session databases (state.db: the home's
// own and one per profile) and keeps Safe Update's own verified copy of one.
// Hermes' pre-update snapshot skips a database over 1 GiB, so without this an
// update of a big install runs with no copy of the session history at all.
//
// Everything here is read-only on the source: the copy is an SQLite online
// read (VACUUM INTO, a read transaction), which works while Hermes or its
// gateway has the file open. The pure-Go driver is already a dependency.
package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	_ "modernc.org/sqlite" // pure-Go driver, name "sqlite"
)

// DefaultProfile is the name used for the Hermes home's own state.db.
const DefaultProfile = "default"

// LegacyDefaultProfile labels a profiles/default folder. Hermes reads "default"
// as the home itself (hermes_cli/profiles.py: "default" is a reserved name), so
// the folder cannot be addressed with -p and must not share the root's label.
const LegacyDefaultProfile = "profiles/default"

// WALAllowance is added to the size of the -wal file when a copy's disk need
// is worked out: frames may be written while the copy runs, and Hermes caps
// its WAL near 64 MiB (hermes_state_wal.py, _WAL_SIZE_LIMIT_BYTES).
const WALAllowance int64 = 64 << 20

// DB is one state.db found on disk.
type DB struct {
	Profile string // "default" or the profile folder name
	Path    string
	Size    int64
	WAL     int64 // size of the -wal file next to it, 0 when there is none
}

// CopyNeed is the free space a copy of this database takes: the file, the
// frames still in its -wal (VACUUM INTO reads them) and an allowance.
func (d DB) CopyNeed() uint64 { return uint64(d.Size + d.WAL + WALAllowance) }

// Service is what the update flow needs; tests replace it.
type Service interface {
	// Find lists the state.db files of a Hermes home: the home's own, then
	// profiles/<name>/state.db, smallest first. Missing files are not listed.
	Find(home string) []DB
	// Ignored lists profile folders Find left out because their name is not
	// a Hermes profile id (see hermes.ValidProfileID).
	Ignored(home string) []string
	// CompactionWork asks, read-only, whether Hermes has compaction work on
	// the database at path (see CompactionWork).
	CompactionWork(ctx context.Context, path string) Compaction
	// Backup makes (and verifies) a copy of one database.
	Backup(ctx context.Context, req BackupReq) (BackupResult, error)
}

// Real is the Service on the real disk.
type Real struct{}

// Find implements Service.
func (Real) Find(home string) []DB { return Find(home) }

// Ignored implements Service.
func (Real) Ignored(home string) []string { return Ignored(home) }

// CompactionWork implements Service.
func (Real) CompactionWork(ctx context.Context, path string) Compaction {
	return CompactionWork(ctx, path)
}

// Backup implements Service.
func (Real) Backup(ctx context.Context, req BackupReq) (BackupResult, error) {
	return Backup(ctx, req)
}

// Find lists the state.db files of a Hermes home, smallest first (so the
// quick ones are done first and a long one cannot hold them up). Profile
// folders whose name is not a Hermes profile id are left out (see Ignored): a
// name outside [a-z0-9_-] could collide with another once made file-safe.
func Find(home string) []DB {
	dbs, _ := scan(home)
	return dbs
}

// Ignored lists the profile folders (with a state.db) that Find skips.
func Ignored(home string) []string {
	_, ign := scan(home)
	return ign
}

func scan(home string) (out []DB, ignored []string) {
	add := func(profile, path string) {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			d := DB{Profile: profile, Path: path, Size: st.Size()}
			if w, err := os.Stat(path + "-wal"); err == nil && w.Mode().IsRegular() {
				d.WAL = w.Size()
			}
			out = append(out, d)
		}
	}
	add(DefaultProfile, filepath.Join(home, "state.db"))
	if ents, err := os.ReadDir(filepath.Join(home, "profiles")); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(home, "profiles", e.Name(), "state.db")
			switch {
			case e.Name() == DefaultProfile:
				add(LegacyDefaultProfile, path)
			case !hermes.ValidProfileID(e.Name()):
				if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
					ignored = append(ignored, e.Name())
				}
			default:
				add(e.Name(), path)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	return out, ignored
}

// Over returns the databases larger than limit bytes.
func Over(dbs []DB, limit int64) []DB {
	var out []DB
	for _, d := range dbs {
		if d.Size > limit {
			out = append(out, d)
		}
	}
	return out
}

// FormatSize renders bytes the way Hermes does ("1.2 GB", binary units).
func FormatSize(n int64) string {
	const k = 1024
	f := float64(n)
	switch {
	case n >= k*k*k:
		return fmt.Sprintf("%.1f GB", f/(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.1f MB", f/(k*k))
	case n >= k:
		return fmt.Sprintf("%.1f KB", f/k)
	}
	return fmt.Sprintf("%d B", n)
}

// BackupReq is one copy to make.
type BackupReq struct {
	Src DB
	// Dir is where copies live (backups/safe-update-statedb); created.
	Dir string
	// Stamp is the time part of the file name (20261110-153000).
	Stamp string
	// Free reports the free bytes of the volume holding a path.
	Free func(path string) (uint64, error)
	// MinFree is the free space that must remain after the copy, in bytes.
	MinFree uint64
	// HolderRuns says a Hermes process (desktop, backend, kernel or gateway)
	// has the database open now. In any journal mode but WAL the copy's read
	// lock would block that process's writes for the whole copy, so then the
	// copy is not made (Skipped says why).
	HolderRuns bool
	// Check overrides the verification of the copy (tests); nil = quick_check.
	Check func(ctx context.Context, path string) error
}

// BackupResult says what a copy did. Skipped is set (and err nil) when the
// copy was not made on purpose; the old copies are then untouched.
type BackupResult struct {
	Path     string
	Size     int64
	Took     time.Duration
	Skipped  string
	Replaced []string // older copies of this profile that were removed
}

var nameSafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func safeName(profile string) string {
	// "profiles/default" becomes "profiles.default": a profile id has no dot,
	// so no real profile can take that file name.
	s := nameSafe.ReplaceAllString(strings.ReplaceAll(profile, "/", "."), "_")
	if s == "" {
		return "profile"
	}
	return s
}

func copyRe(profile string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(safeName(profile)) + `-\d{8}-\d{6}\.db$`)
}

// Backup copies req.Src into req.Dir as <profile>-<stamp>.db, checks the copy
// with PRAGMA quick_check, and only then removes the older copies of the same
// profile (the newest one is kept). It never touches the source. A copy that
// would leave less than MinFree free is not made (Skipped says why).
func Backup(ctx context.Context, req BackupReq) (res BackupResult, err error) {
	start := time.Now()
	defer func() { res.Took = time.Since(start) }()
	if err = os.MkdirAll(req.Dir, 0o755); err != nil {
		return res, fmt.Errorf("cannot create %s: %w", req.Dir, err)
	}
	if req.Free == nil {
		return res, errors.New("statedb: no free-space function")
	}
	free, ferr := req.Free(req.Dir)
	if ferr != nil {
		res.Skipped = "free disk space could not be read (" + ferr.Error() + ")"
		return res, nil
	}
	need := req.Src.CopyNeed()
	if free < need || free-need < req.MinFree {
		res.Skipped = fmt.Sprintf("only %s free; a copy needs about %s (file, -wal and an allowance) and %s must stay free",
			FormatSize(int64(free)), FormatSize(int64(need)), FormatSize(int64(req.MinFree)))
		return res, nil
	}
	if req.HolderRuns {
		mode, merr := JournalMode(ctx, req.Src.Path)
		if merr != nil || mode != "wal" {
			if merr != nil {
				mode = "unknown (" + merr.Error() + ")"
			}
			res.Skipped = "the database is open in another process and its journal mode is " + mode +
				", not wal: a copy would block Hermes' writes while it runs"
			return res, nil
		}
	}
	name := safeName(req.Src.Profile) + "-" + req.Stamp + ".db"
	final := filepath.Join(req.Dir, name)
	partial := final + ".partial"
	removePartials(req.Dir, req.Src.Profile)
	defer func() {
		if err != nil {
			_ = os.Remove(partial)
		}
	}()
	if _, serr := os.Stat(final); serr == nil {
		return res, fmt.Errorf("%s already exists", name)
	}
	if err = vacuumInto(ctx, req.Src.Path, partial); err != nil {
		return res, fmt.Errorf("copy failed: %w", err)
	}
	check := req.Check
	if check == nil {
		check = QuickCheck
	}
	if err = check(ctx, partial); err != nil {
		return res, fmt.Errorf("the copy did not pass its check (%w); it was deleted and older copies were kept", err)
	}
	st, serr := os.Stat(partial)
	if serr != nil {
		return res, serr
	}
	if err = os.Rename(partial, final); err != nil {
		return res, err
	}
	res.Path, res.Size = final, st.Size()
	re := copyRe(req.Src.Profile)
	if ents, derr := os.ReadDir(req.Dir); derr == nil {
		for _, e := range ents {
			if e.Name() != name && !e.IsDir() && re.MatchString(e.Name()) {
				if rerr := os.Remove(filepath.Join(req.Dir, e.Name())); rerr == nil {
					res.Replaced = append(res.Replaced, e.Name())
				}
			}
		}
	}
	return res, nil
}

func removePartials(dir, profile string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	pre := safeName(profile) + "-"
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), pre) && strings.HasSuffix(e.Name(), ".db.partial") && copyRe(profile).MatchString(strings.TrimSuffix(e.Name(), ".partial")) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// dsn builds a file: URI for a path (slash-converted, percent-escaped; a
// Windows drive path becomes file:///C:/...).
func dsn(path string, query string) string {
	u := url.URL{Path: filepath.ToSlash(path)}
	esc := u.EscapedPath()
	if !strings.HasPrefix(esc, "/") {
		esc = "/" + esc
	}
	return "file://" + esc + "?" + query
}

func vacuumInto(ctx context.Context, src, dst string) error {
	// mode=ro: the source is only read (query_only would also forbid the
	// VACUUM INTO output file). A long busy timeout
	// lets a writer finish its transaction; WAL readers do not block writers.
	db, err := sql.Open("sqlite", dsn(src, "mode=ro&_pragma=busy_timeout(30000)&_pragma=cache_size(-16384)"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}

// JournalMode reads PRAGMA journal_mode of a database file, read-only
// ("wal", "delete", ...). The mode of a WAL database is stored in its header,
// so this changes nothing.
func JournalMode(ctx context.Context, path string) (string, error) {
	db, err := sql.Open("sqlite", dsn(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return "", err
	}
	return strings.ToLower(mode), nil
}

// QuickCheck runs PRAGMA quick_check on a database file, read-only.
func QuickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", dsn(path, "mode=ro&_pragma=busy_timeout(5000)&_pragma=cache_size(-16384)"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	var first string
	n := 0
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		if n == 0 {
			first = s
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != 1 || first != "ok" {
		if first == "" {
			first = "no answer"
		}
		return fmt.Errorf("quick_check: %s", first)
	}
	return nil
}
