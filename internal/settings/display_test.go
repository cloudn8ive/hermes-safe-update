package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

func displayIO(t *testing.T, goos string, env map[string]string) (*electronIO, *execx.Fake) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "electron")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{}
	return &electronIO{runner: f, electron: exe, workDir: t.TempDir(), goos: goos,
		getenv: func(k string) string { return env[k] }}, f
}

// Electron aborts on Linux without DISPLAY/WAYLAND_DISPLAY (SSH, cron,
// services). Say so up front, before a backup and before starting it.
func TestElectronOnLinuxWithoutADisplaySaysWhy(t *testing.T) {
	e, f := displayIO(t, "linux", map[string]string{"HOME": "/home/u"})
	err := e.check()
	if !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("check = %v, want ErrNoDisplay", err)
	}
	for _, want := range []string{"DISPLAY", "WAYLAND_DISPLAY", "desktop session"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	if _, err := e.Dump(context.Background(), t.TempDir(), nil); !errors.Is(err, ErrNoDisplay) {
		t.Errorf("Dump = %v, want ErrNoDisplay", err)
	}
	if c := f.Calls(); len(c) != 0 {
		t.Errorf("started Electron anyway: %v", c)
	}
}

func TestElectronDisplayCheckOnlyWhereItApplies(t *testing.T) {
	for _, c := range []struct {
		name string
		goos string
		env  map[string]string
	}{
		{"X11", "linux", map[string]string{"DISPLAY": ":0"}},
		{"Wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}},
		{"macOS has no DISPLAY and does not need one", "darwin", nil},
		{"Windows", "windows", nil},
		{"no OS given (tests, old callers)", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := displayIO(t, c.goos, c.env)
			if err := e.check(); err != nil {
				t.Errorf("check = %v, want nil", err)
			}
		})
	}
}
