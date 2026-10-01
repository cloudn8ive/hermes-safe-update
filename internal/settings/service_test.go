package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// fakeStore stands in for the Electron migrator: "localStorage" is a JSON
// file inside the copy's leveldb dir, so copy, swap and hashing touch real
// files. Calls are counted per mode ("dump1", "dump2", "write1", ...).
type fakeStore struct {
	mu        sync.Mutex
	counts    map[string]int
	fail      map[string]error // by call id
	crash     bool             // write half the keys, then fail
	dropWrite bool             // report success but write nothing
	onWrite   func()
	calls     []string
}

const fakeFile = "fake-store.json"

func fakeStorePath(ud string) string {
	return filepath.Join(ud, "Local Storage", "leveldb", fakeFile)
}

func readFake(ud string) map[string]map[string]string {
	m := map[string]map[string]string{}
	if b, err := os.ReadFile(fakeStorePath(ud)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeFake(t interface{ Fatal(...any) }, ud string, m map[string]map[string]string) {
	b, _ := json.MarshalIndent(m, "", " ")
	if err := os.MkdirAll(filepath.Dir(fakeStorePath(ud)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeStorePath(ud), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeStore) next(mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts == nil {
		f.counts = map[string]int{}
	}
	f.counts[mode]++
	id := fmt.Sprintf("%s%d", mode, f.counts[mode])
	f.calls = append(f.calls, id)
	return f.fail[id]
}

func (f *fakeStore) Dump(_ context.Context, ud string, origins []string) (map[string]map[string]string, error) {
	if err := f.next("dump"); err != nil {
		return nil, err
	}
	if !storeExists(filepath.Join(ud, "Local Storage")) {
		return nil, fmt.Errorf("%w: no store", ErrMigrator)
	}
	all := readFake(ud)
	out := map[string]map[string]string{}
	for _, o := range origins {
		out[o] = map[string]string{}
		for k, v := range all[o] {
			out[o][k] = v
		}
	}
	return out, nil
}

func (f *fakeStore) Write(_ context.Context, ud, origin string, writes map[string]string) error {
	if err := f.next("write"); err != nil {
		return err
	}
	if f.onWrite != nil {
		f.onWrite()
	}
	if f.dropWrite {
		return nil
	}
	all := readFake(ud)
	if all[origin] == nil {
		all[origin] = map[string]string{}
	}
	n := 0
	for k, v := range writes {
		if f.crash && n >= len(writes)/2 {
			writeFake(fatalPanic{}, ud, all)
			return fmt.Errorf("%w: write exited 3221225477: crashed", ErrMigrator)
		}
		all[origin][k] = v
		n++
	}
	writeFake(fatalPanic{}, ud, all)
	return nil
}

type fatalPanic struct{}

func (fatalPanic) Fatal(a ...any) { panic(fmt.Sprint(a...)) }

const (
	oldO = "file://"
	newO = "http://127.0.0.1:47891"
)

type env struct {
	t     *testing.T
	ud    string
	home  string
	procs *platform.FakeProcs
	store *fakeStore
	now   time.Time
	svc   *Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, ud: filepath.Join(root, "userData"), home: filepath.Join(root, "home"),
		procs: platform.NewFakeProcs(), store: &fakeStore{},
		now: time.Date(2026, 10, 1, 3, 15, 0, 0, time.UTC)}
	checkout := filepath.Join(e.home, "hermes-agent")
	exeCmd = `"` + filepath.Join(checkout, "apps", "desktop", "release", "win-unpacked", "Hermes.exe") + `"`
	rs := filepath.Join(checkout, filepath.FromSlash(hermes.RendererServerSource))
	must(t, os.MkdirAll(filepath.Dir(rs), 0o755))
	must(t, os.WriteFile(rs, []byte("const DEFAULT_PORT = 47891\n"), 0o644))
	writeFake(t, e.ud, map[string]map[string]string{
		oldO: {
			"theme":                            "dark",
			"plain":                            "old",
			"hermes.desktop.freshDraftKey":     "d-old",
			"hermes.desktop.sessionSeenCounts": `{"s1":1}`,
		},
		newO: {
			"plain":                            "new",
			"hermes.desktop.freshDraftKey":     "d-new",
			"hermes.desktop.sessionSeenCounts": `{"s2":2}`,
		},
	})
	ldb := filepath.Join(e.ud, "Local Storage", "leveldb")
	must(t, os.WriteFile(filepath.Join(ldb, "000005.ldb"), []byte("binary\x00stuff _file://"), 0o644))
	must(t, os.WriteFile(filepath.Join(ldb, "LOCK"), nil, 0o644))
	must(t, os.WriteFile(filepath.Join(e.ud, "window-state.json"), []byte(`{"x":1}`), 0o644))
	must(t, os.WriteFile(filepath.Join(e.ud, "Preferences"), []byte(`not a json name`), 0o644))
	e.svc = e.newService()
	return e
}

func (e *env) newService() *Manager {
	cfg := config.Defaults().Settings
	return New(Deps{
		Install: hermes.Install{Home: e.home, Checkout: filepath.Join(e.home, "hermes-agent"),
			UserData: e.ud, Electron: filepath.Join(e.home, "electron.exe")},
		Settings:       cfg,
		Procs:          e.procs,
		DesktopMarkers: []string{"win-unpacked"},
		HermesBefore:   "v1.0",
		HermesAfter:    "v1.1",
		Now:            func() time.Time { return e.now },
		WorkDir:        filepath.Join(e.home, "work"),
		store:          e.store,
	})
}

func (e *env) startHermes() {
	e.procs.Add(platform.Process{PID: 4242, Name: "Hermes.exe",
		Cmdline: exeCmd})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// snapshot maps every file under dir (relative slash path, LOCK excluded)
// to its content.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "LOCK" {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	must(t, err)
	return out
}

func assertSame(t *testing.T, before, after map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		var diff []string
		for k := range before {
			if before[k] != after[k] {
				diff = append(diff, k)
			}
		}
		for k := range after {
			if _, ok := before[k]; !ok {
				diff = append(diff, "+"+k)
			}
		}
		t.Errorf("live userData changed: %v", diff)
	}
}

func TestMigrateHappyPath(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.ud)
	rep, err := e.svc.Migrate(context.Background())
	must(t, err)
	if !rep.Changed || rep.BackupID != "settings-20261001-031500" {
		t.Fatalf("report = %+v", rep)
	}
	live := readFake(e.ud)[newO]
	want := map[string]string{
		"theme":                            "dark",            // add
		"plain":                            "old",             // old wins, first run
		"hermes.desktop.freshDraftKey":     "d-new",           // keep new
		"hermes.desktop.sessionSeenCounts": `{"s1":1,"s2":2}`, // union
	}
	if !reflect.DeepEqual(live, want) {
		t.Errorf("live new origin = %v", live)
	}
	if !reflect.DeepEqual(rep.Conflicts, []string{"hermes.desktop.freshDraftKey", "hermes.desktop.sessionSeenCounts", "plain"}) {
		t.Errorf("conflicts = %v", rep.Conflicts)
	}
	if !rep.Plan.FirstRun || rep.Plan.Source != oldO || rep.Plan.Target.URL != newO {
		t.Errorf("plan = %+v", rep.Plan)
	}
	for _, s := range []string{"add 1", "union 1", "old-wins 1", "keep-new 1", "3 conflicts", "settings-20261001-031500"} {
		if !strings.Contains(rep.Line, s) {
			t.Errorf("line %q lacks %q", rep.Line, s)
		}
	}
	if strings.Contains(rep.Line, "d-old") || strings.Contains(rep.Line, "dark") {
		t.Errorf("line leaks values: %q", rep.Line)
	}
	// the backup holds the original bytes
	bk := filepath.Join(e.home, "backups", rep.BackupID)
	got := snapshot(t, bk)
	for k, v := range before {
		if !strings.HasPrefix(k, "Local Storage/") && !strings.HasSuffix(k, ".json") {
			continue // only Local Storage and UI *.json are backed up
		}
		if got[k] != v {
			t.Errorf("backup %s differs", k)
		}
	}
	// nothing left behind in userData or the work dir
	for _, d := range []string{e.ud, filepath.Join(e.ud, "Local Storage")} {
		ents, _ := os.ReadDir(d)
		for _, en := range ents {
			if strings.Contains(en.Name(), ".hsu-") {
				t.Errorf("leftover %s in %s", en.Name(), d)
			}
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(e.home, "work")); len(ents) != 0 {
		t.Errorf("work dir not cleaned: %d entries", len(ents))
	}
	// read-only pre-plan, plan on the backed-up copy, write, re-plan in a
	// fresh process, live verify
	if !reflect.DeepEqual(e.store.calls, []string{"dump1", "dump2", "write1", "dump3", "dump4"}) {
		t.Errorf("calls = %v", e.store.calls)
	}

	// second run: nothing to do (idempotent), no backup taken
	e.now = e.now.Add(time.Hour)
	rep2, err := e.svc.Migrate(context.Background())
	must(t, err)
	if rep2.Changed || rep2.Plan.FirstRun || rep2.Plan.Writes() != 0 || rep2.BackupID != "" {
		t.Errorf("second run = %+v", rep2)
	}

	// the user changes a plain pref in the new origin: later runs keep it
	all := readFake(e.ud)
	all[newO]["plain"] = "user-changed"
	writeFake(t, e.ud, all)
	e.now = e.now.Add(time.Hour)
	rep3, err := e.svc.Migrate(context.Background())
	must(t, err)
	if rep3.Changed || readFake(e.ud)[newO]["plain"] != "user-changed" {
		t.Errorf("later run overwrote a new pref: %+v", rep3)
	}
	if !rep3.Plan.SourceUnchanged {
		t.Errorf("plan = %+v, want source unchanged", rep3.Plan)
	}
}

// Review finding 1: after a successful migration, later runs must not bring
// back keys or session entries the user removed from the new origin while
// the old origin is unchanged.
func TestLaterRunDoesNotResurrectFromUnchangedSource(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Migrate(context.Background())
	must(t, err)
	all := readFake(e.ud)
	delete(all[newO], "theme")
	all[newO]["hermes.desktop.sessionSeenCounts"] = `{"s2":2}`
	writeFake(t, e.ud, all)
	before := snapshot(t, e.ud)

	e.now = e.now.Add(24 * time.Hour)
	rep, err := e.svc.Migrate(context.Background())
	must(t, err)
	if rep.Changed || rep.Plan.Writes() != 0 || !strings.Contains(rep.Line, "unchanged since the last migration") {
		t.Errorf("rep = %+v", rep)
	}
	assertSame(t, before, snapshot(t, e.ud))
	p, err := e.svc.Plan(context.Background())
	must(t, err)
	if p.Writes() != 0 || p.Source != oldO {
		t.Errorf("dry-run plan = %+v", p)
	}

	// the old build was used again: its origin changed, so later-run rules
	// apply (one-side keys copied, plain prefs new-wins)
	all = readFake(e.ud)
	all[oldO]["added-in-old-build"] = "x"
	all[newO]["plain"] = "user-changed"
	writeFake(t, e.ud, all)
	e.now = e.now.Add(time.Hour)
	rep, err = e.svc.Migrate(context.Background())
	must(t, err)
	live := readFake(e.ud)[newO]
	if !rep.Changed || rep.Plan.FirstRun || live["added-in-old-build"] != "x" || live["plain"] != "user-changed" {
		t.Errorf("changed source not migrated with later-run rules: %+v", rep)
	}
	if k, _ := actionOf(rep.Plan, "plain"); k.Action != ActNewWins {
		t.Errorf("plain = %v", k.Action)
	}
}

// Review finding 2: a port change after compaction (file:// and the
// previous port share one .ldb, so equal recency) migrates from the
// previous port, not from the stale file:// origin.
func TestPortChangeWithTiedRecencyUsesPreviousPort(t *testing.T) {
	const port2 = "http://127.0.0.1:51234"
	for _, withMarker := range []bool{true, false} {
		t.Run(fmt.Sprintf("marker=%v", withMarker), func(t *testing.T) {
			e := newEnv(t)
			if withMarker {
				_, err := e.svc.Migrate(context.Background())
				must(t, err)
			}
			ldb := filepath.Join(e.ud, "Local Storage", "leveldb")
			must(t, os.Remove(filepath.Join(ldb, "000005.ldb")))
			must(t, os.WriteFile(filepath.Join(ldb, "000009.ldb"), []byte("x _file://\x00a _http://127.0.0.1:47891\x00b"), 0o644))
			e.svc.deps.Settings.Origin = port2
			e.now = e.now.Add(24 * time.Hour)
			p, err := e.svc.Plan(context.Background())
			must(t, err)
			if p.Source != newO || p.Target.URL != port2 {
				t.Errorf("source = %q, target = %q; want %q -> %q", p.Source, p.Target.URL, newO, port2)
			}
		})
	}
}

// Sandbox finding (C2): the backup and work copies must keep file mtimes.
// D9 picks a fallback-port origin only when its files are NEWER than the
// default port's; with fresh copy mtimes all origins tie, the re-plan on
// the work copy flipped the target back to DEFAULT_PORT and the real
// migration silently became "nothing to write".
func TestFallbackPortSurvivesTheWorkCopy(t *testing.T) {
	const port2 = "http://127.0.0.1:51234"
	e := newEnv(t)
	all := readFake(e.ud)
	all[port2] = map[string]string{"hermes.desktop.freshDraftKey": "d-port2"}
	writeFake(t, e.ud, all)
	ldb := filepath.Join(e.ud, "Local Storage", "leveldb")
	old := e.now.Add(-48 * time.Hour)
	must(t, os.WriteFile(filepath.Join(ldb, "000007.ldb"), []byte("x _http://127.0.0.1:47891\x00a"), 0o644))
	must(t, os.Chtimes(filepath.Join(ldb, "000005.ldb"), old, old))
	must(t, os.Chtimes(filepath.Join(ldb, "000007.ldb"), old, old))
	must(t, os.WriteFile(filepath.Join(ldb, "000009.ldb"), []byte("x _http://127.0.0.1:51234\x00a"), 0o644))

	p, err := e.svc.Plan(context.Background())
	must(t, err)
	if p.Target.URL != port2 || p.Source != newO {
		t.Fatalf("plan %s -> %s, want %s -> %s", p.Source, p.Target.URL, newO, port2)
	}
	rep, err := e.svc.Migrate(context.Background())
	must(t, err)
	if !rep.Changed || rep.Plan.Target.URL != port2 {
		t.Fatalf("Migrate = changed %v target %q (%s), want a migration into %s", rep.Changed, rep.Plan.Target.URL, rep.Line, port2)
	}
	if got := readFake(e.ud)[port2]["hermes.desktop.freshDraftKey"]; got != "d-port2" {
		t.Errorf("keep-new key on the fallback port = %q", got)
	}
	bs, err := e.svc.ListBackups()
	must(t, err)
	st, err := os.Stat(filepath.Join(bs[0].Path, "Local Storage", "leveldb", "000005.ldb"))
	must(t, err)
	if !st.ModTime().Equal(old) {
		t.Errorf("backup mtime = %v, want the original %v", st.ModTime(), old)
	}
}

// Review finding 3: runs with nothing to write take no backup, so the
// automatic run after every update cannot prune the pre-migration backup.
func TestNoOpMigrationsKeepTheFirstBackup(t *testing.T) {
	e := newEnv(t)
	first, err := e.svc.Migrate(context.Background())
	must(t, err)
	for i := 0; i < 4; i++ {
		e.now = e.now.Add(24 * time.Hour)
		rep, err := e.svc.Migrate(context.Background())
		must(t, err)
		if rep.Changed || rep.BackupID != "" {
			t.Errorf("no-op run %d = %+v", i, rep)
		}
	}
	bs, err := e.svc.ListBackups()
	must(t, err)
	if len(bs) != 1 || bs[0].ID != first.BackupID || bs[0].Manifest.Reason != "before migration" {
		t.Errorf("backups = %+v, want only %s", bs, first.BackupID)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.home, "work")); len(ents) != 0 {
		t.Errorf("work dir not cleaned: %d entries", len(ents))
	}
}

func TestMigrateFailuresLeaveLiveStoreUntouched(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(e *env)
		wantErr  error
		backedUp bool
	}{
		{"electron missing", func(e *env) {
			e.svc.store = nil // real Electron runner, path does not exist
		}, ErrElectronMissing, false},
		{"hermes running", func(e *env) { e.startHermes() }, apperr.ErrHermesRunning, false},
		{"process list fails", func(e *env) { e.procs.ListErr = errors.New("denied") }, nil, false},
		{"migrator crashes mid-write", func(e *env) { e.store.crash = true }, ErrMigrator, true},
		{"migrator dump fails before anything is written", func(e *env) {
			e.store.fail = map[string]error{"dump1": fmt.Errorf("%w: exited 1", ErrMigrator)}
		}, ErrMigrator, false},
		{"migrator dump of the backed-up copy fails", func(e *env) {
			e.store.fail = map[string]error{"dump2": fmt.Errorf("%w: exited 1", ErrMigrator)}
		}, ErrMigrator, true},
		{"re-plan not clean", func(e *env) { e.store.dropWrite = true }, ErrNotClean, true},
		{"hermes starts during migration", func(e *env) { e.store.onWrite = e.startHermes }, apperr.ErrHermesRunning, true},
		{"live verify fails: rollback", func(e *env) {
			e.store.fail = map[string]error{"dump4": fmt.Errorf("%w: exited 1", ErrMigrator)}
		}, ErrMigrator, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			before := snapshot(t, e.ud)
			_, err := e.svc.Migrate(context.Background())
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			assertSame(t, before, snapshot(t, e.ud))
			bs, _ := e.svc.ListBackups()
			if (len(bs) > 0) != tc.backedUp {
				t.Errorf("backups = %d, want backed up %v", len(bs), tc.backedUp)
			}
			if m, _ := loadMarker(filepath.Join(e.home, "backups")); !m.firstRun(oldO, newO) {
				t.Error("marker recorded after a failed migration")
			}
			if ents, _ := os.ReadDir(filepath.Join(e.home, "work")); len(ents) != 0 {
				t.Errorf("work dir not cleaned: %d entries", len(ents))
			}
		})
	}
}

