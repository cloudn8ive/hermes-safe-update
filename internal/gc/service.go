package gc

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// Script is the embedded collector wrapper (run with Hermes' managed Python).
//
//go:embed script/safe_update_gc.py
var Script string

// ErrOutputChanged means the script ran but printed nothing we recognise
// (D14: reported as a WARN, never treated as success).
var ErrOutputChanged = errors.New("hermes output changed: generation cleanup output not recognised")

// Deps of the collector.
type Deps struct {
	Runner   execx.Runner
	Python   string // hermes.Install.Python; "" = not found (D7)
	Home     string // HERMES_HOME of the default profile
	Checkout string // default <Home>/hermes-agent
	// Applicable is false off Windows: the hardlink/image-lock problem is
	// Windows-only, so the step is N/A there.
	Applicable bool
}

// Service implements Collector.
type Service struct{ d Deps }

var _ Collector = (*Service)(nil)

// New returns the collector.
func New(d Deps) *Service {
	if d.Checkout == "" {
		d.Checkout = filepath.Join(d.Home, "hermes-agent")
	}
	return &Service{d: d}
}

const runTimeout = 15 * time.Minute

// Run previews (dryRun) or performs the cleanup. A problem inside the
// cleanup is an error whose text says the update itself is unaffected; the
// caller shows it as a WARN and never flips a verified update.
func (s *Service) Run(ctx context.Context, dryRun bool) (Result, error) {
	if !s.d.Applicable {
		return Result{Skipped: "not needed on this OS"}, nil
	}
	if s.d.Python == "" {
		return Result{Skipped: "managed Python not found"}, nil
	}
	script, err := s.writeScript()
	if err != nil {
		return Result{}, fmt.Errorf("generation cleanup: write helper script: %w", err)
	}
	argv := []string{s.d.Python, "-I", script, "--home", s.d.Home, "--checkout", s.d.Checkout}
	if dryRun {
		argv = append(argv, "--dry-run")
	}
	res, err := s.d.Runner.Run(ctx, execx.Cmd{Argv: argv, Timeout: runTimeout})
	if err != nil {
		return Result{}, err
	}
	out := Result{}
	for _, l := range strings.Split(strings.ReplaceAll(res.Output, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out.Lines = append(out.Lines, strings.TrimRight(l, " \t"))
		}
	}
	if res.Code != 0 {
		return out, fmt.Errorf("generation cleanup rc=%d: %s (the update itself is unaffected)", res.Code, execx.Tail(res.Output, 200))
	}
	if dryRun {
		for _, m := range hermes.GCRemoveRe.FindAllStringSubmatch(res.Output, -1) {
			n, _ := strconv.Atoi(m[1])
			out.Count++
			out.MB += n
		}
		if out.Count > 0 {
			out.Line = fmt.Sprintf("%d old dependency set(s), ~%d MB to free", out.Count, out.MB)
		} else {
			out.Line = "nothing to remove yet"
		}
		return out, nil
	}
	m := hermes.GCDoneRe.FindStringSubmatch(res.Output)
	if m == nil {
		return out, ErrOutputChanged
	}
	out.Count, _ = strconv.Atoi(m[1])
	if mb, _ := strconv.Atoi(m[2]); mb > 0 {
		out.MB = mb
	}
	if out.Count > 0 {
		out.Line = fmt.Sprintf("removed %d old dependency set(s), freed %d MB", out.Count, out.MB)
	} else {
		out.Line = "nothing to remove"
	}
	return out, nil
}

// writeScript puts the embedded script under <home>/cache (rewritten only
// when it differs).
func (s *Service) writeScript() (string, error) {
	p := filepath.Join(s.d.Home, "cache", "safe-update-gc.py")
	if cur, err := os.ReadFile(p); err == nil && string(cur) == Script {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return p, fsx.WriteFileAtomic(p, []byte(Script), 0o644)
}
