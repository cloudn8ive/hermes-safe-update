package ui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

func TestPlainRendererBasics(t *testing.T) {
	var buf bytes.Buffer
	r := NewPlain(&buf, &platform.FakeConsole{})
	r.Start("Hermes Safe Update", nil, nil, false)
	r.Line(LevelInfo, "hello")
	r.Line(LevelWarn, "WARN something")
	r.Rows(Row{Key: "disk free", Value: "120 GB (enough)"})
	ok := true
	r.Summary(Card{OK: &ok, Headline: "Hermes is updated and verified.", Rows: []Row{{Key: "Log", Value: "x.log"}}})
	r.Close()
	out := buf.String()
	for _, want := range []string{"hello", "WARN something", "disk free: 120 GB (enough)", "\nHermes is updated and verified.\n", "  Log          x.log"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain output must not contain escape sequences")
	}
	if r.Warnings() != 1 {
		t.Errorf("Warnings() = %d", r.Warnings())
	}
}

func TestPlainPromptAcceptsOnlyListedKeys(t *testing.T) {
	var buf bytes.Buffer
	con := &platform.FakeConsole{Keys: []platform.Key{{Other: true}, {Rune: 'x'}, {Rune: 'y'}}}
	r := NewPlain(&buf, con)
	a, err := r.Prompt(context.Background(), PromptSpec{Text: "Press Y", Keys: []rune{'y', 'n'}, Timeout: time.Second})
	if err != nil || a.Key != 'y' || a.TimedOut {
		t.Errorf("answer = %+v err %v", a, err)
	}
}

func TestPlainPromptTimeoutAndCtrlC(t *testing.T) {
	var buf bytes.Buffer
	r := NewPlain(&buf, &platform.FakeConsole{})
	a, err := r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: 20 * time.Millisecond})
	if err != nil || !a.TimedOut {
		t.Errorf("want timeout, got %+v %v", a, err)
	}
	r = NewPlain(&buf, &platform.FakeConsole{Keys: []platform.Key{{CtrlC: true}}})
	a, _ = r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y'}, AbortOnCtrlC: true})
	if !a.Abort {
		t.Errorf("want abort, got %+v", a)
	}
}

func TestPlainPromptPrintsCountdownEveryTenSeconds(t *testing.T) {
	var buf bytes.Buffer
	clk := testutil.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	r := NewPlain(&buf, &platform.FakeConsole{})
	r.SetClock(clk)
	r.pollEvery = time.Millisecond
	done := make(chan Answer, 1)
	go func() {
		a, _ := r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: 25 * time.Second})
		done <- a
	}()
	waitFor := func(want string) {
		t.Helper()
		for i := 0; i < 2000; i++ {
			r.mu.Lock()
			s := buf.String()
			r.mu.Unlock()
			if strings.Contains(s, want) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("never printed %q:\n%s", want, buf.String())
	}
	snap := func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return buf.String()
	}
	// the first line is printed whatever the timeout
	waitFor("  25s\n")
	clk.Advance(5 * time.Second) // 20 s left
	waitFor("  20s\n")
	clk.Advance(5 * time.Second) // 15 s left: nothing new
	time.Sleep(20 * time.Millisecond)
	clk.Advance(5 * time.Second) // 10 s left
	waitFor("  10s\n")
	if strings.Contains(snap(), "  15s") {
		t.Errorf("only multiples of 10 are printed:\n%s", snap())
	}
	select {
	case <-done:
		t.Fatal("prompt returned early")
	case <-time.After(20 * time.Millisecond):
	}
}

// A long timeout (the default is ten minutes) must not print sixty lines: once
// a minute while more than two minutes are left, every ten seconds after.
func TestCountdownLineForALongTimeout(t *testing.T) {
	var got []string
	for left := 600; left >= 0; left-- {
		if line, due := countdownLine(left, 10); due {
			got = append(got, line)
		}
	}
	want := []string{"  10 min", "  9 min", "  8 min", "  7 min", "  6 min", "  5 min", "  4 min", "  3 min", "  2 min",
		"  110s", "  100s", "  90s", "  80s", "  70s", "  60s", "  50s", "  40s", "  30s", "  20s", "  10s", "  0s"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lines:\n got %q\nwant %q", got, want)
	}
}

// The prompt itself prints those lines: 590 s left is not one of them, 540 s is.
func TestPlainPromptCountdownIsOncePerMinuteWhileLong(t *testing.T) {
	var buf bytes.Buffer
	clk := testutil.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	r := NewPlain(&buf, &platform.FakeConsole{})
	r.SetClock(clk)
	r.pollEvery = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = r.Prompt(ctx, PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: 10 * time.Minute})
	}()
	snap := func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return buf.String()
	}
	waitFor := func(want string) {
		t.Helper()
		for i := 0; i < 2000; i++ {
			if strings.Contains(snap(), want) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("never printed %q:\n%s", want, snap())
	}
	waitFor("  10 min\n")
	clk.Advance(10 * time.Second) // 590 s left
	time.Sleep(30 * time.Millisecond)
	if strings.Contains(snap(), "590s") || strings.Contains(snap(), "  600s") {
		t.Errorf("a ten-second line while 10 minutes are left:\n%s", snap())
	}
	clk.Advance(50 * time.Second) // 540 s left
	waitFor("  9 min\n")
}

