package settings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

var (
	// ErrElectronMissing: Hermes' bundled Electron was not found, so the
	// store cannot be read or written safely. Nothing is changed.
	ErrElectronMissing = errors.New("the bundled Electron of Hermes was not found")
	// ErrMigrator: the migrator failed, timed out or produced no valid result.
	ErrMigrator = errors.New("settings migrator failed")
)

// storeIO reads and writes localStorage of a userData COPY. The real
// implementation runs the embedded migrator on Hermes' Electron; tests use a
// fake.
type storeIO interface {
	// Dump returns origin -> key -> value for each requested origin.
	Dump(ctx context.Context, userData string, origins []string) (map[string]map[string]string, error)
	// Write sets writes under origin, flushes and verifies in-process.
	Write(ctx context.Context, userData, origin string, writes map[string]string) error
}

// electronIO runs migrator/main.js with Hermes' bundled Electron. Every run
// gets a fresh process (the re-plan must run in a fresh process) and a
// fresh output dir that is removed afterwards (it holds values).
type electronIO struct {
	runner   execx.Runner
	electron string
	workDir  string // private scratch dir for scripts and outputs
	timeout  time.Duration
}

func (e *electronIO) check() error {
	if e.electron == "" {
		return fmt.Errorf("%w (no Electron path; is the desktop app built?)", ErrElectronMissing)
	}
	if st, err := os.Stat(e.electron); err != nil || st.IsDir() {
		return fmt.Errorf("%w at %s", ErrElectronMissing, e.electron)
	}
	return nil
}

// writeScripts copies the embedded migrator into dir.
func writeScripts(dir string) (string, error) {
	files := MigratorFiles()
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("write migrator script: %w", err)
	}
	return filepath.Join(dir, "main.js"), nil
}

func nonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// run executes one migrator mode and decodes <out>/<result> into v. The
// nonce in the result proves it came from this run, not a stale file.
func (e *electronIO) run(ctx context.Context, userData, mode, result string, extra []string, v any) error {
	if err := e.check(); err != nil {
		return err
	}
	if !filepath.IsAbs(userData) {
		return fmt.Errorf("%w: %s: user data path %q is not absolute", ErrMigrator, mode, userData)
	}
	if err := os.MkdirAll(e.workDir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", e.workDir, err)
	}
	dir, err := os.MkdirTemp(e.workDir, "mig-")
	if err != nil {
		return fmt.Errorf("create migrator dir: %w", err)
	}
	defer func() { _ = removeAll(dir) }()
	if dir, err = filepath.Abs(dir); err != nil { // a relative workDir must not reach the migrator
		return fmt.Errorf("resolve migrator dir: %w", err)
	}
	script, err := writeScripts(filepath.Join(dir, "js"))
	if err != nil {
		return err
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	n := nonce()
	timeout := e.timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	argv := append([]string{e.electron, script,
		"--user-data=" + filepath.ToSlash(userData),
		"--out=" + filepath.ToSlash(out),
		"--nonce=" + n,
		"--mode=" + mode,
		fmt.Sprintf("--watchdog-ms=%d", (timeout - 5*time.Second).Milliseconds()),
	}, extra...)
	res, err := e.runner.Run(ctx, execx.Cmd{Argv: argv, Dir: dir, Timeout: timeout})
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrMigrator, mode, err)
	}
	b, rerr := os.ReadFile(filepath.Join(out, result))
	if res.Code != 0 {
		detail := migratorDetail(res.Output)
		if res.TimedOut {
			detail = "timed out after " + timeout.String() + "; " + detail
		}
		if rerr == nil && mode == "write" {
			// keep the mismatch list (key names only) from write.json
			var w writeResult
			if json.Unmarshal(b, &w) == nil && len(w.Mismatches) > 0 {
				detail = "verify mismatches: " + strings.Join(w.Mismatches, ", ")
			}
		}
		return fmt.Errorf("%w: %s exited %d: %s", ErrMigrator, mode, res.Code, detail)
	}
	if rerr != nil {
		return fmt.Errorf("%w: %s produced no %s", ErrMigrator, mode, result)
	}
	var hdr struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(b, &hdr); err != nil || hdr.Nonce != n {
		return fmt.Errorf("%w: %s: %s has no matching nonce (stale or foreign output)", ErrMigrator, mode, result)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %s: bad %s: %v", ErrMigrator, mode, result, err)
	}
	return nil
}

// migratorDetail picks the most useful part of a failed run's output: the
// first "migrator:" line (the script's own message) when there is one, else
// the tail of the output.
func migratorDetail(output string) string {
	for _, l := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "migrator:") {
			return execx.Tail(l, 300)
		}
	}
	return execx.Tail(output, 300)
}

func (e *electronIO) Dump(ctx context.Context, userData string, origins []string) (map[string]map[string]string, error) {
	var d struct {
		Origins map[string]map[string]string `json:"origins"`
	}
	if err := e.run(ctx, userData, "dump", "dump.json", []string{"--origins=" + strings.Join(origins, ",")}, &d); err != nil {
		return nil, err
	}
	for _, o := range origins {
		if d.Origins[o] == nil {
			return nil, fmt.Errorf("%w: dump is missing origin %s", ErrMigrator, o)
		}
	}
	return d.Origins, nil
}

type writeResult struct {
	Written    int      `json:"written"`
	Mismatches []string `json:"mismatches"`
}

func (e *electronIO) Write(ctx context.Context, userData, origin string, writes map[string]string) error {
	if err := e.check(); err != nil {
		return err
	}
	if !filepath.IsAbs(userData) {
		return fmt.Errorf("%w: write: user data path %q is not absolute", ErrMigrator, userData)
	}
	if err := os.MkdirAll(e.workDir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", e.workDir, err)
	}
	in, err := os.CreateTemp(e.workDir, "writes-*.json")
	if err != nil {
		return fmt.Errorf("stage writes: %w", err)
	}
	defer os.Remove(in.Name())
	if err := json.NewEncoder(in).Encode(writes); err != nil {
		_ = in.Close()
		return fmt.Errorf("stage writes: %w", err)
	}
	if err := in.Close(); err != nil {
		return fmt.Errorf("stage writes: %w", err)
	}
	var w writeResult
	if err := e.run(ctx, userData, "write", "write.json",
		[]string{"--origin=" + origin, "--in=" + filepath.ToSlash(in.Name())}, &w); err != nil {
		return err
	}
	if len(w.Mismatches) > 0 || w.Written != len(writes) {
		return fmt.Errorf("%w: wrote %d of %d keys; verify mismatches: %s", ErrMigrator, w.Written, len(writes), strings.Join(w.Mismatches, ", "))
	}
	return nil
}
