// Package execx runs child processes the one way this tool allows: argv
// slices (never a shell string), a timeout on every call, WaitDelay so a
// grandchild holding the pipe cannot hang us, HERMES_HOME removed from the
// child's environment (children always see the default profile, as in the
// Python updater), stdin closed, and no console window for helpers on
// Windows. Packages depend on the Runner interface; tests use Fake.
package execx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Exit codes used when the child never produced one (same as the Python).
const (
	CodeTimeout  = 124
	CodeNotFound = 127
)

// DefaultTimeout applies when Cmd.Timeout is zero.
const DefaultTimeout = 300 * time.Second

const waitDelay = 5 * time.Second

// ErrEmptyArgv is returned for a Cmd without argv.
var ErrEmptyArgv = errors.New("execx: empty argv")

// Cmd describes one child process.
type Cmd struct {
	Argv    []string
	Dir     string
	Env     map[string]string // added to / overriding os.Environ() (minus HERMES_HOME)
	Timeout time.Duration     // 0 = DefaultTimeout; negative = no timeout (avoid)
	// ShowWindow lets the child have a console window on Windows (only for
	// `hermes update`, whose children are allowed to show progress).
	ShowWindow bool
	// KillTree, when set, replaces the default timeout kill (Process.Kill,
	// which stops only the direct child) with a stop of the whole process
	// tree rooted at the child's pid. If it fails the child is still killed.
	// Only the Timeout triggers it when the caller passes a context that is
	// never cancelled (context.WithoutCancel).
	KillTree func(pid int) error
	// PromptQuiet, when set (Run only), cuts a child that has printed a
	// question (a line ending in "[y/N]", "(y/n)", ...) and then nothing for
	// this long: with no one at its stdin it would wait for the whole Timeout.
	// The result is TimedOut with Stalled and Prompt set. 0 = off.
	PromptQuiet time.Duration
}

// Result of a finished child.
type Result struct {
	Code     int    // exit code; 124 timeout; 127 could not start
	Output   string // stdout then stderr (Run) or merged lines (Stream)
	TimedOut bool
	// Stalled: the child was cut by PromptQuiet; Prompt is its last line.
	Stalled  bool
	Prompt   string
	Duration time.Duration
}

// Runner runs children. Errors are reserved for misuse (empty argv) and
// context cancellation by the caller; a failing child is a Result.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
	// Stream merges stdout and stderr and calls onLine for each line as it
	// arrives (without the line ending). Output holds all lines joined by \n.
	Stream(ctx context.Context, c Cmd, onLine func(string)) (Result, error)
}

// New returns the real runner.
func New() Runner { return realRunner{} }

type realRunner struct{}

// Environ returns os.Environ() without HERMES_HOME, plus extra.
func Environ(extra map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "HERMES_HOME") {
			continue
		}
		if _, override := extra[k]; override {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func (realRunner) prepare(ctx context.Context, c Cmd) (*exec.Cmd, context.Context, context.CancelFunc, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return nil, nil, nil, ErrEmptyArgv
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		cctx, cancel = context.WithTimeout(ctx, timeout)
	}
	cmd := exec.CommandContext(cctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = Environ(c.Env)
	cmd.Stdin = nil
	cmd.WaitDelay = waitDelay
	setSysProcAttr(cmd, c.ShowWindow)
	if c.KillTree != nil {
		killTree := c.KillTree
		cmd.Cancel = func() error {
			if err := killTree(cmd.Process.Pid); err != nil {
				return errors.Join(err, cmd.Process.Kill())
			}
			return nil
		}
	}
	return cmd, cctx, cancel, nil
}

func finish(res *Result, cctx, parent context.Context, err error) {
	var ee *exec.ExitError
	if res.Stalled && err == nil {
		// the child exited 0 (it answered itself, or finished as the watch fired): not a stall
		res.Stalled, res.Prompt = false, ""
	}
	switch {
	case res.Stalled:
		res.Code, res.TimedOut = CodeTimeout, true
		if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
			res.Output += "\n"
		}
		res.Output += "cut: it asked a question and nobody can answer it here"
	case cctx.Err() == context.DeadlineExceeded && parent.Err() == nil:
		res.Code, res.TimedOut = CodeTimeout, true
		if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
			res.Output += "\n"
		}
		res.Output += "timed out after " + res.Duration.Round(time.Second).String()
	case err == nil:
		res.Code = 0
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
	default:
		res.Code = CodeNotFound
		res.Output = err.Error()
	}
}