func TestMigrateNothingToDo(t *testing.T) {
	t.Run("no store", func(t *testing.T) {
		e := newEnv(t)
		must(t, os.RemoveAll(filepath.Join(e.ud, "Local Storage")))
		rep, err := e.svc.Migrate(context.Background())
		if err != nil || rep.Changed || !strings.Contains(rep.Line, "no settings store") {
			t.Errorf("rep = %+v, err = %v", rep, err)
		}
	})
	t.Run("only the target origin", func(t *testing.T) {
		e := newEnv(t)
		writeFake(t, e.ud, map[string]map[string]string{newO: {"a": "1"}})
		must(t, os.Remove(filepath.Join(e.ud, "Local Storage", "leveldb", "000005.ldb")))
		before := snapshot(t, e.ud)
		rep, err := e.svc.Migrate(context.Background())
		if err != nil || rep.Changed || !strings.Contains(rep.Line, "nothing to migrate") {
			t.Errorf("rep = %+v, err = %v", rep, err)
		}
		assertSame(t, before, snapshot(t, e.ud))
	})
}

func TestPlanIsReadOnly(t *testing.T) {
	e := newEnv(t)
	e.startHermes() // dry-run is allowed while the app runs: it works on a copy
	before := snapshot(t, e.ud)
	p, err := e.svc.Plan(context.Background())
	must(t, err)
	if p.Writes() != 3 || !p.FirstRun || p.Target.URL != newO {
		t.Errorf("plan = %+v", p)
	}
	assertSame(t, before, snapshot(t, e.ud))
	if bs, _ := e.svc.ListBackups(); len(bs) != 0 {
		t.Error("dry-run made a backup")
	}
}