// A timeout that is not on the minute grid (150 s) still starts with a line:
// "3 min", then the once-a-minute rule from 2 min.
func TestPlainPromptPrintsAFirstLineForAnyTimeout(t *testing.T) {
	var buf bytes.Buffer
	clk := testutil.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	r := NewPlain(&buf, &platform.FakeConsole{})
	r.SetClock(clk)
	r.pollEvery = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = r.Prompt(ctx, PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: 150 * time.Second})
	}()
	snap := func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return buf.String()
	}
	waitFor := func(want string) {
		t.Helper()
		for i := 0; i < 2000; i++ {
			if strings.Contains(snap(), want) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("never printed %q:\n%s", want, snap())
	}
	waitFor("  3 min\n")
	clk.Advance(30 * time.Second) // 120 s left
	waitFor("  2 min\n")
	if n := strings.Count(snap(), "  3 min\n"); n != 1 {
		t.Errorf("the first line was printed %d times:\n%s", n, snap())
	}
}

func TestPlainBannersRecordTimingsAndHideUpstreamTips(t *testing.T) {
	var buf bytes.Buffer
	clk := testutil.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	r := NewPlain(&buf, &platform.FakeConsole{})
	r.SetClock(clk)
	tun := config.Defaults().Tunables
	est := timings.NewEstimator(timings.KindUpdate, timings.UpdateSteps(tun.StepSeconds), nil, tun, clk)
	r.Start("Hermes Safe Update", est.Steps, est, true)
	r.Stage("remote")
	clk.Advance(30 * time.Second)
	r.Stage("close")
	r.Output("Tip: You can now select a provider and model:")
	r.Output("kept")
	r.Section("Pre-flight")
	ok := true
	r.Complete(&ok)
	out := buf.String()
	if !strings.Contains(out, "== 1/5 Check\n") || !strings.Contains(out, "== 2/5 Close Hermes\n") {
		t.Errorf("banners: %q", out)
	}
	if strings.Contains(out, "Tip:") || strings.Contains(out, "Pre-flight") || !strings.Contains(out, "  | kept\n") {
		t.Errorf("output: %q", out)
	}
	rec := est.Record(clk.Now().Add(-30 * time.Second))
	if rec == nil || len(rec.Steps) != 2 || rec.Steps[0].Key != "remote" || rec.Steps[0].Seconds != 30 {
		t.Errorf("plain mode must still record timings (D5): %+v", rec)
	}
}

func TestPlainLinesCarryTheirLevelWords(t *testing.T) {
	var buf bytes.Buffer
	r := NewPlain(&buf, &platform.FakeConsole{})
	r.Line(LevelNote, "n")
	r.Line(LevelStop, "STOP s")
	r.Line(LevelResult, "OK")
	r.Line(LevelOK, "fine")
	want := "note: n\nSTOP s\nRESULT: OK\nfine\n"
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}

// gateClock blocks its second Now call (the countdown goroutine's first read)
// until released, so a test can hold that goroutine at a known point.
type gateClock struct {
	inner   timings.Clock
	mu      sync.Mutex
	calls   int
	blocked chan struct{} // closed when the gated call is waiting
	release chan struct{}
}

func (g *gateClock) Now() time.Time {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()
	if n == 2 {
		close(g.blocked)
		<-g.release
	}
	return g.inner.Now()
}

// The countdown goroutine must be finished when Prompt returns: a countdown
// line may not appear after the answer, in the middle of the next section.
func TestPlainPromptCountdownNeverPrintsAfterPromptReturns(t *testing.T) {
	var buf bytes.Buffer
	var bufMu sync.Mutex
	r := NewPlain(&lockedWriter{w: &buf, mu: &bufMu}, &platform.FakeConsole{Keys: []platform.Key{{Rune: 'y'}}})
	g := &gateClock{inner: testutil.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)), blocked: make(chan struct{}), release: make(chan struct{})}
	r.SetClock(g)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Prompt(context.Background(), PromptSpec{Text: "?", Keys: []rune{'y'}, Timeout: 30 * time.Second})
	}()
	<-g.blocked
	select {
	case <-done: // old behaviour: Prompt returned with the countdown goroutine still running
	case <-time.After(100 * time.Millisecond):
	}
	close(g.release)
	<-done
	time.Sleep(100 * time.Millisecond) // a late goroutine would print now
	bufMu.Lock()
	defer bufMu.Unlock()
	if strings.Contains(buf.String(), "30s") {
		t.Errorf("countdown line after the prompt was answered:\n%s", buf.String())
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestPlainHeaderShowsToolVersion(t *testing.T) {
	var buf bytes.Buffer
	r := NewPlain(&buf, &platform.FakeConsole{})
	r.SetVersion("v0.1.2")
	r.Start("Hermes Update Check", nil, nil, false)
	if first := strings.SplitN(buf.String(), "\n", 2)[0]; first != "Hermes Update Check · v0.1.2" {
		t.Errorf("first line = %q", first)
	}

	buf.Reset()
	r = NewPlain(&buf, &platform.FakeConsole{})
	r.Start("Hermes Safe Update", nil, nil, false)
	if first := strings.SplitN(buf.String(), "\n", 2)[0]; first != "Hermes Safe Update" {
		t.Errorf("without a version the header is the bare title, got %q", first)
	}
}
