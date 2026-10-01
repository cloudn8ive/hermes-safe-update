package update

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

func TestFlowStageOrder(t *testing.T) {
	keys := func(m Mode) []string {
		var out []string
		for _, s := range Flow(m) {
			out = append(out, s.Key)
		}
		return out
	}
	up := keys(ModeUpdate)
	if up[0] != "stepAside" || up[len(up)-1] != "summary" {
		t.Errorf("update flow = %v", up)
	}
	idx := map[string]int{}
	for i, k := range up {
		idx[k] = i
	}
	// rule 5 / brief: close before update, verify before settings, settings, then post-update hooks, before relaunch; housekeeping after relaunch
	order := []string{"wait", "close", "gateway_stop", "verify", "settings", "hooks", "relaunch", "cleanup", "cua"}
	for i := 1; i < len(order); i++ {
		if idx[order[i-1]] >= idx[order[i]] {
			t.Errorf("%s must come before %s: %v", order[i-1], order[i], up)
		}
	}
	ck := keys(ModeCheck)
	for _, k := range ck {
		if k == "close" || k == "gateway_stop" {
			t.Errorf("check must never close or update: %v", ck)
		}
	}
}

func TestExecuteStopsAtFirstError(t *testing.T) {
	var ran []string
	stop := errors.New("stop")
	stages := []Stage{
		{Key: "a", Run: func(context.Context, *Run) error { ran = append(ran, "a"); return nil }},
		{Key: "b", Skip: func(*Run) bool { return true }, Run: func(context.Context, *Run) error { ran = append(ran, "b"); return nil }},
		{Key: "c", Run: func(context.Context, *Run) error { ran = append(ran, "c"); return stop }},
		{Key: "d", Run: func(context.Context, *Run) error { ran = append(ran, "d"); return nil }},
	}
	err := Execute(context.Background(), &Run{}, stages)
	if !errors.Is(err, stop) || len(ran) != 2 || ran[1] != "c" {
		t.Errorf("err=%v ran=%v", err, ran)
	}
	notYet := []Stage{{Key: "x", Run: func(context.Context, *Run) error { return apperr.ErrNotImplemented }}}
	if err := Execute(context.Background(), &Run{}, notYet); !errors.Is(err, apperr.ErrNotImplemented) || !strings.Contains(err.Error(), "stage x") {
		t.Errorf("got %v", err)
	}
}
