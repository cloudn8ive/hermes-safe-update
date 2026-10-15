package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// This file is how Safe Update keeps talking to Hermes when the launcher it
// used is gone: `bin\hermes.exe` can be replaced by an update (and then be
// missing, or only a `hermes.cmd` that cmd.exe cannot run). Hermes' own
// dashboard runs its updates through the managed Python instead of the
// launcher, and prints that command on request, so the same route is open to
// us (#135766, #126279).

const runtimeArgvTimeout = 60 * time.Second

// RuntimeArgv asks Hermes how to run `hermes <args>` without its launcher:
// `<python> -I -B <checkout>/hermes_cli/_launchers.py
// --print-runtime-command -- <args>` prints the argv as a JSON array. The
// answer is accepted only when it is what Hermes documents: the first element
// an existing python executable under home, then "-I", "-c", the bootstrap
// code, and the arguments exactly as given. Anything else is an error and the
// caller keeps whatever it had.
func RuntimeArgv(ctx context.Context, run execx.Runner, python, home, checkout string, args []string) ([]string, error) {
	if run == nil || python == "" || checkout == "" {
		return nil, errors.New("managed Python or checkout unknown")
	}
	script := joinPath(checkout, filepath.FromSlash(LaunchersScript))
	if !exists(script) {
		return nil, fmt.Errorf("%s not found", LaunchersScript)
	}
	argv := append([]string{python, "-I", "-B", script, RuntimeCommandFlag, "--"}, args...)
	res, err := run.Run(ctx, execx.Cmd{Argv: argv, Timeout: runtimeArgvTimeout, Env: utf8Env})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s exited %d", RuntimeCommandFlag, res.Code)
	}
	var got []string
	lines := strings.Split(strings.ReplaceAll(res.Output, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- { // the last JSON array line: banner noise may precede it
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "[") && json.Unmarshal([]byte(l), &got) == nil {
			break
		}
		got = nil
	}
	if got == nil {
		return nil, errors.New("no command in the output")
	}
	if len(got) != 4+len(args) || got[1] != "-I" || got[2] != "-c" || got[3] == "" {
		return nil, errors.New("unexpected command shape")
	}
	for i, a := range args {
		if got[4+i] != a {
			return nil, errors.New("the command does not end with the requested arguments")
		}
	}
	if !strings.HasPrefix(strings.ToLower(filepath.Base(got[0])), "python") || !exists(got[0]) || !underDir(normPath(got[0]), normPath(home)) {
		return nil, fmt.Errorf("interpreter %q is not a Python under the Hermes home", got[0])
	}
	return got, nil
}

// DirectPrefix is the command prefix that stands in for the launcher:
// RuntimeArgv's answer for no arguments beyond a probe, with the probe
// argument cut off. It is "<python> -I -c <bootstrap>"; Safe Update's own
// arguments are appended to it.
func DirectPrefix(ctx context.Context, run execx.Runner, python, home, checkout string) ([]string, error) {
	got, err := RuntimeArgv(ctx, run, python, home, checkout, []string{"--version"})
	if err != nil {
		return nil, err
	}
	return got[:len(got)-1], nil
}

// LauncherRoute rewrites the command a child runs: while it is set, any
// command whose program is the launcher path From runs Prefix instead
// (Prefix replaces the launcher; the arguments follow). One route is shared by
// everything that runs `hermes ...` in a run (update, gateway, verify, cua,
// mirror), so a launcher that changes mid-run changes for all of them at once.
type LauncherRoute struct {
	mu     sync.RWMutex
	from   string
	prefix []string
}

// Set routes the program `from` to the command prefix: [new launcher] for a
// launcher that moved, [python -I -c bootstrap] for the launcher-free route.
func (l *LauncherRoute) Set(from string, prefix []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.from, l.prefix = from, append([]string(nil), prefix...)
}

// Clear removes the route.
func (l *LauncherRoute) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.from, l.prefix = "", nil
}

// Get returns the routed program and its replacement prefix (nil = none).
func (l *LauncherRoute) Get() (from string, prefix []string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.from, append([]string(nil), l.prefix...)
}

func (l *LauncherRoute) rewrite(c execx.Cmd) execx.Cmd {
	from, prefix := l.Get()
	if from == "" || len(prefix) == 0 || len(c.Argv) == 0 || !sameProgram(c.Argv[0], from) {
		return c
	}
	c.Argv = append(append([]string(nil), prefix...), c.Argv[1:]...)
	return c
}

func sameProgram(a, b string) bool {
	return a == b || strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// Wrap returns a Runner that applies the route before running a command.
func (l *LauncherRoute) Wrap(inner execx.Runner) execx.Runner {
	if l == nil || inner == nil {
		return inner
	}
	return routedRunner{inner: inner, route: l}
}

type routedRunner struct {
	inner execx.Runner
	route *LauncherRoute
}

func (r routedRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	return r.inner.Run(ctx, r.route.rewrite(c))
}

func (r routedRunner) Stream(ctx context.Context, c execx.Cmd, onLine func(string)) (execx.Result, error) {
	return r.inner.Stream(ctx, r.route.rewrite(c), onLine)
}
