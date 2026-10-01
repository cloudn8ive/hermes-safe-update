package settings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// argOf returns the value of --name= in argv.
func argOf(argv []string, name string) string {
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return v
		}
	}
	return ""
}

func newTestElectron(t *testing.T, f *execx.Fake) *electronIO {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "electron.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &electronIO{runner: f, electron: exe, workDir: t.TempDir(), timeout: time.Minute}
}

func TestElectronDumpRunsScriptAndParsesOutput(t *testing.T) {
	f := &execx.Fake{}
	var got execx.Cmd
	var scriptBody []byte
	f.OnFunc(nil, func(c execx.Cmd) execx.Result {
		got = c
		scriptBody, _ = os.ReadFile(c.Argv[1])
		out := map[string]any{"nonce": argOf(c.Argv, "nonce"), "origins": map[string]map[string]string{
			"file://": {"a": "1"}, "http://127.0.0.1:47891": {},
		}}
		b, _ := json.Marshal(out)
		_ = os.WriteFile(filepath.Join(argOf(c.Argv, "out"), "dump.json"), b, 0o644)
		return execx.Result{Code: 0, Output: "dump ok"}
	})
	e := newTestElectron(t, f)
	ud := t.TempDir()
	dump, err := e.Dump(context.Background(), ud, []string{"file://", "http://127.0.0.1:47891"})
	if err != nil {
		t.Fatal(err)
	}
	if dump["file://"]["a"] != "1" || dump["http://127.0.0.1:47891"] == nil {
		t.Errorf("dump = %v", dump)
	}
	if got.Argv[0] != e.electron {
		t.Errorf("argv[0] = %q", got.Argv[0])
	}
	if !strings.Contains(string(scriptBody), "hermes-safe-update settings migrator") {
		t.Errorf("script %q not written from MigratorFiles", got.Argv[1])
	}
	if argOf(got.Argv, "mode") != "dump" || argOf(got.Argv, "user-data") != filepath.ToSlash(ud) ||
		argOf(got.Argv, "origins") != "file://,http://127.0.0.1:47891" {
		t.Errorf("argv = %q", got.Argv)
	}
	if got.Timeout != time.Minute {
		t.Errorf("timeout = %v", got.Timeout)
	}
	if _, err := os.Stat(argOf(got.Argv, "out")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("out dir (holds values) not removed: %v", err)
	}
}

func TestElectronFailures(t *testing.T) {
	tests := []struct {
		name string
		fn   func(c execx.Cmd) execx.Result
		want string
	}{
		{"non-zero exit", func(c execx.Cmd) execx.Result { return execx.Result{Code: 1, Output: "migrator: ERROR boom"} }, "boom"},
		{"timeout", func(c execx.Cmd) execx.Result { return execx.Result{Code: 124, TimedOut: true, Output: "timed out"} }, "timed out"},
		{"no output file", func(c execx.Cmd) execx.Result { return execx.Result{Code: 0} }, "dump.json"},
		{"stale output from another run", func(c execx.Cmd) execx.Result {
			_ = os.WriteFile(filepath.Join(argOf(c.Argv, "out"), "dump.json"), []byte(`{"nonce":"other","origins":{}}`), 0o644)
			return execx.Result{Code: 0}
		}, "nonce"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &execx.Fake{}
			f.OnFunc(nil, tc.fn)
			e := newTestElectron(t, f)
			_, err := e.Dump(context.Background(), t.TempDir(), []string{"file://"})
			if err == nil || !errors.Is(err, ErrMigrator) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want ErrMigrator containing %q", err, tc.want)
			}
		})
	}
}

func TestElectronMissing(t *testing.T) {
	e := &electronIO{runner: &execx.Fake{}, electron: filepath.Join(t.TempDir(), "nope.exe"), workDir: t.TempDir()}
	if _, err := e.Dump(context.Background(), t.TempDir(), nil); !errors.Is(err, ErrElectronMissing) {
		t.Errorf("err = %v", err)
	}
	e.electron = ""
	if err := e.Write(context.Background(), t.TempDir(), "x", nil); !errors.Is(err, ErrElectronMissing) {
		t.Errorf("err = %v", err)
	}
}

func TestElectronWritePassesValuesByFileAndChecksMismatches(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		f := &execx.Fake{}
		var in map[string]string
		f.OnFunc(nil, func(c execx.Cmd) execx.Result {
			b, _ := os.ReadFile(argOf(c.Argv, "in"))
			_ = json.Unmarshal(b, &in)
			res := map[string]any{"nonce": argOf(c.Argv, "nonce"), "written": len(in), "mismatches": []string{}}
			code := 0
			if mismatch {
				res["mismatches"] = []string{"k"}
				code = 1
			}
			b, _ = json.Marshal(res)
			_ = os.WriteFile(filepath.Join(argOf(c.Argv, "out"), "write.json"), b, 0o644)
			if argOf(c.Argv, "mode") != "write" || argOf(c.Argv, "origin") != "http://127.0.0.1:47891" {
				code = 2
			}
			for _, a := range c.Argv {
				if strings.Contains(a, "secret-value") {
					code = 3 // values must never be on the command line
				}
			}
			return execx.Result{Code: code}
		})
		e := newTestElectron(t, f)
		err := e.Write(context.Background(), t.TempDir(), "http://127.0.0.1:47891", map[string]string{"k": "secret-value"})
		if mismatch {
			if !errors.Is(err, ErrMigrator) || !strings.Contains(err.Error(), "k") {
				t.Errorf("mismatch: err = %v", err)
			}
			continue
		}
		if err != nil || in["k"] != "secret-value" {
			t.Errorf("err = %v, in = %v", err, in)
		}
	}
}

func TestElectronRefusesRelativePathsBeforeRunning(t *testing.T) {
	for _, ud := range []string{"rel/userData", "./x", "/c/not/absolute/on/windows/or/ok/on/unix"} {
		if filepath.IsAbs(ud) {
			continue // a rooted path is legitimate on this OS
		}
		f := &execx.Fake{}
		e := newTestElectron(t, f)
		_, err := e.Dump(context.Background(), ud, []string{"file://"})
		if err == nil || !errors.Is(err, ErrMigrator) || !strings.Contains(err.Error(), "absolute") {
			t.Errorf("%q: err = %v, want ErrMigrator mentioning absolute", ud, err)
		}
		if len(f.Calls()) != 0 {
			t.Errorf("%q: migrator was started with a relative path", ud)
		}
	}
	e := newTestElectron(t, &execx.Fake{})
	if err := e.Write(context.Background(), "rel", "file://", map[string]string{"a": "b"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("Write relative: err = %v", err)
	}
}

func TestElectronFailureSurfacesMigratorLineFirst(t *testing.T) {
	f := &execx.Fake{}
	f.OnFunc(nil, func(c execx.Cmd) execx.Result {
		return execx.Result{Code: 2, Output: "[1234:ERR] noise from chromium\r\nmigrator: Error: Path must be absolute\r\n    at stack line\r\n"}
	})
	e := newTestElectron(t, f)
	_, err := e.Dump(context.Background(), t.TempDir(), []string{"file://"})
	if err == nil || !strings.Contains(err.Error(), "exited 2: migrator: Error: Path must be absolute") {
		t.Errorf("err = %v", err)
	}
}
