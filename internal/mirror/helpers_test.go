package mirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

const officialURL = "https://github.com/NousResearch/hermes-agent.git"

// gitBin finds a real git for fixtures, or skips the test.
func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	return p
}

// g runs git in dir for fixture setup (never touches the network: every
// remote is a local path) and returns trimmed stdout.
func g(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitBin(t), append([]string{"-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit adds a file to a non-bare working repo and commits it.
func commit(t *testing.T, work, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	g(t, work, "add", name)
	g(t, work, "commit", "-m", "add "+name)
	return g(t, work, "rev-parse", "HEAD")
}

// env is one synthetic Hermes home plus helpers.
type env struct {
	t      *testing.T
	home   string
	state  string // safe-update.json
	runner *execx.Fake
	now    *testutil.Clock
	sleeps []time.Duration
	alive  map[int]bool
	logs   []string
	out    []string
	mu     sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return &env{
		t: t, home: home,
		state:  filepath.Join(home, "safe-update.json"),
		runner: &execx.Fake{},
		now:    testutil.NewClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)),
		alive:  map[int]bool{},
	}
}

func (e *env) deps() Deps {
	return Deps{
		Runner:      e.runner,
		Git:         "git-under-test",
		Launcher:    "hermes-under-test",
		Home:        e.home,
		MachinePath: e.state,
		Disk:        &platform.FakeDisk{FreeBytes: 100e9},
		Now:         e.now.Now,
		Sleep: func(_ context.Context, d time.Duration) {
			e.mu.Lock()
			e.sleeps = append(e.sleeps, d)
			e.mu.Unlock()
			e.now.Advance(d)
		},
		Alive: func(pid int) bool { return e.alive[pid] },
		Log:   func(s string) { e.logs = append(e.logs, s) },
		Out:   func(s string) { e.out = append(e.out, s) },
		Exe:   `C:\tools\hermes-safe-update.exe`,
	}
}

// realGit returns Deps that run the real git binary (fixtures are local).
func (e *env) realDeps() Deps {
	d := e.deps()
	d.Runner = execx.New()
	d.Git = gitBin(e.t)
	return d
}

// newBare creates an empty bare repo at <t>/<name>.git whose HEAD is main.
func newBare(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".git")
	g(t, filepath.Dir(p), "init", "--bare", "--initial-branch=main", p)
	return p
}

// newWork clones bare into a working repo on main.
func newWork(t *testing.T, bare string) string {
	t.Helper()
	w := filepath.Join(t.TempDir(), "work")
	g(t, filepath.Dir(w), "clone", bare, w)
	g(t, w, "checkout", "-B", "main")
	return w
}

// apiServer serves the two GitHub endpoints the package uses.
type apiServer struct {
	*httptest.Server
	mu      sync.Mutex
	Main    string // sha for /commits/main ("" = 500)
	Compare string // JSON body for /compare/ ("" = 500)
	Size    int    // KB for /repos/<repo>
	UA      []string
	Paths   []string
}

func newAPI(t *testing.T) *apiServer {
	t.Helper()
	a := &apiServer{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.UA = append(a.UA, r.Header.Get("User-Agent"))
		a.Paths = append(a.Paths, r.URL.RequestURI())
		switch {
		case strings.Contains(r.URL.Path, "/commits/main"):
			if a.Main == "" {
				http.Error(w, "boom", 500)
				return
			}
			_, _ = w.Write([]byte(a.Main + "\n"))
		case strings.Contains(r.URL.Path, "/compare/"):
			if a.Compare == "" {
				http.Error(w, "boom", 500)
				return
			}
			_, _ = w.Write([]byte(a.Compare))
		default:
			_, _ = w.Write([]byte(`{"size": ` + strconv.Itoa(a.Size) + `}`))
		}
	}))
	t.Cleanup(a.Close)
	return a
}
