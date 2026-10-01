package execx

import (
	"context"
	"strings"
	"sync"
)

// Fake is a scripted Runner for tests. Rules are matched in order: exact
// argv first registered wins, then prefixes. Unmatched commands return 127.
type Fake struct {
	mu    sync.Mutex
	rules []fakeRule
	calls []Cmd
}

type fakeRule struct {
	argv   []string
	prefix bool
	res    Result
	fn     func(Cmd) Result
}

// On registers an exact-argv response.
func (f *Fake) On(argv []string, res Result) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{argv: argv, res: res})
	return f
}

// OnPrefix registers a response for any argv starting with prefix.
func (f *Fake) OnPrefix(prefix []string, res Result) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{argv: prefix, prefix: true, res: res})
	return f
}

// OnFunc registers a computed response for argv starting with prefix.
func (f *Fake) OnFunc(prefix []string, fn func(Cmd) Result) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{argv: prefix, prefix: true, fn: fn})
	return f
}

// Calls returns every Cmd run so far.
func (f *Fake) Calls() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Cmd(nil), f.calls...)
}

func match(r fakeRule, argv []string) bool {
	if !r.prefix && len(r.argv) != len(argv) {
		return false
	}
	if len(r.argv) > len(argv) {
		return false
	}
	for i, a := range r.argv {
		if a != argv[i] {
			return false
		}
	}
	return true
}

func (f *Fake) Run(ctx context.Context, c Cmd) (Result, error) {
	if len(c.Argv) == 0 {
		return Result{}, ErrEmptyArgv
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	rules := append([]fakeRule(nil), f.rules...)
	f.mu.Unlock()
	for _, exact := range []bool{true, false} {
		for _, r := range rules {
			if r.prefix == exact || !match(r, c.Argv) {
				continue
			}
			if r.fn != nil {
				return r.fn(c), ctx.Err()
			}
			return r.res, ctx.Err()
		}
	}
	return Result{Code: CodeNotFound, Output: "fake: no rule for " + strings.Join(c.Argv, " ")}, ctx.Err()
}

func (f *Fake) Stream(ctx context.Context, c Cmd, onLine func(string)) (Result, error) {
	res, err := f.Run(ctx, c)
	if onLine != nil && res.Output != "" {
		for _, l := range strings.Split(res.Output, "\n") {
			onLine(l)
		}
	}
	return res, err
}

var _ Runner = (*Fake)(nil)
