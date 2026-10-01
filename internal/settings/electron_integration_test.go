//go:build electron

// Integration test: runs the real migrator on Hermes' bundled Electron
// against a synthetic userData in t.TempDir() (never the real userData).
//
//	HSU_ELECTRON=<path to electron(.exe)> go test -tags electron ./internal/settings -run Electron -count=1 -v
//
// Without HSU_ELECTRON it looks for the default install's Electron and skips
// when there is none.
package settings

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func findElectron(t *testing.T) string {
	if p := os.Getenv("HSU_ELECTRON"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("LOCALAPPDATA"), "hermes", "hermes-agent", "apps", "desktop",
			"node_modules", "electron", "dist", "electron.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no Electron (set HSU_ELECTRON)")
	return ""
}

func TestElectronEndToEnd(t *testing.T) {
	electron := findElectron(t)
	ctx := context.Background()
	root := t.TempDir()
	ud := filepath.Join(root, "userData")
	home := filepath.Join(root, "home")
	checkout := filepath.Join(home, "hermes-agent")
	rs := filepath.Join(checkout, filepath.FromSlash(hermes.RendererServerSource))
	must(t, os.MkdirAll(filepath.Dir(rs), 0o755))
	must(t, os.WriteFile(rs, []byte("const DEFAULT_PORT = 47891\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(ud, "Local Storage", "leveldb"), 0o755))
	must(t, os.WriteFile(filepath.Join(ud, "window-state.json"), []byte(`{"x":1}`), 0o644))

	// Seed the synthetic store through the same migrator (Chromium writes it).
	seed := &electronIO{runner: execx.New(), electron: electron, workDir: filepath.Join(root, "seedwork"), timeout: 90 * time.Second}
	must(t, seed.Write(ctx, ud, "file://", map[string]string{
		"hermes.desktop.theme":             "dark",
		"plain":                            "old",
		"hermes.desktop.freshDraftKey":     "d-old",
		"hermes.desktop.sessionSeenCounts": `{"s1":1}`,
		"unicode ✓ key":                    "värde ✓",
	}))
	must(t, seed.Write(ctx, ud, "http://127.0.0.1:47891", map[string]string{
		"plain":                            "new",
		"hermes.desktop.freshDraftKey":     "d-new",
		"hermes.desktop.sessionSeenCounts": `{"s2":2}`,
	}))

	svc := New(Deps{
		Install:         hermes.Install{Home: home, Checkout: checkout, UserData: ud, Electron: electron},
		Settings:        config.Defaults().Settings,
		Procs:           platform.NewFakeProcs(), // never look at (or touch) real processes
		ElectronTimeout: 90 * time.Second,
	})

	origins, err := svc.DetectOrigins(ctx)
	must(t, err)
	if !reflect.DeepEqual(origins, []Origin{{URL: "file://", Keys: 5}, {URL: "http://127.0.0.1:47891", Keys: 3}}) {
		t.Fatalf("origins = %+v", origins)
	}
	plan, err := svc.Plan(ctx)
	must(t, err)
	if plan.Writes() != 4 || !plan.FirstRun {
		t.Fatalf("plan writes = %d, first = %v", plan.Writes(), plan.FirstRun)
	}

	before := snapshot(t, ud)
	start := time.Now()
	rep, err := svc.Migrate(ctx)
	must(t, err)
	t.Logf("migrate took %s: %s", time.Since(start).Round(time.Millisecond), rep.Line)
	if !rep.Changed {
		t.Fatalf("not changed: %+v", rep)
	}
	reader := &electronIO{runner: execx.New(), electron: electron, workDir: filepath.Join(root, "readwork"), timeout: 90 * time.Second}
	cp, err := workCopy(filepath.Join(ud, "Local Storage"), filepath.Join(root, "check"), "c")
	must(t, err)
	dump, err := reader.Dump(ctx, cp, []string{"http://127.0.0.1:47891"})
	must(t, err)
	want := map[string]string{
		"hermes.desktop.theme":             "dark",
		"plain":                            "old",
		"hermes.desktop.freshDraftKey":     "d-new",
		"hermes.desktop.sessionSeenCounts": `{"s1":1,"s2":2}`,
		"unicode ✓ key":                    "värde ✓",
	}
	if !reflect.DeepEqual(dump["http://127.0.0.1:47891"], want) {
		t.Errorf("live new origin = %v", dump["http://127.0.0.1:47891"])
	}

	// idempotent second run: source unchanged, no writes, no backup
	rep2, err := svc.Migrate(ctx)
	must(t, err)
	if rep2.Changed || rep2.Plan.Writes() != 0 || rep2.BackupID != "" || !strings.Contains(rep2.Line, "unchanged since the last migration") {
		t.Errorf("second run = %+v", rep2)
	}

	// the user removes a migrated key in the new origin: a later run must
	// not bring it back from the unchanged file:// origin
	must(t, seed.Write(ctx, ud, "http://127.0.0.1:47891", map[string]string{"hermes.desktop.sessionSeenCounts": `{"s2":2}`}))
	rep3, err := svc.Migrate(ctx)
	must(t, err)
	if rep3.Changed {
		t.Errorf("third run re-unioned from an unchanged source: %+v", rep3)
	}
	if bs, _ := svc.ListBackups(); len(bs) != 1 {
		t.Errorf("no-op runs took backups: %d", len(bs))
	}

	// revert to the pre-migration backup: byte-identical within the backup
	// scope (the direct seed write above made Chromium add cache dirs such
	// as GPUCache next to the store; those are not settings)
	_, err = svc.Revert(ctx, rep.BackupID)
	must(t, err)
	assertSame(t, settingsScope(before), settingsScope(snapshot(t, ud)))
	t.Logf("revert to %s byte-identical: ok", rep.BackupID)

	// nothing left in userData besides the store and the json file
	ents, _ := os.ReadDir(ud)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

// settingsScope keeps what a settings backup covers: Local Storage and the
// top-level UI *.json files.
func settingsScope(snap map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range snap {
		if strings.HasPrefix(k, "Local Storage/") || (!strings.Contains(k, "/") && strings.HasSuffix(k, ".json")) {
			out[k] = v
		}
	}
	return out
}

func TestElectronInheritedRunAsNodeIsHandled(t *testing.T) {
	electron := findElectron(t)
	t.Setenv("ELECTRON_RUN_AS_NODE", "1")
	root := t.TempDir()
	ud := filepath.Join(root, "ud")
	must(t, os.MkdirAll(filepath.Join(ud, "Local Storage", "leveldb"), 0o755))
	e := &electronIO{runner: execx.New(), electron: electron, workDir: filepath.Join(root, "w"), timeout: 90 * time.Second}
	must(t, e.Write(context.Background(), ud, "file://", map[string]string{"k": "v"}))
	d, err := e.Dump(context.Background(), ud, []string{"file://"})
	must(t, err)
	if d["file://"]["k"] != "v" {
		t.Errorf("dump = %v", d)
	}
}

// A bad --user-data must make the migrator exit 2 by itself. Electron's
// default uncaught-exception handler shows a modal native error dialog, which
// would block the process (and the user's desktop) until the deadline.
func TestElectronBadUserDataExitsWithoutDialog(t *testing.T) {
	electron := findElectron(t)
	dir := t.TempDir()
	script, err := writeScripts(filepath.Join(dir, "js"))
	must(t, err)
	out := filepath.Join(dir, "out")
	must(t, os.MkdirAll(out, 0o755))
	// Relative to the migrator's cwd (dir) and it exists: this is the case
	// that used to reach app.setPath and pop the native dialog.
	must(t, os.MkdirAll(filepath.Join(dir, "ud", "Local Storage", "leveldb"), 0o755))
	for name, ud := range map[string]string{
		"relative existing": "ud",
		"relative":          "relative/userData",
		"msys style":        "/c/definitely/not/here",
		"missing dir":       filepath.Join(dir, "no-such-dir"),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res, err := execx.New().Run(ctx, execx.Cmd{
				Argv:    []string{electron, script, "--user-data=" + ud, "--out=" + out, "--nonce=n", "--mode=dump", "--origins=file://"},
				Dir:     dir,
				Timeout: 10 * time.Second,
			})
			must(t, err)
			if res.TimedOut || ctx.Err() != nil {
				t.Fatalf("migrator did not exit by itself (a dialog is probably blocking it): %+v", res)
			}
			if res.Code != 2 || !strings.Contains(res.Output, "migrator:") {
				t.Errorf("code = %d, output = %q; want exit 2 with a migrator: message", res.Code, res.Output)
			}
		})
	}
}
