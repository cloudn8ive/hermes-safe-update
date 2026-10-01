package update

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

// headRepo is a Repo whose HEAD is fixed.
type headRepo struct{ head string }

func (r headRepo) Head(context.Context) (string, string, error) { return "", r.head, nil }
func (headRepo) DirtyCount(context.Context) (int, error)        { return 0, nil }
func (headRepo) DiffKinds(context.Context) (*bool, *bool)       { return nil, nil }

func TestParityNothingChanged(t *testing.T) {
	var cases []struct {
		HeadNow string `json:"head_now"`
		Start   string
		Out     string
		Want    bool
	}
	testutil.ParityFixture(t, "nothing_changed", &cases)
	for _, c := range cases {
		r := &Run{Deps: Deps{Repo: headRepo{head: strings.TrimSpace(c.HeadNow)}}, Facts: Facts{HeadFull: c.Start}}
		if got := r.nothingChanged(context.Background(), c.Out); got != c.Want {
			t.Errorf("nothingChanged(head=%q start=%q out=%q) = %v, Python %v", c.HeadNow, c.Start, c.Out, got, c.Want)
		}
	}
}

func secondsOf(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func TestParityEstimateMinutes(t *testing.T) {
	var cases []struct {
		S    float64
		Want string
	}
	testutil.ParityFixture(t, "est_minutes", &cases)
	for _, c := range cases {
		got := "about " + strconv.Itoa(minutes(secondsOf(c.S))) + " min"
		if got != c.Want {
			t.Errorf("minutes(%vs) -> %q, Python %q", c.S, got, c.Want)
		}
	}
}

func TestParityFmtDuration(t *testing.T) {
	var cases []struct {
		S    float64
		Want string
	}
	testutil.ParityFixture(t, "fmt_duration", &cases)
	for _, c := range cases {
		if got := fmtDuration(secondsOf(c.S)); got != c.Want {
			t.Errorf("fmtDuration(%vs) = %q, Python %q", c.S, got, c.Want)
		}
	}
}

func TestParityDisplayLine(t *testing.T) {
	var cases []struct {
		Line string
		Want *string
	}
	testutil.ParityFixture(t, "display", &cases)
	var hidden []string
	testutil.ParityFixture(t, "ui_hide", &hidden)
	for _, c := range cases {
		got := displayLine(c.Line)
		if c.Want != nil && isTip(*c.Want, hidden) {
			// Python filters the tips in two steps (display_line passes them, the
			// UI module's _HIDE drops them); Go drops them in displayLine. The
			// screen result is the same (hidden), so require that.
			if got != "" {
				t.Errorf("tip %q should be hidden, got %q", c.Line, got)
			}
			continue
		}
		switch {
		case c.Want == nil && got != "":
			t.Errorf("displayLine(%q) = %q, Python hides it", c.Line, got)
		case c.Want != nil && got != *c.Want:
			t.Errorf("displayLine(%q) = %q, Python %q", c.Line, got, *c.Want)
		}
	}
}

func isTip(line string, tips []string) bool {
	for _, h := range tips {
		if strings.Contains(line, h) {
			return true
		}
	}
	return false
}
