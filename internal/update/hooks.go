package update

import (
	"context"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// ExecHooks runs the user's hooks from safe-update.yaml with execx: argv
// only (a shell runs only if argv[0] is one), timeout + WaitDelay, the
// Hermes home as default working directory, context in env vars. The last
// non-blank output line becomes the hook's summary row. A hook can never
// change the update's result.
type ExecHooks struct {
	Runner execx.Runner
	Hooks  []config.Hook
	Dir    string // default working directory (the Hermes home)
	// Procs stops a timed-out hook's whole process tree (a PowerShell
	// hook's children would otherwise linger). Nil = only the direct child.
	Procs platform.Procs
}

var _ HookRunner = (*ExecHooks)(nil)

// Run runs every hook registered for when, in file order.
func (e *ExecHooks) Run(ctx context.Context, when config.HookWhen, env map[string]string) []HookOutcome {
	var out []HookOutcome
	for _, h := range e.Hooks {
		if h.When != when {
			continue
		}
		name := h.Name
		if name == "" && len(h.Run) > 0 {
			name = h.Run[0]
		}
		if len(h.Run) == 0 || h.Run[0] == "" {
			out = append(out, HookOutcome{Name: name, ExitCode: execx.CodeNotFound, LastLine: "no command"})
			continue
		}
		timeout := h.Timeout()
		if timeout <= 0 {
			timeout = defaultHookTimout
		}
		dir := h.Dir
		if dir == "" {
			dir = e.Dir
		}
		cmd := execx.Cmd{Argv: h.Run, Dir: dir, Timeout: timeout, Env: env}
		if e.Procs != nil {
			cmd.KillTree = func(pid int) error { return e.Procs.KillTree(context.WithoutCancel(ctx), platform.Process{PID: pid}) }
		}
		res, err := e.Runner.Run(ctx, cmd)
		o := HookOutcome{Name: name, ExitCode: res.Code, TimedOut: res.TimedOut, LastLine: lastLine(res.Output), Took: res.Duration}
		if res.TimedOut && o.Took == 0 {
			o.Took = timeout
		}
		if err != nil {
			o.ExitCode, o.LastLine = execx.CodeNotFound, err.Error()
		}
		out = append(out, o)
	}
	return out
}

// lastLine returns the last non-blank line, trimmed and capped at 200 runes.
func lastLine(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if r := []rune(l); len(r) > 200 {
				l = string(r[:200])
			}
			return l
		}
	}
	return ""
}