func TestDetectOriginsAndTarget(t *testing.T) {
	e := newEnv(t)
	got, err := e.svc.DetectOrigins(context.Background())
	must(t, err)
	if !reflect.DeepEqual(got, []Origin{{URL: oldO, Keys: 4}, {URL: newO, Keys: 3}}) {
		t.Errorf("origins = %+v", got)
	}
	tg, err := e.svc.DetectTarget(context.Background())
	if err != nil || tg.URL != newO || !strings.Contains(tg.Reason, "DEFAULT_PORT") {
		t.Errorf("target = %+v, %v", tg, err)
	}
	e.svc.deps.Settings.Origin = "http://127.0.0.1:50000"
	tg, _ = e.svc.DetectTarget(context.Background())
	if tg.URL != "http://127.0.0.1:50000" {
		t.Errorf("override: %+v", tg)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestBackupManifest(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.ud)
	b, err := e.svc.Backup(context.Background(), "manual")
	must(t, err)
	if b.ID != "settings-20261001-031500" || b.Path != filepath.Join(e.home, "backups", b.ID) {
		t.Errorf("backup = %+v", b)
	}
	raw, err := os.ReadFile(filepath.Join(b.Path, "manifest.json"))
	must(t, err)
	var m Manifest
	must(t, json.Unmarshal(raw, &m))
	if m.Version != 1 || m.Created != "2026-10-01T03:15:00Z" || m.Reason != "manual" || m.HermesBefore != "v1.0" || m.HermesAfter != "v1.1" {
		t.Errorf("manifest header = %+v", m)
	}
	wantFiles := map[string]string{
		"Local Storage/leveldb/" + fakeFile: sha(before["Local Storage/leveldb/"+fakeFile]),
		"Local Storage/leveldb/000005.ldb":  sha(before["Local Storage/leveldb/000005.ldb"]),
		"window-state.json":                 sha(`{"x":1}`),
	}
	if !reflect.DeepEqual(m.Files, wantFiles) {
		t.Errorf("files = %v", m.Files)
	}
	if !reflect.DeepEqual(m.KeysPerOrigin, map[string]int{oldO: 4, newO: 3}) {
		t.Errorf("keys per origin = %v", m.KeysPerOrigin)
	}
	assertSame(t, before, snapshot(t, e.ud))
}

func TestBackupWithoutElectronStillBacksUp(t *testing.T) {
	e := newEnv(t)
	e.svc.store = nil
	b, err := e.svc.Backup(context.Background(), "manual")
	must(t, err)
	if len(b.Manifest.Files) != 3 || len(b.Manifest.KeysPerOrigin) != 0 {
		t.Errorf("manifest = %+v", b.Manifest)
	}
}

func TestBackupRefusedWhileHermesRuns(t *testing.T) {
	e := newEnv(t)
	e.startHermes()
	if _, err := e.svc.Backup(context.Background(), "manual"); !errors.Is(err, apperr.ErrHermesRunning) {
		t.Errorf("err = %v", err)
	}
}

func TestRetentionKeepsNewestAndListsNewestFirst(t *testing.T) {
	e := newEnv(t)
	bdir := filepath.Join(e.home, "backups")
	must(t, os.MkdirAll(filepath.Join(bdir, "not-a-settings-backup"), 0o755))
	must(t, os.MkdirAll(filepath.Join(bdir, "ls-origin-20261001-022418"), 0o755))
	var ids []string
	for i := 0; i < 5; i++ {
		b, err := e.svc.Backup(context.Background(), "manual")
		must(t, err)
		ids = append(ids, b.ID)
		e.now = e.now.Add(time.Minute)
	}
	bs, err := e.svc.ListBackups()
	must(t, err)
	var got []string
	for _, b := range bs {
		got = append(got, b.ID)
	}
	if !reflect.DeepEqual(got, []string{ids[4], ids[3], ids[2]}) {
		t.Errorf("kept %v, want newest 3 of %v", got, ids)
	}
	for _, keep := range []string{"not-a-settings-backup", "ls-origin-20261001-022418"} {
		if _, err := os.Stat(filepath.Join(bdir, keep)); err != nil {
			t.Errorf("prune touched %s", keep)
		}
	}
	if !bs[0].Created.Equal(e.now.Add(-time.Minute)) {
		t.Errorf("created = %v", bs[0].Created)
	}
	n, err := e.svc.Prune(1)
	if err != nil || n != 2 {
		t.Errorf("prune(1) = %d, %v", n, err)
	}
	if _, err := e.svc.Prune(0); err == nil {
		t.Error("prune(0) must be refused")
	}
}

func TestBackupSameSecondGetsUniqueID(t *testing.T) {
	e := newEnv(t)
	a, err := e.svc.Backup(context.Background(), "manual")
	must(t, err)
	b, err := e.svc.Backup(context.Background(), "manual")
	must(t, err)
	if a.ID == b.ID || !strings.HasPrefix(b.ID, a.ID) {
		t.Errorf("ids %s, %s", a.ID, b.ID)
	}
	bs, _ := e.svc.ListBackups()
	if bs[0].ID != b.ID {
		t.Errorf("newest = %s", bs[0].ID)
	}
}

func TestRevertIsByteIdenticalAndRestoresMarker(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.ud)
	rep, err := e.svc.Migrate(context.Background())
	must(t, err)
	must(t, os.WriteFile(filepath.Join(e.ud, "window-state.json"), []byte(`{"x":2}`), 0o644))
	e.now = e.now.Add(time.Minute)
	rr, err := e.svc.Revert(context.Background(), rep.BackupID)
	must(t, err)
	if !rr.Changed || rr.BackupID != rep.BackupID || !strings.Contains(rr.Line, rep.BackupID) {
		t.Errorf("revert report = %+v", rr)
	}
	assertSame(t, before, snapshot(t, e.ud))
	m, err := loadMarker(filepath.Join(e.home, "backups"))
	must(t, err)
	if !m.firstRun(oldO, newO) {
		t.Error("marker not restored: the next migration would not be a first run")
	}
	// a "before revert" backup of the migrated state exists
	bs, _ := e.svc.ListBackups()
	if bs[0].Manifest.Reason != "before revert" {
		t.Errorf("newest backup = %+v", bs[0].Manifest)
	}
	// revert "" = newest = undo the revert
	e.now = e.now.Add(time.Minute)
	_, err = e.svc.Revert(context.Background(), "")
	must(t, err)
	if readFake(e.ud)[newO]["theme"] != "dark" {
		t.Error("revert of the revert did not bring the migrated state back")
	}
}

