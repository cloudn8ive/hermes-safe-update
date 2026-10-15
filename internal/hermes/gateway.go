package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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
	gwTaskWait         = 60 * time.Second
	// gwStartSettle: how long StartAfterStale waits for a new gateway process
	// after `gateway start` said it worked.
	gwStartSettle = 20 * time.Second
	probeInfoMax  = 200
)

var utf8Env = map[string]string{"PYTHONIOENCODING": "utf-8"}

// CLIGateway implements Gateway through the launcher and the process table.
type CLIGateway struct {
	Home     string // HERMES_HOME (gateway_state.json, process classification)
	Launcher string
	Python   string // managed Python; "" = probe unavailable (D7)
	// PythonNow, when set, finds the managed Python at probe time. An update
	// can replace the interpreter (its folder name carries a version), so a
	// probe after it must not run an interpreter looked up before it.
	PythonNow func() string
	Checkout  string
	Run       execx.Runner
	Procs     platform.Procs
	GOOS      string // "windows" runs the probe; anything else = not needed
	// Tasks, when set, lets Start fall back to the Scheduled Task Hermes names
	// in its own recovery hint when `gateway start` could not bring a gateway
	// up. Nil = no fallback.
	Tasks platform.Autostart
	// Sleep waits between polls; default time.Sleep. Tests inject a fake.
	Sleep func(time.Duration)

	// startVia is how the last successful Start brought the gateway up
	// ("" = not started by this object; "gateway start"; or "Task Scheduler
	// <name>").
	startVia string
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
		return GateNotApplicable, "not needed on this platform"
	}
	python := g.Python
	if g.PythonNow != nil {
		if p := g.PythonNow(); p != "" {
			python = p
		}
	}
	if python == "" {
		return GateUnknown, "managed Python not found"
	}
	res, err := g.Run.Run(ctx, execx.Cmd{
		Argv:    []string{python, "-I", "-c", GateProbeScript, g.Checkout},
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

// StopStale stops one gateway that is running old code, for the restart after
// an update: `gateway stop`, a bounded wait, then a forced kill of that pid
// alone. It never runs `gateway stop --all` and never kills another gateway
// process: on macOS/Linux `--all` stops the gateways of every profile, and
// the restart that follows brings back only one. pid is the gateway the state
// file names (0 = unknown: nothing is killed, and an error says it is still
// there). It returns an error only if that gateway is still running.
func (g *CLIGateway) StopStale(ctx context.Context, pid int) error {
	has := func() (bool, error) {
		pids, err := g.PIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, p := range pids {
			if pid <= 0 || p == pid {
				return true, nil
			}
		}
		return false, nil
	}
	still, err := has()
	if err != nil || !still {
		return err
	}
	if _, err := g.Run.Run(ctx, execx.Cmd{Argv: []string{g.Launcher, "gateway", "stop"}, Timeout: gwStopTimeout, Env: utf8Env}); err != nil {
		return err
	}
	for waited := time.Duration(0); ; waited += gwStopPoll {
		if still, err = has(); err != nil || !still {
			return err
		}
		if waited >= gwStopWait {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		g.sleep(gwStopPoll)
	}
	if pid > 0 {
		procs, err := g.gatewayProcs(ctx)
		if err != nil {
			return err
		}
		for _, p := range procs {
			if p.PID != pid {
				continue
			}
			if err := g.Procs.KillTree(ctx, p); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
		g.sleep(gwAfterForceSettle)
		if still, err = has(); err != nil || !still {
			return err
		}
	}
	if pid > 0 {
		return fmt.Errorf("gateway process %d could not be stopped", pid)
	}
	return errors.New("the gateway could not be stopped (its pid is unknown, so it was not killed)")
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

// posix reports whether the gateway is service-managed (systemd, launchd).
// An empty GOOS counts as Windows, the tested default.
func (g *CLIGateway) posix() bool { return g.GOOS != "" && g.GOOS != "windows" }

// Start runs `gateway start --all` unless a gateway is already running. On
// macOS/Linux it runs a plain `gateway start` instead: `--all` there means
// "kill ALL stale gateways across all profiles first", which would also take
// down gateways of other profiles (hermes_cli/gateway.py, `_cmd_start`).
// Unverified on a real Mac or Linux box.
func (g *CLIGateway) Start(ctx context.Context) error {
	pids, err := g.PIDs(ctx)
	if err != nil {
		return err
	}
	if len(pids) > 0 {
		return nil
	}
	return g.start(ctx, !g.posix(), nil)
}

// StartAfterStale brings back the gateway StopStale stopped, on an install
// where other gateways (other profiles) may be running. Start would return
// at once because some gateway exists; this runs `gateway start` anyway. It
// never passes `--all`, on any OS: `--all` sweeps every profile's gateway
// first (hermes_cli/gateway.py, `_cmd_start`), while the plain verb checks
// and starts this profile's gateway only. It refuses if a gateway with
// stalePID is still there, and succeeds only when a gateway process that was
// not there before appears (the others do not count).
func (g *CLIGateway) StartAfterStale(ctx context.Context, stalePID int) error {
	before, err := g.PIDs(ctx)
	if err != nil {
		return err
	}
	for _, p := range before {
		if stalePID > 0 && p == stalePID {
			return fmt.Errorf("gateway process %d is still running; not starting a second one", p)
		}
	}
	if err := g.start(ctx, false, before); err != nil {
		return err
	}
	// `gateway start` can say "already running" for this profile and do
	// nothing; only a gateway that was not there before is a start.
	for waited := time.Duration(0); ; waited += gwStopPoll {
		now, perr := g.PIDs(ctx)
		if perr != nil {
			return perr
		}
		if newPID(now, before) {
			return nil
		}
		if waited >= gwStartSettle || ctx.Err() != nil {
			g.startVia = ""
			return errors.New("`gateway start` ran but no new gateway process appeared")
		}
		g.sleep(gwStopPoll)
	}
}

// newPID reports whether now holds a pid that before does not.
func newPID(now, before []int) bool {
	for _, p := range now {
		if !slices.Contains(before, p) {
			return true
		}
	}
	return false
}

// start runs `gateway start` (with `--all` when all is set), and on a failed
// start the Task Scheduler fallback. known are the gateway pids that already
// ran: only a pid outside it proves the fallback worked.
func (g *CLIGateway) start(ctx context.Context, all bool, known []int) error {
	g.startVia = ""
	argv := []string{g.Launcher, "gateway", "start"}
	if all {
		argv = append(argv, "--all")
	}
	res, err := g.Run.Run(ctx, execx.Cmd{Argv: argv, Timeout: gwStartTimeout, Env: utf8Env})
	if err != nil {
		return err
	}
	if !GatewayStartFailed(res.Code, res.Output) {
		g.startVia = "gateway start"
		return nil
	}
	failure := fmt.Errorf("gateway start failed (rc=%d): %s", res.Code, execx.Tail(res.Output, 200))
	// Task Scheduler starts the process outside this process's Job Object (a
	// parent job can kill a gateway that `gateway start` spawned, #91675). The
	// task name is the one Hermes printed itself, and only a registered task
	// is run.
	name := ParseGatewayTask(res.Output)
	if name == "" || g.Tasks == nil || !g.Tasks.Supported() {
		return failure
	}
	if _, found, terr := g.Tasks.TaskExecutable(ctx, name); terr != nil || !found {
		return failure
	}
	if rerr := g.Tasks.RunTask(ctx, name); rerr != nil {
		return fmt.Errorf("%w; Scheduled Task %s could not be run: %v", failure, name, rerr)
	}
	for waited := time.Duration(0); ; waited += gwStopPoll {
		pids, perr := g.PIDs(ctx)
		if perr != nil {
			return perr
		}
		if newPID(pids, known) {
			g.startVia = "Task Scheduler (" + name + ")"
			return nil
		}
		if waited >= gwTaskWait || ctx.Err() != nil {
			return fmt.Errorf("%w; Scheduled Task %s was run but no gateway appeared", failure, name)
		}
		g.sleep(gwStopPoll)
	}
}

// StartedVia says how the last successful Start brought the gateway up:
// "gateway start", or "Task Scheduler (<task name>)" after the fallback.
// "" when Start did nothing (a gateway was already running) or has not run.
func (g *CLIGateway) StartedVia() string { return g.startVia }
