package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// Plain is the renderer for pipes, files, --plain and TERM=dumb: no escape
// sequences and no footer, but the same information (python-behaviour §5.1).
// It still drives the estimator so timings are recorded in plain mode (D5).
type Plain struct {
	mu       sync.Mutex
	w        io.Writer
	con      platform.Console
	clk      timings.Clock
	est      *timings.Estimator
	groups   []string
	banners  bool
	group    string
	warnings int

	countEvery time.Duration // countdown print interval of a prompt (10 s)
	pollEvery  time.Duration
}

// NewPlain writes to w and reads keys from con.
func NewPlain(w io.Writer, con platform.Console) *Plain {
	return &Plain{w: w, con: con, clk: timings.SystemClock{}, countEvery: 10 * time.Second, pollEvery: 100 * time.Millisecond}
}

var _ Renderer = (*Plain)(nil)

// SetClock replaces the clock used by prompt countdowns and by the
// estimator Start creates when it is given none (tests).
func (p *Plain) SetClock(c timings.Clock) { p.clk = c }

func (p *Plain) println(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.w, s)
}

func (p *Plain) Start(title string, steps []timings.Step, est *timings.Estimator, banners bool) {
	p.mu.Lock()
	p.est, p.banners = est, banners
	if p.est == nil && len(steps) > 0 {
		p.est = timings.NewEstimator(timings.KindUpdate, steps, nil, config.Defaults().Tunables, p.clk)
	}
	for _, s := range steps {
		seen := false
		for _, g := range p.groups {
			seen = seen || g == s.Group
		}
		if !seen {
			p.groups = append(p.groups, s.Group)
		}
	}
	p.mu.Unlock()
	p.println(title)
}

func (p *Plain) Stage(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.est == nil {
		return
	}
	p.est.Stage(key)
	if !p.banners {
		return
	}
	cur, ok := p.est.Current()
	if !ok || cur.Key != key || cur.Group == p.group {
		return
	}
	p.group = cur.Group
	n := 1
	for i, g := range p.groups {
		if g == cur.Group {
			n = i + 1
		}
	}
	fmt.Fprintf(p.w, "== %d/%d %s\n", n, len(p.groups), cur.Group)
}

// WithEstimator runs fn on the estimator under the renderer's lock.
func (p *Plain) WithEstimator(fn func(*timings.Estimator)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.est != nil {
		fn(p.est)
	}
}

func (p *Plain) Section(title string) {
	if p.banners && title == "Pre-flight" {
		return // the stage banner already says it
	}
	p.println("== " + title)
}

func withPrefix(prefix, text string) string {
	if strings.HasPrefix(text, prefix) {
		return text
	}
	return prefix + text
}

func (p *Plain) Line(level Level, text string) {
	switch level {
	case LevelNote:
		text = withPrefix("note: ", text)
	case LevelWarn:
		p.mu.Lock()
		p.warnings++
		p.mu.Unlock()
		text = withPrefix("WARN ", text)
	case LevelStop:
		text = withPrefix("STOP ", text)
	case LevelResult:
		text = withPrefix("RESULT: ", text)
	}
	p.println(text)
}

func (p *Plain) Rows(rows ...Row) {
	for _, r := range rows {
		first, rest, _ := strings.Cut(r.Value, "\n")
		indent := ""
		if r.Nested {
			indent = "  "
		}
		p.println(indent + r.Key + ": " + first)
		if rest != "" {
			for _, extra := range strings.Split(rest, "\n") {
				p.println("   " + strings.TrimLeft(strings.TrimSpace(extra), "· "))
			}
		}
	}
}

func (p *Plain) Output(line string) {
	if hiddenOutput(line) {
		return
	}
	p.println("  | " + strings.TrimRight(line, "\r\n"))
}

func (p *Plain) Command(argv string) { p.println("command: " + argv) }

func (p *Plain) Waiting(string, time.Time, string) {}

func (p *Plain) Prompt(ctx context.Context, spec PromptSpec) (Answer, error) {
	p.println(spec.Text)
	if spec.Timeout > 0 {
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go p.countdown(cctx, p.clk.Now().Add(spec.Timeout))
	}
	return readAnswer(ctx, p.con, spec)
}

// countdown prints "  <N>s" whenever the seconds left reach a multiple of
// the interval (10 s), like the Python's plain prompt.
func (p *Plain) countdown(ctx context.Context, deadline time.Time) {
	every := int(p.countEvery.Seconds())
	if every < 1 {
		every = 1
	}
	last := -1
	t := time.NewTicker(p.pollEvery)
	defer t.Stop()
	for {
		left := int(deadline.Sub(p.clk.Now()).Seconds())
		if left >= 0 && left%every == 0 && left != last {
			last = left
			p.println(fmt.Sprintf("  %ds", left))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *Plain) Complete(ok *bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.est != nil {
		p.est.Complete(ok)
	}
}

func (p *Plain) Summary(c Card) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.w, "\n%s\n", c.Headline)
	for _, r := range c.Rows {
		first, rest, _ := strings.Cut(r.Value, "\n")
		fmt.Fprintf(p.w, "  %-12s %s\n", r.Key, first)
		if rest != "" {
			for _, extra := range strings.Split(rest, "\n") {
				fmt.Fprintf(p.w, "  %-12s %s\n", "", strings.TrimLeft(strings.TrimSpace(extra), "· "))
			}
		}
	}
}

func (p *Plain) Warnings() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.warnings
}

func (p *Plain) Close() {}
