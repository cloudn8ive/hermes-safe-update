package hermes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

const verifyTimeout = 300 * time.Second

// CLIVerifier implements Verifier (D17) with the launcher.
type CLIVerifier struct {
	Launcher string
	Run      execx.Runner
}

var _ Verifier = (*CLIVerifier)(nil)

// VerifyDesktop runs `hermes --run-module hermes_cli.desktop_update_verify`.
// rc 0 with empty output = verified; anything else returns the text to show
// (the update stays "OK, desktop build not verified").
func (v *CLIVerifier) VerifyDesktop(ctx context.Context) (bool, string) {
	if v.Launcher == "" {
		return false, "hermes launcher not available; desktop build not verified"
	}
	res, err := v.Run.Run(ctx, execx.Cmd{
		Argv:    append([]string{v.Launcher}, VerifyModuleArgv...),
		Timeout: verifyTimeout,
		Env:     utf8Env,
	})
	if err != nil {
		return false, "desktop verify interrupted: " + err.Error()
	}
	out := strings.TrimSpace(res.Output)
	if res.Code == 0 && out == "" {
		return true, ""
	}
	if out == "" {
		out = fmt.Sprintf("desktop verify exited with rc=%d and no output", res.Code)
	}
	return false, out
}

// VerifyInput is everything the final verdict needs (python-behaviour §2.9).
type VerifyInput struct {
	RC       int    // `hermes update` exit code
	Output   string // `hermes update` output
	Head     string // full HEAD after the update
	Gateway  GatewayState
	Alive    bool // gateway pid alive
	VerifyRC int  // 0 when VerifyDesktop reported ok, non-zero otherwise
	// NoGateway: no gateway ran before the update, so none is required
	// afterwards (a desktop-only install must not end in PROBLEM because no
	// messaging gateway is up). Gateway and Alive are then ignored.
	NoGateway bool
}

// VerifyResult is the verdict plus its log line.
type VerifyResult struct {
	OK              bool
	CodeOK          bool // gateway reports the new HEAD and is alive
	KnownFalseFleet bool // OK only because of the known false fleet warning
	// RelaunchTolerated is the Hermes line (trimmed) when OK only because the
	// updater exited 1 on its own unverified gateway relaunch (#123971) and
	// Safe Update's own check found the gateway on the new code; "" otherwise.
	RelaunchTolerated string
	Line              string
	// Note explains a PROBLEM whose cause Hermes' exit code names (rc 2: another
	// update holds the install); "" otherwise. Shown next to Line, not in it.
	Note string
}

// EvaluateUpdate decides RESULT OK / PROBLEM. Two exit-1 cases count as OK,
// both only when the gateway runs the new code and the desktop build
// verified:
//   - the known false fleet warning (exit 1 + KnownFalseFleet text);
//   - the updater's own unverified gateway relaunch (#123971): exit 1 exactly,
//     a gateway was running before, and every failure line of the output is of
//     that family (ToleratedRelaunchExit); Safe Update's own check stands in
//     for the one Hermes could not make.
func EvaluateUpdate(in VerifyInput) VerifyResult {
	r := VerifyResult{CodeOK: in.NoGateway || (in.Head != "" && in.Gateway.CodeSHA == in.Head && in.Alive)}
	relaunchLine, relaunchOK := "", false
	if in.RC == 1 && !in.NoGateway {
		relaunchLine, relaunchOK = ToleratedRelaunchExit(in.Output)
	}
	switch {
	case in.RC == 0 && r.CodeOK && in.VerifyRC == 0:
		r.OK, r.Line = true, "RESULT: OK"
	case in.RC == 1 && strings.Contains(in.Output, KnownFalseFleet) && r.CodeOK && in.VerifyRC == 0:
		r.OK, r.KnownFalseFleet = true, true
		r.Line = "RESULT: OK (updater exited 1 on the known false fleet warning #93406; gateway verified on new code)"
		if in.NoGateway {
			r.Line = "RESULT: OK (updater exited 1 on the known false fleet warning #93406; no gateway was running)"
		}
	case relaunchOK && r.CodeOK && in.VerifyRC == 0:
		r.OK, r.RelaunchTolerated = true, relaunchLine
		r.Line = "RESULT: OK (updater exited 1 on its own unverified gateway relaunch, hermes-agent #123971; gateway verified on the new code by Safe Update)"
	default:
		r.Line = fmt.Sprintf("RESULT: PROBLEM (update rc=%d, gateway on new code=%t, desktop verify rc=%d)", in.RC, r.CodeOK, in.VerifyRC)
		r.Note = ConcurrentUpdateNote(in.RC, in.Output)
	}
	return r
}

// GatewayWait polls gateway_state.json until the gateway reports the new
// code and its pid is alive (python-behaviour §2.9: 24 x 5 s).
type GatewayWait struct {
	State func() GatewayState
	Alive func(pid int) bool
	Sleep func(time.Duration)
	Max   time.Duration
	Poll  time.Duration
}

// Wait returns the last state and whether its pid is alive. It checks first
// and sleeps after, up to Max in total.
func (w GatewayWait) Wait(ctx context.Context, head string) (GatewayState, bool) {
	var st GatewayState
	alive := false
	for waited := time.Duration(0); ; waited += w.Poll {
		st = w.State()
		alive = w.Alive(st.PID)
		if head != "" && st.CodeSHA == head && alive {
			return st, alive
		}
		if waited >= w.Max || ctx.Err() != nil {
			return st, alive
		}
		w.Sleep(w.Poll)
	}
}

// AliveFunc adapts platform.Procs; a pid <= 0 is dead.
func AliveFunc(p platform.Procs) func(int) bool {
	return func(pid int) bool { return pid > 0 && p.Alive(pid) }
}
