package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

const (
	probeTimeout       = 180 * time.Second
	gwStopTimeout      = 180 * time.Second
	gwStartTimeout     = 300 * time.Second
	gwStopWait         = 20 * time.Second
	gwStopPoll         = 2 * time.Second
	gwAfterForceSettle = 3 * time.Second
	probeInfoMax       = 200
)

var utf8Env = map[string]string{"PYTHONIOENCODING": "utf-8"}

// CLIGateway implements Gateway through the launcher and the process table.
type CLIGateway struct {
	Home     string // HERMES_HOME (gateway_state.json, process classification)
	Launcher string
	Python   string // managed Python; "" = probe unavailable (D7)
	Checkout string
	Run      execx.Runner
	Procs    platform.Procs
	GOOS     string // "windows" runs the probe; anything else = not needed
	// Sleep waits between polls; default time.Sleep. Tests inject a fake.
	Sleep func(time.Duration)
}

var _ Gateway = (*CLIGateway)(nil)

func (g *CLIGateway) sleep(d time.Duration) {
	if g.Sleep != nil {
		g.Sleep(d)
		return
	}
	time.Sleep(d)
}

// State reads gateway_state.json; a missing, unreadable or wrongly typed
// file is the zero value.
func (g *CLIGateway) State() GatewayState {
	b, err := os.ReadFile(joinPath(g.Home, GatewayStateFile))
	if err != nil {
		return GatewayState{}
	}
	var st GatewayState
	if json.Unmarshal(b, &st) != nil {
		return GatewayState{}
	}
	return st
}

// Probe runs the pause-gate probe under Hermes' managed Python. It never
// falls back to a system Python (D7).
func (g *CLIGateway) Probe(ctx context.Context) (GateResult, string) {
	if g.GOOS != "windows" {
		return GateUnknown, "not needed on this platform"
	}
	if g.Python == "" {
		return GateUnknown, "managed Python not found"
	}
	res, err := g.Run.Run(ctx, execx.Cmd{
		Argv:    []string{g.Python, "-I", "-c", GateProbeScript, g.Checkout},
		Timeout: probeTimeout,
		Env:     utf8Env,
	})
	if err != nil {
		return GateUnknown, "probe interrupted: " + err.Error()
	}
	return ParseProbe(res.Output, res.Code)
}

type probeResult struct {
	OK       bool     `json:"ok"`
	Error    string   `json:"error"`
	Chain    []string `json:"chain"`
	Gateways [][]any  `json:"gateways"`
}

// ParseProbe interprets the probe's output (python-behaviour §2.5). Only
// the four known Hermes errors read as "blocked"; every other failure is
// unknown, never blocked.
func ParseProbe(out string, rc int) (GateResult, string) {
	var payload string
	found := false
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, GateProbeTag) {
			payload, found = strings.TrimPrefix(l, GateProbeTag), true
			break
		}
	}
	if !found {
		return GateUnknown, fmt.Sprintf("probe did not run (rc=%d): %s", rc, lastChars(execx.Tail(out, 400), probeInfoMax))
	}
	var r probeResult
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		return GateUnknown, "probe output unreadable"
	}
	if !r.OK {
		chain := r.Chain
		if len(chain) == 0 {
			chain = []string{r.Error}
		}
		last := chain[len(chain)-1]
		if PauseGateErr.MatchString(chain[0]) {
			if _, after, ok := strings.Cut(last, ": "); ok {
				last = after
			}
			return GateBlocked, truncRunes(last, probeInfoMax)
		}
		return GateUnknown, "probe error: " + truncRunes(last, probeInfoMax)
	}
	var parts []string
	for _, gw := range r.Gateways {
		if len(gw) == 2 {
			parts = append(parts, fmt.Sprintf("%v (pid %v)", gw[0], gw[1]))
		}
	}
	if len(parts) == 0 {
		return GateOK, "none running"
	}
	return GateOK, strings.Join(parts, ", ")
}

func lastChars(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// PIDs lists the running gateway processes of this home.
func (g *CLIGateway) PIDs(ctx context.Context) ([]int, error) {
	procs, err := g.gatewayProcs(ctx)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, p.PID)
	}
	return pids, nil
}

// gatewayProcs lists the gateway processes of this home, sorted by pid.
func (g *CLIGateway) gatewayProcs(ctx context.Context) ([]platform.Process, error) {
	procs, err := g.Procs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process listing failed: %w", err)
	}
	var out []platform.Process
	for _, p := range procs {
		if Classify(p, g.Home, g.Checkout, nil) == ClassGateway {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// Stop stops the gateway: `gateway stop`, then `gateway stop --all` (each
// followed by a bounded wait), then a forced kill of what remains. It
// returns an error only if a gateway process is still there at the end.
func (g *CLIGateway) Stop(ctx context.Context) error {
	pids, err := g.PIDs(ctx)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	for _, args := range [][]string{{"gateway", "stop"}, {"gateway", "stop", "--all"}} {
		if _, err := g.Run.Run(ctx, execx.Cmd{Argv: append([]string{g.Launcher}, args...), Timeout: gwStopTimeout, Env: utf8Env}); err != nil {
			return err
		}
		left, err := g.waitGone(ctx)
		if err != nil {
			return err
		}
		if len(left) == 0 {
			return nil
		}
	}
	toKill, err := g.gatewayProcs(ctx)
	if err != nil {
		return err
	}
	for _, p := range toKill {
		if err := g.Procs.KillTree(ctx, p); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	g.sleep(gwAfterForceSettle)
	left, err := g.PIDs(ctx)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		return fmt.Errorf("gateway process(es) %v could not be stopped", left)
	}
	return nil
}

// waitGone polls until no gateway pid is left or the wait is over.
func (g *CLIGateway) waitGone(ctx context.Context) ([]int, error) {
	for waited := time.Duration(0); ; waited += gwStopPoll {
		left, err := g.PIDs(ctx)
		if err != nil || len(left) == 0 || waited >= gwStopWait {
			return left, err
		}
		if err := ctx.Err(); err != nil {
			return left, err
		}
		g.sleep(gwStopPoll)
	}
}

// Start runs `gateway start --all` unless a gateway is already running.
func (g *CLIGateway) Start(ctx context.Context) error {
	pids, err := g.PIDs(ctx)
	if err != nil {
		return err
	}
	if len(pids) > 0 {
		return nil
	}
	res, err := g.Run.Run(ctx, execx.Cmd{Argv: []string{g.Launcher, "gateway", "start", "--all"}, Timeout: gwStartTimeout, Env: utf8Env})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("gateway start failed (rc=%d): %s", res.Code, execx.Tail(res.Output, 200))
	}
	return nil
}