func TestRevertRefusals(t *testing.T) {
	t.Run("hermes running", func(t *testing.T) {
		e := newEnv(t)
		b, err := e.svc.Backup(context.Background(), "manual")
		must(t, err)
		e.startHermes()
		if _, err := e.svc.Revert(context.Background(), b.ID); !errors.Is(err, apperr.ErrHermesRunning) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("tampered backup", func(t *testing.T) {
		e := newEnv(t)
		b, err := e.svc.Backup(context.Background(), "manual")
		must(t, err)
		must(t, os.WriteFile(filepath.Join(b.Path, "Local Storage", "leveldb", "000005.ldb"), []byte("changed"), 0o644))
		writeFake(t, e.ud, map[string]map[string]string{newO: {"z": "1"}})
		before := snapshot(t, e.ud)
		if _, err := e.svc.Revert(context.Background(), b.ID); err == nil || !strings.Contains(err.Error(), "000005.ldb") {
			t.Errorf("err = %v", err)
		}
		assertSame(t, before, snapshot(t, e.ud))
	})
	t.Run("tampered ui json in backup", func(t *testing.T) {
		e := newEnv(t)
		b, err := e.svc.Backup(context.Background(), "manual")
		must(t, err)
		must(t, os.WriteFile(filepath.Join(b.Path, "window-state.json"), []byte(`{"x":9}`), 0o644))
		must(t, os.WriteFile(filepath.Join(e.ud, "window-state.json"), []byte(`{"x":5}`), 0o644))
		before := snapshot(t, e.ud)
		if _, err := e.svc.Revert(context.Background(), b.ID); err == nil || !strings.Contains(err.Error(), "window-state.json") {
			t.Errorf("err = %v", err)
		}
		assertSame(t, before, snapshot(t, e.ud))
		// a damaged backup is refused before anything happens: no extra
		// "before revert" backup that could prune an older good one
		if bs, _ := e.svc.ListBackups(); len(bs) != 1 {
			t.Errorf("backups = %d, want 1", len(bs))
		}
	})
	t.Run("unknown id", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Revert(context.Background(), "settings-19990101-000000"); err == nil {
			t.Error("want error")
		}
	})
	t.Run("no backups", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Revert(context.Background(), ""); err == nil {
			t.Error("want error")
		}
	})
	t.Run("path traversal id", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Revert(context.Background(), `..\..\x`); err == nil {
			t.Error("want error")
		}
	})
}

