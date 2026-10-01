package settings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// ErrNotClean: after writing, a fresh-process re-plan still wanted to write
// keys (the copy did not take the migration). Nothing live was changed.
var ErrNotClean = errors.New("settings re-plan after migration is not clean")

const (
	lsDirName      = "Local Storage"
	manifestFile   = "manifest.json"
	markerCopyFile = "marker.json" // the migration marker as it was at backup time
	backupPrefix   = "settings-"
	stampLayout    = "20060102-150405"
	tmpPrefix      = ".hsu-" // work dirs next to the live store (same volume)
	manifestVer    = 1
	defaultTimeout = 2 * time.Minute
)

var backupIDRe = regexp.MustCompile(`^settings-[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)

// Deps are what Manager needs. Zero values get sensible defaults.
type Deps struct {
	Install        hermes.Install
	Settings       config.Settings
	Procs          platform.Procs
	DesktopMarkers []string // platform.Paths.DesktopProcessMarkers()
	Runner         execx.Runner
	HermesBefore   string // version shown in the manifest
	HermesAfter    string
	Now            func() time.Time
	// WorkDir holds throw-away copies and migrator output (which contains
	// values); every run removes what it created. Default
	// <home>/cache/safe-update-settings.
	WorkDir         string
	ElectronTimeout time.Duration // per migrator run; default 2 min

	store storeIO // tests
}

// SetHermesVersions sets the versions the next backups record in their
// manifest (the update flow learns "after" only once the update ran).
func (m *Manager) SetHermesVersions(before, after string) {
	m.deps.HermesBefore, m.deps.HermesAfter = before, after
}

// Manager implements Service.
type Manager struct {
	deps  Deps
	store storeIO
}

var _ Service = (*Manager)(nil)

// New returns the settings service.
func New(d Deps) *Manager {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Runner == nil {
		d.Runner = execx.New()
	}
	if d.WorkDir == "" {
		d.WorkDir = filepath.Join(d.Install.Home, "cache", "safe-update-settings")
	}
	if d.ElectronTimeout <= 0 {
		d.ElectronTimeout = defaultTimeout
	}
	return &Manager{deps: d, store: d.store}
}

func (m *Manager) io() storeIO {
	if m.store != nil {
		return m.store
	}
	return &electronIO{runner: m.deps.Runner, electron: m.deps.Install.Electron, workDir: m.deps.WorkDir, timeout: m.deps.ElectronTimeout}
}

// ready fails early (before any backup) when the migrator cannot run.
func (m *Manager) ready() error {
	if c, ok := m.io().(interface{ check() error }); ok {
		return c.check()
	}
	return nil
}

func (m *Manager) lsDir() string { return filepath.Join(m.deps.Install.UserData, lsDirName) }

func (m *Manager) backupDir() string {
	if m.deps.Settings.BackupDir != "" {
		return m.deps.Settings.BackupDir
	}
	return filepath.Join(m.deps.Install.Home, "backups")
}

func (m *Manager) retention() int {
	if m.deps.Settings.BackupRetention > 0 {
		return m.deps.Settings.BackupRetention
	}
	return 3
}

// ensureClosed refuses while the desktop app runs on this userData. A
// process list that cannot be read is a refusal too (never guess "closed").
// A desktop instance that provably runs on ANOTHER userData dir (its helper
// children carry a different --user-data-dir, e.g. the live app while a
// sandbox copy is migrated) does not block.
func (m *Manager) ensureClosed(ctx context.Context) error {
	if m.deps.Procs == nil {
		return errors.New("cannot check whether Hermes is running (no process lister)")
	}
	ps, err := m.deps.Procs.List(ctx)
	if err != nil {
		return fmt.Errorf("cannot check whether Hermes is running: %w", err)
	}
	for _, p := range ps {
		if hermes.Classify(p, m.deps.Install.Home, m.deps.Install.Checkout, m.deps.DesktopMarkers) == hermes.ClassDesktop &&
			!usesOtherUserData(p, ps, m.deps.Install.UserData) {
			return fmt.Errorf("%w (pid %d): close it first; settings were not changed", apperr.ErrHermesRunning, p.PID)
		}
	}
	return nil
}

// ---------------------------------------------------------------- copies

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// copyTree copies src into dst (created), skipping LevelDB's LOCK and
// anything that is not a regular file or directory (symlinks). It returns
// relative slash path -> sha256 of every copied file, prefixed by prefix.
func copyTree(src, dst, prefix string) (map[string]string, error) {
	sums := map[string]string{}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case !d.Type().IsRegular(), d.Name() == lockFile:
			return nil
		}
		sum, err := copyFile(p, target)
		if err != nil {
			return err
		}
		sums[prefix+filepath.ToSlash(rel)] = sum
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("copy %q: %w", src, err)
	}
	return sums, nil
}

func copyFile(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	// Keep the modification time: origin recency (D9 fallback-port
	// detection, source choice) is read from file mtimes, so a copy with
	// fresh mtimes would plan differently from the store it was taken from.
	if st, err := in.Stat(); err == nil {
		if err := os.Chtimes(dst, st.ModTime(), st.ModTime()); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSum(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// uiJSONFiles lists the top-level *.json files of userData (UI state such as
// window-state.json).
func uiJSONFiles(userData string) ([]string, error) {
	ents, err := os.ReadDir(userData)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", userData, err)
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// workCopy copies the live (or a backup's) Local Storage into a fresh
// userData-shaped dir under parent and returns that dir. The caller removes it.
func workCopy(lsSrc, parent, tag string) (string, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create %q: %w", parent, err)
	}
	dir, err := os.MkdirTemp(parent, tmpPrefix+tag+"-")
	if err != nil {
		return "", fmt.Errorf("create work copy: %w", err)
	}
	if _, err := copyTree(lsSrc, filepath.Join(dir, lsDirName), ""); err != nil {
		_ = removeAll(dir)
		return "", err
	}
	return dir, nil
}

// cleanWorkDir removes the work dir when it is empty (never other files).
func (m *Manager) cleanWorkDir() { _ = os.Remove(m.deps.WorkDir) }

// removeAll is os.RemoveAll with retries. On Windows a just-killed Electron
// (or its crashpad/GPU helpers) keeps handles in a copy for a moment, so a
// single RemoveAll after a failed migrator run left work copies (with
// setting values) behind next to the live store.
func removeAll(p string) error {
	delay := 50 * time.Millisecond
	var err error
	for range 10 {
		if err = os.RemoveAll(p); err == nil {
			return nil
		}
		time.Sleep(delay)
		if delay < time.Second {
			delay *= 2
		}
	}
	return err
}

// ---------------------------------------------------------------- reading

// snapshotStore copies the live store, dumps the candidate origins with the
// migrator and returns origin -> values plus each origin's recency.
func (m *Manager) readStore(ctx context.Context, lsSrc string, target string) (map[string]map[string]string, map[string]time.Time, error) {
	recency, err := scanOrigins(lsSrc)
	if err != nil {
		return nil, nil, err
	}
	ud, err := workCopy(lsSrc, m.deps.WorkDir, "read")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = removeAll(ud); m.cleanWorkDir() }()
	dump, err := m.io().Dump(ctx, ud, dumpOrigins(recency, target))
	return dump, recency, err
}

// dumpOrigins: every scanned candidate, plus file:// and the target (a
// key-position scan can miss an origin hidden in a compressed block).
func dumpOrigins(recency map[string]time.Time, target string) []string {
	set := map[string]bool{"file://": true}
	if target != "" {
		set[target] = true
	}
	for o := range recency {
		set[o] = true
	}
	out := make([]string, 0, len(set))
	for o := range set {
		out = append(out, o)
	}
	sortOrigins(out)
	return out
}

// sortOrigins: file:// first, then by URL.
func sortOrigins(os []string) {
	sort.Slice(os, func(i, j int) bool {
		if (os[i] == "file://") != (os[j] == "file://") {
			return os[i] == "file://"
		}
		return os[i] < os[j]
	})
}

func keyCounts(dump map[string]map[string]string) map[string]int {
	out := map[string]int{}
	for o, kv := range dump {
		if len(kv) > 0 {
			out[o] = len(kv)
		}
	}
	return out
}

func (m *Manager) target(recency map[string]time.Time) (Target, error) {
	if m.deps.Settings.Origin != "" {
		return chooseTarget(m.deps.Settings.Origin, "", recency), nil
	}
	def, err := defaultPortOrigin(m.deps.Install.Checkout)
	if err != nil {
		return Target{}, err
	}
	return chooseTarget("", def, recency), nil
}

// DetectOrigins lists the origins that hold keys (read through Chromium on
// a copy; allowed while Hermes runs).
func (m *Manager) DetectOrigins(ctx context.Context) ([]Origin, error) {
	if !storeExists(m.lsDir()) {
		return nil, nil
	}
	t, _ := m.DetectTarget(ctx)
	dump, _, err := m.readStore(ctx, m.lsDir(), t.URL)
	if err != nil {
		return nil, err
	}
	counts := keyCounts(dump)
	var urls []string
	for o := range counts {
		urls = append(urls, o)
	}
	sortOrigins(urls)
	out := make([]Origin, 0, len(urls))
	for _, u := range urls {
		out = append(out, Origin{URL: u, Keys: counts[u]})
	}
	return out, nil
}

// DetectTarget applies D9 (settings.origin / --origin, DEFAULT_PORT, a
// newer fallback-port origin already in the store).
func (m *Manager) DetectTarget(_ context.Context) (Target, error) {
	recency := map[string]time.Time{}
	if storeExists(m.lsDir()) {
		r, err := scanOrigins(m.lsDir())
		if err != nil {
			return Target{}, err
		}
		recency = r
	}
	return m.target(recency)
}

// ---------------------------------------------------------------- backups

// Backup copies Local Storage and the UI *.json files to a new backup with
// a manifest, then prunes to the retention count.
func (m *Manager) Backup(ctx context.Context, reason string) (Backup, error) {
	if err := m.ensureClosed(ctx); err != nil {
		return Backup{}, err
	}
	b, err := m.backup(reason)
	if err != nil {
		return Backup{}, err
	}
	// Key counts are informative: a missing Electron must not stop a backup.
	if m.ready() == nil {
		t, _ := m.DetectTarget(ctx)
		if dump, _, err := m.readStore(ctx, filepath.Join(b.Path, lsDirName), t.URL); err == nil {
			b.Manifest.KeysPerOrigin = keyCounts(dump)
			if err := writeManifest(b.Path, b.Manifest); err != nil {
				return b, err
			}
		}
	}
	if _, err := m.pruneExcept(m.retention(), ""); err != nil {
		return b, err
	}
	return b, nil
}

// backup writes a complete backup (files, marker copy, manifest) into a
// hidden dir and renames it into place, so a crash never leaves a backup
// that looks complete.
func (m *Manager) backup(reason string) (Backup, error) {
	if !storeExists(m.lsDir()) {
		return Backup{}, fmt.Errorf("no settings store at %q", m.lsDir())
	}
	dir := m.backupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Backup{}, fmt.Errorf("create %q: %w", dir, err)
	}
	now := m.deps.Now().UTC()
	id := backupPrefix + now.Format(stampLayout)
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(dir, id)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		id = fmt.Sprintf("%s%s-%d", backupPrefix, now.Format(stampLayout), i)
	}
	tmp := filepath.Join(dir, ".tmp-"+id)
	_ = os.RemoveAll(tmp)
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	files, err := copyTree(m.lsDir(), filepath.Join(tmp, lsDirName), lsDirName+"/")
	if err != nil {
		return Backup{}, err
	}
	jsons, err := uiJSONFiles(m.deps.Install.UserData)
	if err != nil {
		return Backup{}, err
	}
	for _, n := range jsons {
		sum, err := copyFile(filepath.Join(m.deps.Install.UserData, n), filepath.Join(tmp, n))
		if err != nil {
			return Backup{}, fmt.Errorf("back up %q: %w", n, err)
		}
		files[n] = sum
	}
	if b, err := os.ReadFile(filepath.Join(dir, markerFile)); err == nil {
		if err := os.WriteFile(filepath.Join(tmp, markerCopyFile), b, 0o644); err != nil {
			return Backup{}, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Backup{}, fmt.Errorf("read migration marker: %w", err)
	}
	man := Manifest{
		Version: manifestVer, Created: now.Format(time.RFC3339), Reason: reason,
		HermesBefore: m.deps.HermesBefore, HermesAfter: m.deps.HermesAfter,
		Files: files, KeysPerOrigin: map[string]int{},
	}
	if err := writeManifest(tmp, man); err != nil {
		return Backup{}, err
	}
	final := filepath.Join(dir, id)
	if err := fsx.Rename(tmp, final); err != nil {
		return Backup{}, err
	}
	ok = true
	return Backup{ID: id, Path: final, Created: now, Manifest: man}, nil
}

func writeManifest(dir string, man Manifest) error {
	b, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, manifestFile), append(b, '\n'), 0o644)
}

func readBackup(path string) (Backup, error) {
	raw, err := os.ReadFile(filepath.Join(path, manifestFile))
	if err != nil {
		return Backup{}, err
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Backup{}, fmt.Errorf("%q: %w", filepath.Join(path, manifestFile), err)
	}
	if err := validateManifestKeys(man.Files); err != nil {
		return Backup{}, fmt.Errorf("damaged backup %q: %w", filepath.Base(path), err)
	}
	created, _ := time.Parse(time.RFC3339, man.Created)
	return Backup{ID: filepath.Base(path), Path: path, Created: created, Manifest: man}, nil
}

// validManifestKey reports whether k is a relative slash path a backup may
// name: "Local Storage/<rel>" or one top-level "<name>.json". The manifest
// may come from a shared or synced backup directory, so its keys are
// untrusted and must never reach outside userData.
func validManifestKey(k string) bool {
	if k == "" || strings.ContainsAny(k, "\\:\x00") || filepath.VolumeName(k) != "" || path.IsAbs(k) || path.Clean(k) != k {
		return false
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	if rel, ok := strings.CutPrefix(k, lsDirName+"/"); ok {
		return rel != ""
	}
	return !strings.Contains(k, "/") && strings.HasSuffix(k, ".json") && len(k) > len(".json")
}

func validateManifestKeys(files map[string]string) error {
	var bad []string
	for k := range files {
		if !validManifestKey(k) {
			bad = append(bad, fmt.Sprintf("%q", k))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("manifest names paths a backup cannot contain: %s", strings.Join(bad, ", "))
	}
	return nil
}

// within reports whether target is root or lies below it.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ListBackups returns the settings backups, newest first. Directories that
// are not settings backups, or have no readable manifest, are skipped.
func (m *Manager) ListBackups() ([]Backup, error) {
	ents, err := os.ReadDir(m.backupDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", m.backupDir(), err)
	}
	var out []Backup
	for _, e := range ents {
		if !e.IsDir() || !backupIDRe.MatchString(e.Name()) {
			continue
		}
		b, err := readBackup(filepath.Join(m.backupDir(), e.Name()))
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return backupSeq(out[i].ID) > backupSeq(out[j].ID)
	})
	return out, nil
}

// backupSeq orders same-second ids: "x" < "x-2" < "x-10".
func backupSeq(id string) string {
	base, n := id, 1
	if i := strings.LastIndexByte(id, '-'); i > len(backupPrefix)+8 {
		if _, err := fmt.Sscanf(id[i+1:], "%d", &n); err == nil && len(id[i+1:]) < 6 {
			base = id[:i]
		} else {
			n = 1
		}
	}
	return fmt.Sprintf("%s#%06d", base, n)
}

// Prune keeps the newest retention backups.
func (m *Manager) Prune(retention int) (int, error) { return m.pruneExcept(retention, "") }

func (m *Manager) pruneExcept(retention int, keep string) (int, error) {
	if retention < 1 {
		return 0, fmt.Errorf("backup retention must be at least 1 (got %d)", retention)
	}
	bs, err := m.ListBackups()
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for i, b := range bs {
		if i < retention || b.ID == keep {
			continue
		}
		if err := os.RemoveAll(b.Path); err != nil {
			errs = append(errs, fmt.Errorf("remove old backup %s: %w", b.ID, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// ---------------------------------------------------------------- plan

type planned struct {
	plan    Plan
	writes  map[string]string
	dump    map[string]map[string]string
	recency map[string]time.Time
}

// planFrom computes the plan from a userData copy holding Local Storage.
func (m *Manager) planFrom(ctx context.Context, lsSrc string) (planned, error) {
	recency, err := scanOrigins(lsSrc)
	if err != nil {
		return planned{}, err
	}
	tg, err := m.target(recency)
	if err != nil {
		return planned{}, err
	}
	dump, _, err := m.readStore(ctx, lsSrc, tg.URL)
	if err != nil {
		return planned{}, err
	}
	return m.planDump(tg, dump, recency)
}

func (m *Manager) planDump(tg Target, dump map[string]map[string]string, recency map[string]time.Time) (planned, error) {
	mk, err := loadMarker(m.backupDir())
	if err != nil {
		return planned{}, err
	}
	src := chooseSource(tg.URL, keyCounts(dump), recency, mk.targets())
	if src == "" {
		return planned{plan: Plan{Target: tg}, dump: dump, recency: recency}, nil
	}
	if mk.sourceUnchanged(src, tg.URL, dumpHash(dump[src])) {
		return planned{plan: Plan{Source: src, Target: tg, SourceUnchanged: true}, dump: dump, recency: recency}, nil
	}
	p, w := BuildPlan(src, tg, dump[src], dump[tg.URL], RulesFrom(m.deps.Settings), mk.firstRun(src, tg.URL))
	return planned{plan: p, writes: w, dump: dump, recency: recency}, nil
}

// Plan computes the migration on a copy without changing anything. It is
// allowed while Hermes runs (the copy may then be slightly stale).
func (m *Manager) Plan(ctx context.Context) (Plan, error) {
	if !storeExists(m.lsDir()) {
		return Plan{}, nil
	}
	if err := m.ready(); err != nil {
		return Plan{}, err
	}
	pl, err := m.planFrom(ctx, m.lsDir())
	return pl.plan, err
}

// ---------------------------------------------------------------- migrate

func summaryLine(p Plan) string {
	c := p.Counts()
	return fmt.Sprintf("add %d | union %d | old-wins %d | keep-new %d | new-wins %d | same %d | only-new %d | %d conflicts",
		c[ActAdd], c[ActUnion], c[ActOldWins], c[ActKeepNew], c[ActNewWins], c[ActSame], c[ActOnlyInNew], len(Conflicts(p)))
}

// OriginError: the target origin could not be detected. A settings backup was
// still taken (BackupID; empty when that failed too, then Err says why).
type OriginError struct {
	BackupID string
	Cause    error
	Err      error // the backup failure, if any
}

func (e *OriginError) Error() string {
	if e.BackupID == "" {
		return fmt.Sprintf("%v; the settings backup failed too: %v", e.Cause, e.Err)
	}
	return fmt.Sprintf("%v; settings backup %s was taken (undo any later change with: hermes-safe-update revert-settings --backup %s); "+
		"migrate by hand with Hermes closed: hermes-safe-update migrate-settings --origin <url> (try --dry-run first)",
		e.Cause, e.BackupID, e.BackupID)
}

func (e *OriginError) Unwrap() error { return e.Cause }

// backupIfOriginUnknown: when the target origin cannot be detected (and no
// --origin was given) the run cannot migrate, but a desktop-origin-sensitive
// update must never leave the user without a backup, so take one first.
func (m *Manager) backupIfOriginUnknown() (Report, bool, error) {
	if m.deps.Settings.Origin != "" {
		return Report{}, false, nil
	}
	_, derr := defaultPortOrigin(m.deps.Install.Checkout)
	if derr == nil {
		return Report{}, false, nil
	}
	oe := &OriginError{Cause: derr}
	b, berr := m.backup("origin not detected")
	if berr != nil {
		oe.Err = berr
		return Report{}, true, oe
	}
	oe.BackupID = b.ID
	return Report{BackupID: b.ID}, true, oe
}

// Migrate: refuse while Hermes runs -> plan on a read copy (no backup when
// there is nothing to write, so automatic no-op runs never prune the
// pre-migration backup) -> backup -> copy -> Electron write on the copy ->
// re-plan in a fresh process (no writes left) -> swap -> verify the live
// store on a fresh copy -> automatic rollback on any failure.
func (m *Manager) Migrate(ctx context.Context) (Report, error) {
	if err := m.ensureClosed(ctx); err != nil {
		return Report{}, err
	}
	if !storeExists(m.lsDir()) {
		return Report{Line: "settings: no settings store yet; nothing to migrate"}, nil
	}
	if rep, failed, err := m.backupIfOriginUnknown(); failed {
		return rep, err
	}
	if err := m.ready(); err != nil {
		return Report{}, err
	}
	pre, err := m.planFrom(ctx, m.lsDir())
	if err != nil {
		return Report{}, err
	}
	if rep, done, err := m.noWrites(pre); done {
		return rep, err
	}

	b, err := m.backup("before migration")
	if err != nil {
		return Report{}, err
	}
	rep := Report{BackupID: b.ID}
	if _, err := m.pruneExcept(m.retention(), b.ID); err != nil {
		return rep, err
	}

	// Work copy next to the live store (same volume, so the swap is a rename),
	// made from the backup so the plan matches what was backed up.
	work, err := workCopy(filepath.Join(b.Path, lsDirName), m.deps.Install.UserData, "work")
	if err != nil {
		return rep, err
	}
	defer func() { _ = removeAll(work); m.cleanWorkDir() }()
	workLS := filepath.Join(work, lsDirName)

	pl, err := m.planFrom(ctx, workLS)
	if err != nil {
		return rep, err
	}
	b.Manifest.KeysPerOrigin = keyCounts(pl.dump)
	if err := writeManifest(b.Path, b.Manifest); err != nil {
		return rep, err
	}
	if r, done, err := m.noWrites(pl); done {
		// the store changed between the read and the backup (Hermes is
		// closed, so unexpected); keep the backup and report it
		r.BackupID = b.ID
		return r, err
	}
	rep.Plan, rep.Conflicts = pl.plan, Conflicts(pl.plan)

	// 1. write on the copy (verified in-process by the migrator)
	if err := m.io().Write(ctx, work, pl.plan.Target.URL, pl.writes); err != nil {
		return rep, fmt.Errorf("migrate the copy: %w; live settings unchanged (backup %s)", err, b.ID)
	}
	// 2. re-plan in a fresh process: nothing may be left to write
	if err := m.replanClean(ctx, workLS, pl); err != nil {
		return rep, fmt.Errorf("%w; live settings unchanged (backup %s)", err, b.ID)
	}
	// 3. swap (Hermes must still be closed)
	if err := m.ensureClosed(ctx); err != nil {
		return rep, err
	}
	old, err := m.swapIn(workLS)
	if err != nil {
		return rep, fmt.Errorf("%w; live settings unchanged (backup %s)", err, b.ID)
	}
	// 4. verify the live store on a fresh copy; roll back on any failure
	if verr := m.replanClean(ctx, m.lsDir(), pl); verr != nil {
		if rerr := m.swapBack(old); rerr != nil {
			return rep, fmt.Errorf("verify live settings: %v; ROLLBACK FAILED: %v; restore with: revert-settings --backup %s", verr, rerr, b.ID)
		}
		return rep, fmt.Errorf("verify live settings: %w; rolled back, live settings unchanged (backup %s)", verr, b.ID)
	}
	_ = removeAll(old)
	rep.Changed = true
	if err := m.recordMarker(pl); err != nil {
		return rep, err
	}
	rep.Line = fmt.Sprintf("settings: migrated %s -> %s (%s); backup %s", pl.plan.Source, pl.plan.Target.URL, summaryLine(pl.plan), b.ID)
	return rep, nil
}

// noWrites ends a run whose plan has nothing to write (done=true): no
// source, a source unchanged since its last migration, or a source whose
// every key is already in the target (recorded, so later runs see an
// unchanged source).
func (m *Manager) noWrites(pl planned) (Report, bool, error) {
	rep := Report{Plan: pl.plan, Conflicts: Conflicts(pl.plan)}
	switch {
	case pl.plan.Source == "":
		rep.Line = fmt.Sprintf("settings: nothing to migrate (only %s holds keys)", pl.plan.Target.URL)
	case pl.plan.SourceUnchanged:
		rep.Line = fmt.Sprintf("settings: %s unchanged since the last migration to %s; nothing to write", pl.plan.Source, pl.plan.Target.URL)
	case len(pl.writes) == 0:
		rep.Line = fmt.Sprintf("settings: already migrated %s -> %s (%s)", pl.plan.Source, pl.plan.Target.URL, summaryLine(pl.plan))
		return rep, true, m.recordMarker(pl)
	default:
		return Report{}, false, nil
	}
	return rep, true, nil
}

// replanClean dumps lsSrc in a fresh migrator process and requires the same
// source/target plan to have nothing left to write.
func (m *Manager) replanClean(ctx context.Context, lsSrc string, first planned) error {
	dump, _, err := m.readStore(ctx, lsSrc, first.plan.Target.URL)
	if err != nil {
		return err
	}
	if dump[first.plan.Source] == nil || dump[first.plan.Target.URL] == nil {
		return fmt.Errorf("%w: origin missing after migration", ErrNotClean)
	}
	p, w := BuildPlan(first.plan.Source, first.plan.Target, dump[first.plan.Source], dump[first.plan.Target.URL],
		RulesFrom(m.deps.Settings), first.plan.FirstRun)
	if len(w) != 0 {
		var keys []string
		for _, k := range p.Keys {
			if _, ok := w[k.Key]; ok {
				keys = append(keys, k.Key)
			}
		}
		return fmt.Errorf("%w: %d key(s) still differ: %s", ErrNotClean, len(w), strings.Join(keys, ", "))
	}
	return nil
}

func (m *Manager) recordMarker(pl planned) error {
	mk, err := loadMarker(m.backupDir())
	if err != nil {
		return err
	}
	mk.record(pl.plan.Source, pl.plan.Target.URL, dumpHash(pl.dump[pl.plan.Source]), m.deps.Now())
	return mk.save(m.backupDir())
}

// swapIn moves the live Local Storage aside and newLS into its place. On
// failure the live store is put back. It returns the moved-aside path.
func (m *Manager) swapIn(newLS string) (string, error) {
	live := m.lsDir()
	old := filepath.Join(m.deps.Install.UserData, tmpPrefix+"old-"+randSuffix())
	if err := fsx.Rename(live, old); err != nil {
		return "", fmt.Errorf("move live settings aside: %w", err)
	}
	if err := fsx.Rename(newLS, live); err != nil {
		if rerr := fsx.Rename(old, live); rerr != nil {
			return "", fmt.Errorf("swap in: %v; putting the old store back failed: %w", err, rerr)
		}
		return "", fmt.Errorf("swap in: %w", err)
	}
	return old, nil
}

// swapBack replaces the live store with old (the moved-aside original).
func (m *Manager) swapBack(old string) error {
	live := m.lsDir()
	failed := filepath.Join(m.deps.Install.UserData, tmpPrefix+"failed-"+randSuffix())
	if err := fsx.Rename(live, failed); err != nil {
		return err
	}
	if err := fsx.Rename(old, live); err != nil {
		_ = fsx.Rename(failed, live)
		return err
	}
	return os.RemoveAll(failed)
}

// ---------------------------------------------------------------- revert

// Revert restores backup id ("" = newest) with Hermes closed: it verifies
// the backup's hashes first, takes a "before revert" backup, swaps the
// store in, restores the UI json files and the migration marker, and
// verifies the live hashes (rolling back on a mismatch).
func (m *Manager) Revert(ctx context.Context, id string) (Report, error) {
	if id != "" && !backupIDRe.MatchString(id) {
		return Report{}, fmt.Errorf("%w: %q is not a settings backup id (see list-backups)", apperr.ErrUsage, id)
	}
	if err := m.ensureClosed(ctx); err != nil {
		return Report{}, err
	}
	var src Backup
	if id == "" {
		bs, err := m.ListBackups()
		if err != nil {
			return Report{}, err
		}
		if len(bs) == 0 {
			return Report{}, errors.New("no settings backups to restore")
		}
		src = bs[0]
	} else {
		b, err := readBackup(filepath.Join(m.backupDir(), id))
		if err != nil {
			return Report{}, fmt.Errorf("settings backup %s: %w (nothing restored)", id, err)
		}
		src = b
	}
	if err := verifyFiles(src.Path, src.Manifest.Files); err != nil {
		return Report{}, fmt.Errorf("backup %s is damaged, nothing restored: %w", src.ID, err)
	}

	// stage the backup's store next to the live one and check it
	stage, err := os.MkdirTemp(m.deps.Install.UserData, tmpPrefix+"restore-")
	if err != nil {
		return Report{}, fmt.Errorf("stage restore: %w", err)
	}
	defer func() { _ = removeAll(stage) }()
	if _, err := copyTree(filepath.Join(src.Path, lsDirName), filepath.Join(stage, lsDirName), lsDirName+"/"); err != nil {
		return Report{}, err
	}
	if err := verifyFiles(stage, onlyLS(src.Manifest.Files)); err != nil {
		return Report{}, fmt.Errorf("staged copy differs from backup %s: %w", src.ID, err)
	}

	pre, err := m.backup("before revert")
	if err != nil {
		return Report{}, err
	}
	rep := Report{BackupID: src.ID}
	if err := m.ensureClosed(ctx); err != nil {
		return rep, err
	}
	old, err := m.swapIn(filepath.Join(stage, lsDirName))
	if err != nil {
		return rep, err
	}
	rollback := func(cause error) error {
		var errs []error
		if err := m.swapBack(old); err != nil {
			errs = append(errs, err)
		}
		if err := restoreJSON(pre.Path, m.deps.Install.UserData, pre.Manifest.Files); err != nil {
			errs = append(errs, err)
		}
		if err := errors.Join(errs...); err != nil {
			return fmt.Errorf("%v; ROLLBACK FAILED: %v; restore with: revert-settings --backup %s", cause, err, pre.ID)
		}
		return fmt.Errorf("%w; rolled back (backup of the state before: %s)", cause, pre.ID)
	}
	if err := restoreJSON(src.Path, m.deps.Install.UserData, src.Manifest.Files); err != nil {
		return rep, rollback(err)
	}
	if err := verifyFiles(m.deps.Install.UserData, src.Manifest.Files); err != nil {
		return rep, rollback(fmt.Errorf("verify restored settings: %w", err))
	}
	_ = removeAll(old)
	if err := restoreMarker(src.Path, m.backupDir()); err != nil {
		return rep, err
	}
	if _, err := m.pruneExcept(m.retention(), src.ID); err != nil {
		return rep, err
	}
	rep.Changed = true
	rep.Line = fmt.Sprintf("settings: restored backup %s (%d files verified); the state before is in %s", src.ID, len(src.Manifest.Files), pre.ID)
	return rep, nil
}

func onlyLS(files map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		if strings.HasPrefix(k, lsDirName+"/") {
			out[k] = v
		}
	}
	return out
}

// verifyFiles checks every relative path's sha256 under root, and that no
// extra files exist in root's Local Storage (LOCK aside).
func verifyFiles(root string, files map[string]string) error {
	var bad []string
	for rel, want := range files {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if !within(root, target) {
			bad = append(bad, rel)
			continue
		}
		got, err := fileSum(target)
		if err != nil || got != want {
			bad = append(bad, rel)
		}
	}
	ls := filepath.Join(root, lsDirName)
	_ = filepath.WalkDir(ls, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == lockFile {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if _, ok := files[filepath.ToSlash(rel)]; !ok {
			bad = append(bad, "+"+filepath.ToSlash(rel))
		}
		return nil
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("hash mismatch: %s", strings.Join(bad, ", "))
	}
	return nil
}

// restoreJSON writes the backed-up top-level UI json files back atomically.
// UI json files that did not exist at backup time are left alone.
func restoreJSON(from, userData string, files map[string]string) error {
	for rel := range files {
		if strings.Contains(rel, "/") {
			continue
		}
		target := filepath.Join(userData, rel)
		if filepath.Base(rel) != rel || filepath.Ext(rel) != ".json" || !within(userData, target) {
			return fmt.Errorf("restore %q: not a top-level json file inside userData", rel)
		}
		b, err := os.ReadFile(filepath.Join(from, rel))
		if err != nil {
			return fmt.Errorf("restore %s: %w", rel, err)
		}
		if err := fsx.WriteFileAtomic(target, b, 0o644); err != nil {
			return fmt.Errorf("restore %s: %w", rel, err)
		}
	}
	return nil
}

// restoreMarker puts the migration marker back to its state at backup time
// (absent then = remove now), so the next migration decides old-wins vs
// new-wins as it would have then.
func restoreMarker(backupPath, dir string) error {
	b, err := os.ReadFile(filepath.Join(backupPath, markerCopyFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Remove(filepath.Join(dir, markerFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("reset migration marker: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("read marker copy: %w", err)
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, markerFile), b, 0o644)
}
