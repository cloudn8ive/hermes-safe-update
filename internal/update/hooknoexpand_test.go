package update

import (
	"context"
	"slices"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// docs/configuration.md promises that `~` and `$VAR` reach a hook exactly as
// written (the argument list goes to the program untouched; a POSIX example
// names /bin/sh and an absolute script path).
func TestHookArgumentsAreNotExpanded(t *testing.T) {
	t.Setenv("HSU_TEST_VAR", "expanded")
	runner := &execx.Fake{}
	var got execx.Cmd
	runner.OnFunc(nil, func(c execx.Cmd) execx.Result {
		got = c
		return execx.Result{Output: "ok"}
	})
	argv := []string{"/bin/sh", "/abs/x.sh", "~/file", "$HSU_TEST_VAR", "${HSU_TEST_VAR}", "%HSU_TEST_VAR%"}
	h := &ExecHooks{Runner: runner, Dir: "~/home",
		Hooks: []config.Hook{{Name: "posix", When: config.HookPostUpdate, Run: argv, Dir: "$HSU_TEST_VAR/work"}}}
	out := h.Run(context.Background(), config.HookPostUpdate, nil)
	if len(out) != 1 || out[0].LastLine != "ok" {
		t.Fatalf("outcome %+v", out)
	}
	if !slices.Equal(got.Argv, argv) {
		t.Errorf("argv changed on its way to the program: %q", got.Argv)
	}
	if got.Dir != "$HSU_TEST_VAR/work" {
		t.Errorf("dir changed: %q", got.Dir)
	}
}