var _ Service = (*Manager)(nil)

// The manifest records the Hermes versions of the run that made the backup;
// callers learn them after New (the update flow knows "after" only later).
func TestSetHermesVersionsFillsTheManifest(t *testing.T) {
	e := newEnv(t)
	e.svc.SetHermesVersions("v2.0", "v2.1")
	b, err := e.svc.Backup(context.Background(), "manual")
	must(t, err)
	raw, err := os.ReadFile(filepath.Join(b.Path, "manifest.json"))
	must(t, err)
	var m Manifest
	must(t, json.Unmarshal(raw, &m))
	if m.HermesBefore != "v2.0" || m.HermesAfter != "v2.1" {
		t.Errorf("manifest versions = %q -> %q", m.HermesBefore, m.HermesAfter)
	}
}

// A manifest from a shared backup dir is untrusted: forged keys must not
// make revert write (or hash) anything outside userData.
func TestRevertRejectsForgedManifestKeys(t *testing.T) {
	keys := []string{`..\x.json`, `../x.json`, `C:\x.json`, `\\server\x.json`, `Local Storage/../../x`, `sub\x.json`}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			e := newEnv(t)
			b, err := e.svc.Backup(context.Background(), "manual")
			must(t, err)
			content := []byte(`{"evil":1}`)
			sum := sha256.Sum256(content)
			mp := filepath.Join(b.Path, "manifest.json")
			raw, err := os.ReadFile(mp)
			must(t, err)
			var m Manifest
			must(t, json.Unmarshal(raw, &m))
			m.Files[key] = hex.EncodeToString(sum[:])
			out, err := json.Marshal(m)
			must(t, err)
			must(t, os.WriteFile(mp, out, 0o644))
			// plant the payload where the forged key would point, in the backup
			_ = os.WriteFile(filepath.Join(b.Path, filepath.FromSlash(strings.ReplaceAll(key, `\`, "/"))), content, 0o644)
			parent := filepath.Dir(e.ud)
			outside := snapshot(t, parent)
			before := snapshot(t, e.ud)
			_, err = e.svc.Revert(context.Background(), b.ID)
			if err == nil || !strings.Contains(err.Error(), "damaged") {
				t.Fatalf("err = %v, want damaged-backup error", err)
			}
			assertSame(t, before, snapshot(t, e.ud))
			assertSame(t, outside, snapshot(t, parent))
			if bs, _ := e.svc.ListBackups(); len(bs) != 0 {
				t.Errorf("ListBackups returned the forged backup")
			}
		})
	}
}

// ---- RT-4F: builds without renderer-server.ts (renderer loaded from file://)

const mainLoadsFromFile = "const url = `${pathToFileURL(resolveRendererIndex()).toString()}?win=quick#/`\n"

func (e *env) setMain(content string) {
	e.t.Helper()
	co := filepath.Join(e.home, "hermes-agent")
	_ = os.Remove(filepath.Join(co, filepath.FromSlash(hermes.RendererServerSource)))
	if content == "" {
		return
	}
	p := filepath.Join(co, filepath.FromSlash(hermes.DesktopMainSource))
	must(e.t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(e.t, os.WriteFile(p, []byte(content), 0o644))
}

// The store holds both origins: the old 127.0.0.1 port (older settings) and
// file:// (what the running app writes now). Target file://, source 47891.
func TestFileTargetWhenRendererServerIsGone(t *testing.T) {
	e := newEnv(t)
	e.setMain(mainLoadsFromFile)
	writeFake(t, e.ud, map[string]map[string]string{
		newO: {"theme": "dark", "plain": "old", "layoutTree.v2": "old-layout"},
		oldO: {"plain": "new", "hermes.desktop.freshDraftKey": "d-new"},
	})
	must(t, os.WriteFile(filepath.Join(e.ud, "Local Storage", "leveldb", "000007.ldb"), []byte("x _http://127.0.0.1:47891\x00a"), 0o644))
	d, err := e.svc.DetectTarget(context.Background())
	must(t, err)
	if d.URL != oldO || !strings.Contains(d.Reason, "no renderer-server.ts in this build") {
		t.Fatalf("target = %+v", d)
	}
	p, err := e.svc.Plan(context.Background())
	must(t, err)
	if p.Source != newO || p.Target.URL != oldO || !p.FirstRun {
		t.Fatalf("plan %s -> %s first=%v", p.Source, p.Target.URL, p.FirstRun)
	}
	c := p.Counts()
	if c[ActAdd] != 2 || c[ActOldWins] != 1 || len(Conflicts(p)) != 1 {
		t.Errorf("counts = %v conflicts %v", c, Conflicts(p))
	}
	rep, err := e.svc.Migrate(context.Background())
	must(t, err)
	if !rep.Changed || !strings.Contains(rep.Line, newO+" -> file://") {
		t.Fatalf("report = %+v", rep)
	}
	live := readFake(e.ud)[oldO]
	if live["plain"] != "old" || live["theme"] != "dark" || live["layoutTree.v2"] != "old-layout" || live["hermes.desktop.freshDraftKey"] != "d-new" {
		t.Errorf("live file:// = %v", live)
	}
	// later run: the source did not change, so nothing is written again
	rep, err = e.svc.Migrate(context.Background())
	must(t, err)
	if rep.Changed || !strings.Contains(rep.Line, "unchanged since the last migration") {
		t.Errorf("second run = %+v", rep)
	}
}

func TestUnrecognisedMainBacksUpAndNamesTheCommand(t *testing.T) {
	for name, main := range map[string]string{
		"unrecognisable main.ts":        "const nothing = 1\n",
		"no main.ts":                    "",
		"still imports renderer-server": "import { x } from './renderer-server'\n" + mainLoadsFromFile,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.setMain(main)
			before := snapshot(t, filepath.Join(e.ud, "Local Storage"))
			rep, err := e.svc.Migrate(context.Background())
			var oe *OriginError
			if !errors.As(err, &oe) {
				t.Fatalf("err = %v, want *OriginError", err)
			}
			if oe.BackupID == "" || rep.BackupID != oe.BackupID {
				t.Fatalf("no backup id: %+v / %+v", oe, rep)
			}
			for _, s := range []string{"cannot detect the target origin", oe.BackupID, "hermes-safe-update migrate-settings --origin <url>"} {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q lacks %q", err, s)
				}
			}
			bs, lerr := e.svc.ListBackups()
			must(t, lerr)
			if len(bs) != 1 || bs[0].ID != oe.BackupID || bs[0].Manifest.Reason != "origin not detected" {
				t.Errorf("backups = %+v", bs)
			}
			assertSame(t, before, snapshot(t, filepath.Join(e.ud, "Local Storage")))
		})
	}
}

func TestRendererServerPresentIsUnchanged(t *testing.T) {
	e := newEnv(t)
	// both files: renderer-server.ts wins (DEFAULT_PORT), main.ts is not consulted
	co := filepath.Join(e.home, "hermes-agent")
	p := filepath.Join(co, filepath.FromSlash(hermes.DesktopMainSource))
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(mainLoadsFromFile), 0o644))
	d, err := e.svc.DetectTarget(context.Background())
	must(t, err)
	if d.URL != newO || !strings.Contains(d.Reason, "DEFAULT_PORT") {
		t.Errorf("target = %+v", d)
	}
}

func TestOriginOverrideWinsWithoutRendererServer(t *testing.T) {
	e := newEnv(t)
	e.setMain("const nothing = 1\n")
	e.svc.deps.Settings.Origin = newO
	p, err := e.svc.Plan(context.Background())
	must(t, err)
	if p.Target.URL != newO || !strings.Contains(p.Target.Reason, "--origin") {
		t.Errorf("plan target = %+v", p.Target)
	}
	rep, err := e.svc.Migrate(context.Background())
	must(t, err)
	if !rep.Changed {
		t.Errorf("report = %+v", rep)
	}
}
