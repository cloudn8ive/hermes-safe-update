package mirror

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Constants carried over from the Python helper (python-behaviour §3.4).
// DefaultAPIBase is the real GitHub REST endpoint. Only the binary's main()
// passes it in; library code and tests never default to it, so a test that
// forgets to set Deps.APIBase cannot reach the network.
const DefaultAPIBase = "https://api.github.com"

const (
	Repo       = "NousResearch/hermes-agent"
	Branch     = "main"
	githubURL  = "https://github.com/" + Repo + ".git"
	userAgent  = "hermes-safe-update"
	staleLock  = 1800 * time.Second
	netTimeout = 900 * time.Second

	lockFile    = "update-mirror.lock"
	stateFile   = "update-mirror-state.json"
	logFile     = "update-mirror.log"
	shimName    = "hermes_safe_update_mirror.py"
	legacyShim  = "hermes_update_mirror.py"
	mirrorDirNm = "hermes-mirror"
	repoDirNm   = "hermes-agent.git"
)

// Sentinel errors. Callers use errors.Is; SetupExitCode maps them to the
// documented setup exit codes (0 ok, 1 failure, 2 no space/location).
var (
	ErrBusy         = errors.New("another mirror refresh is running")
	ErrNotSetUp     = errors.New("no mirror configured")
	ErrNoLocation   = errors.New("no location has enough free space for a mirror")
	ErrNoSpace      = errors.New("not enough free space for the mirror")
	ErrIncomplete   = errors.New("mirror set up, but the refresh or the background job is not ok")
	ErrCloneFailed  = errors.New("download of the mirror failed")
	errNoMainBranch = errors.New("mirror has no main branch")
)

// Deps are the collaborators of the mirror service. Everything but Runner,
// Git, Home and MachinePath has a usable default (see New).
type Deps struct {
	Runner   execx.Runner
	Git      string // one git resolver for everything (D4: hermes.Install.Git)
	Launcher string // hermes CLI (for `hermes cron ...`)
	Home     string // HERMES_HOME of the default profile
	// MachinePath is safe-update.json; the mirror location lives in it.
	MachinePath string
	Cfg         config.Mirror
	Disk        platform.Disk
	// FallbackDir is where the mirror goes when no other volume has room
	// (default: the parent of Home, i.e. outside the folder a backup zips).
	FallbackDir string
	// Exe is this program, called by the cron shim ("mirror refresh").
	Exe string
	// Checkout is the Hermes checkout (for the pack-size estimate in Location).
	Checkout string

	HTTP     *http.Client // default: plain client, per-call timeouts via ctx
	APIBase  string       // empty = no API (git only); main sets DefaultAPIBase, tests an httptest URL
	CloneURL string       // default the official GitHub URL (tests: a local bare repo)

	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
	Alive func(pid int) bool // nil = assume alive (never steal a live lock)
	Log   func(string)       // file log lines (may be nil)
	Out   func(string)       // user-visible lines for setup/status (may be nil)
}

// Svc implements Service.
type Svc struct {
	d Deps
}

// New fills defaults and returns the service.
func New(d Deps) *Svc {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = func(ctx context.Context, dur time.Duration) {
			t := time.NewTimer(dur)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if d.HTTP == nil {
		d.HTTP = &http.Client{}
	}
	if d.CloneURL == "" {
		d.CloneURL = githubURL
	}
	if d.FallbackDir == "" && d.Home != "" {
		d.FallbackDir = filepath.Dir(d.Home)
	}
	if d.Cfg.CronSchedule == "" {
		d.Cfg.CronSchedule = "every 4h"
	}
	if d.Cfg.JobName == "" {
		d.Cfg.JobName = "Hermes update mirror refresh"
	}
	if d.Cfg.KeepFreeGB == 0 {
		d.Cfg.KeepFreeGB = 5
	}
	if d.Cfg.Growth == 0 {
		d.Cfg.Growth = 0.5
	}
	return &Svc{d: d}
}

// logf sends a line to the structured log (Deps.Log) and, best effort, to
// <home>/logs/update-mirror.log as "<ISO time> <message>" (the documented
// mirror log, same format as the Python helper). A failure to write the
// file never affects the refresh and prints nothing (cron runs are silent).
func (s *Svc) logf(msg string) {
	if s.d.Log != nil {
		s.d.Log(msg)
	}
	s.appendLog(msg)
}

func (s *Svc) appendLog(msg string) {
	if s.d.Home == "" {
		return
	}
	dir := filepath.Join(s.d.Home, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, logFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	eol := "\n"
	if runtime.GOOS == "windows" {
		eol = "\r\n"
	}
	msg = strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)
	_, _ = f.WriteString(s.d.Now().Format(time.RFC3339) + " " + msg + eol)
}

func (s *Svc) say(msg string) {
	if s.d.Out != nil {
		s.d.Out(msg)
	}
}

func (s *Svc) cachePath(name string) string { return filepath.Join(s.d.Home, "cache", name) }

func (s *Svc) markerPath() string { return filepath.Join(s.d.Home, ".hermes-update-in-progress") }

func (s *Svc) alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if s.d.Alive == nil {
		return true
	}
	return s.d.Alive(pid)
}

// exists is a small helper for "is this a file/dir".
func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