func (r realRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	cmd, cctx, cancel, err := r.prepare(ctx, c)
	if err != nil {
		return Result{}, err
	}
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var w *activity
	if c.PromptQuiet > 0 {
		w = &activity{}
		cmd.Stdout, cmd.Stderr = w.wrap(&stdout), w.wrap(&stderr)
	}
	start := time.Now()
	stopWatch := func() (bool, string) { return false, "" }
	if w != nil {
		w.touch()
		stopWatch = w.watch(c.PromptQuiet, cancel)
	}
	err = cmd.Run()
	stalled, prompt := stopWatch()
	res := Result{Duration: time.Since(start), Output: stdout.String() + stderr.String(), Stalled: stalled, Prompt: prompt}
	finish(&res, cctx, ctx, err)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

func (r realRunner) Stream(ctx context.Context, c Cmd, onLine func(string)) (Result, error) {
	cmd, cctx, cancel, err := r.prepare(ctx, c)
	if err != nil {
		return Result{}, err
	}
	defer cancel()
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var lines []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			l := strings.TrimRight(sc.Text(), "\r")
			lines = append(lines, l)
			if onLine != nil {
				onLine(l)
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	start := time.Now()
	err = cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	_ = pw.Close()
	wg.Wait()
	res := Result{Duration: time.Since(start), Output: strings.Join(lines, "\n")}
	finish(&res, cctx, ctx, err)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

// Tail returns the last n characters of s with blank lines dropped and the
// rest joined by " | " (the Python log format for child output).
func Tail(s string, n int) string {
	var parts []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			parts = append(parts, strings.TrimSpace(l))
		}
	}
	out := strings.Join(parts, " | ")
	if r := []rune(out); len(r) > n {
		out = string(r[len(r)-n:])
	}
	return out
}

// promptRe: a question left waiting on the same line (no newline after it).
var promptRe = regexp.MustCompile(`(?i)(\[y/n\]|\(y/n\)|\[yes/no\]|\(yes/no\))[ \t]*:?[ \t]*$`)

// LooksLikePrompt reports whether the last line of out is a yes/no question.
func LooksLikePrompt(out string) bool {
	return promptRe.MatchString(lastLineRaw(out))
}

// lastLineRaw is the text after the last line break (empty when out ends with one).
func lastLineRaw(out string) string {
	if i := strings.LastIndexAny(out, "\r\n"); i >= 0 {
		return out[i+1:]
	}
	return out
}

// activity records when a child last wrote and the end of what it wrote.
type activity struct {
	mu     sync.Mutex
	last   time.Time
	recent []byte
}

func (a *activity) touch() {
	a.mu.Lock()
	a.last = time.Now()
	a.mu.Unlock()
}

type activityWriter struct {
	a *activity
	w io.Writer
}

func (a *activity) wrap(w io.Writer) io.Writer { return activityWriter{a, w} }

func (w activityWriter) Write(p []byte) (int, error) {
	w.a.mu.Lock()
	w.a.last = time.Now()
	w.a.recent = append(w.a.recent, p...)
	if len(w.a.recent) > 512 {
		w.a.recent = append([]byte(nil), w.a.recent[len(w.a.recent)-512:]...)
	}
	w.a.mu.Unlock()
	return w.w.Write(p)
}

// watch cancels the child once it is quiet for quiet after a question. The
// returned stop ends the watcher and says whether it cut the child.
func (a *activity) watch(quiet time.Duration, cancel context.CancelFunc) func() (bool, string) {
	done := make(chan struct{})
	finished := make(chan struct{})
	var cut bool
	var prompt string
	go func() {
		defer close(finished)
		tick := time.NewTicker(max(quiet/4, 10*time.Millisecond))
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				a.mu.Lock()
				idle, text := time.Since(a.last), string(a.recent)
				a.mu.Unlock()
				if idle >= quiet && LooksLikePrompt(text) {
					cut, prompt = true, strings.TrimSpace(lastLineRaw(text))
					cancel()
					return
				}
			}
		}
	}()
	return func() (bool, string) {
		close(done)
		<-finished
		return cut, prompt
	}
}
