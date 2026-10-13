package update

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func offerRun(t *testing.T, spawn func(context.Context) error, answer rune) *harness {
	t.Helper()
	h := newHarness(t)
	h.mode = ModeCheck
	h.mirror = &fakeMirror{}
	h.spawn = spawn
	h.con.Terminal = true
	h.con.Keys = []platform.Key{key(answer)}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	return h
}

// Where a window of its own can be opened (Windows) the offer says so, and S
// opens it.
func TestMirrorOfferPromisesAWindowOnlyWhereOneOpens(t *testing.T) {
	started := false
	h := offerRun(t, func(context.Context) error { started = true; return nil }, 's')
	h.mustContain("in its own window", "mirror setup started in its own window")
	if !started {
		t.Error("S did not start mirror setup")
	}
}

// macOS/Linux have no spawn: no promise of a window, and S names the command
// to run in a terminal instead of a "could not start" warning.
func TestMirrorOfferWithoutAWindowNamesTheCommand(t *testing.T) {
	h := offerRun(t, nil, 's')
	out := h.output()
	if strings.Contains(out, "own window") {
		t.Errorf("promised a window that cannot open:\n%s", out)
	}
	h.mustContain("S shows the one command to run in a terminal", "run `hermes-safe-update mirror setup` in a terminal")
	if strings.Contains(out, "could not start") {
		t.Errorf("a missing window is not a failure:\n%s", out)
	}
}
