package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// hookEnvWorld gives the install real absolute paths of this OS (the other
// flow tests use Windows-style constants, which are not absolute on Linux),
// registers one hook per point and records the environment each one gets.
type hookEnvWorld struct {
	h              *harness
	home, checkout string
	pyOld, pyNew   string
	seen           map[string]map[string]string // hook name -> env
}

func newHookEnvWorld(t *testing.T) *hookEnvWorld {
	t.Helper()
	root := t.TempDir()
	w := &hookEnvWorld{
		h:        newHarness(t),
		home:     filepath.Join(root, "hermes"),
		checkout: filepath.Join(root, "hermes", "hermes-agent"),
		seen:     map[string]map[string]string{},
	}
	w.pyOld = filepath.Join(w.home, "tools", "python-3.13", "python")
	w.pyNew = filepath.Join(w.home, "tools", "python-3.14", "python")
	w.h.install.Home, w.h.install.Checkout, w.h.install.Python = w.home, w.checkout, w.pyOld
	// The managed Python is looked up when the hook runs: the old one until
	// `hermes update` has run, the new one after it.
	w.h.managedPython = func() string {
		if w.h.updates > 0 {
			return w.pyNew
		}
		return w.pyOld
	}
	for _, p := range []struct {
		name string
		when config.HookWhen
	}{{"pre", config.HookPreClose}, {"post", config.HookPostUpdate}, {"relaunch", config.HookPostRelaunch}} {
		w.h.cfg.Hooks = append(w.h.cfg.Hooks, config.Hook{Name: p.name, When: p.when, Run: []string{p.name + ".exe"}})
		name := p.name
		w.h.runner.OnFunc([]string{name + ".exe"}, func(c execx.Cmd) execx.Result {
			w.seen[name] = c.Env
			return execx.Result{Output: "ok"}
		})
	}
	w.h.con.Keys = []platform.Key{key('y')}
	return w
}

func (w *hookEnvWorld) want(t *testing.T, hook string, env map[string]string) {
	t.Helper()
	got, ok := w.seen[hook]
	if !ok {
		t.Fatalf("hook %s did not run", hook)
	}
	for k, v := range env {
		if g, present := got[k]; !present || g != v {
			t.Errorf("hook %s: %s = %q (present=%v), want %q", hook, k, g, present, v)
		}
	}
}

// Every hook point gets the home, the checkout and the managed Python next
// to STAGE and RESULT; the post-update and post-relaunch hooks get the
// Python found after the update, not the one pre-flight saw.
func TestHookEnvHomeCheckoutPythonPerStage(t *testing.T) {
	w := newHookEnvWorld(t)
	if err := w.h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, w.h.output())
	}
	base := func(stage, result, py string) map[string]string {
		return map[string]string{
			"HERMES_SAFE_UPDATE_STAGE": stage, "HERMES_SAFE_UPDATE_RESULT": result,
			"HERMES_SAFE_UPDATE_HOME": w.home, "HERMES_SAFE_UPDATE_CHECKOUT": w.checkout,
			"HERMES_SAFE_UPDATE_PYTHON": py,
		}
	}
	w.want(t, "pre", base("pre-close", "unknown", w.pyOld))
	w.want(t, "post", base("post-update", "verified", w.pyNew))
	w.want(t, "relaunch", base("post-relaunch", "verified", w.pyNew))
}

// A failed verification still gives the post-relaunch hook (the only one
// that runs then) the three paths, with RESULT=problem.
func TestHookEnvOnTheFailurePath(t *testing.T) {
	w := newHookEnvWorld(t)
	w.h.verifier = fakeVerifier{ok: false, out: "desktop build missing"}
	_ = w.h.run()
	if _, ran := w.seen["post"]; ran {
		t.Fatal("post-update hook ran after an unverified update")
	}
	w.want(t, "pre", map[string]string{"HERMES_SAFE_UPDATE_PYTHON": w.pyOld})
	w.want(t, "relaunch", map[string]string{
		"HERMES_SAFE_UPDATE_STAGE": "post-relaunch", "HERMES_SAFE_UPDATE_RESULT": "problem",
		"HERMES_SAFE_UPDATE_HOME": w.home, "HERMES_SAFE_UPDATE_CHECKOUT": w.checkout,
		"HERMES_SAFE_UPDATE_PYTHON": w.pyNew,
	})
}

// An unknown value is an empty string, never a missing variable and never a
// relative path: a hook can test `-z`, and the tool's own value replaces
// anything the parent's environment held under the same name.
func TestHookEnvUnknownValuesAreEmptyButSet(t *testing.T) {
	w := newHookEnvWorld(t)
	w.h.install.Python = ""
	w.h.managedPython = func() string { return "" }
	if err := w.h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, w.h.output())
	}
	w.want(t, "post", map[string]string{
		"HERMES_SAFE_UPDATE_HOME": w.home, "HERMES_SAFE_UPDATE_CHECKOUT": w.checkout,
		"HERMES_SAFE_UPDATE_PYTHON": "",
	})

	// relative paths from a config override are not passed on
	r := &Run{}
	r.Facts.Install = hermes.Install{Home: "rel/home", Checkout: "rel/checkout", Python: "rel/python"}
	env := r.hookEnv(config.HookPostUpdate, "verified")
	for _, k := range []string{"HERMES_SAFE_UPDATE_HOME", "HERMES_SAFE_UPDATE_CHECKOUT", "HERMES_SAFE_UPDATE_PYTHON"} {
		if v, ok := env[k]; !ok || v != "" {
			t.Errorf("%s = %q (present=%v), want present and empty", k, v, ok)
		}
	}
}

// The variable names are a public contract: only additions are allowed.
func TestHookEnvNamesAreStable(t *testing.T) {
	r := &Run{}
	env := r.hookEnv(config.HookPreClose, "unknown")
	for _, k := range []string{
		"HERMES_SAFE_UPDATE_STAGE", "HERMES_SAFE_UPDATE_RESULT", "HERMES_SAFE_UPDATE_HOME",
		"HERMES_SAFE_UPDATE_CHECKOUT", "HERMES_SAFE_UPDATE_PYTHON",
	} {
		if _, ok := env[k]; !ok {
			t.Errorf("hook variable %s is gone (variables are only added, never renamed or removed)", k)
		}
	}
}

// TestHookEnvHelperProcess is the child of TestHookEnvReachesARealProcess,
// not a test: it prints the hook variables it was started with.
func TestHookEnvHelperProcess(t *testing.T) {
	if os.Getenv("HSU_HOOKENV_HELPER") != "1" {
		return
	}
	for _, k := range []string{"HERMES_SAFE_UPDATE_STAGE", "HERMES_SAFE_UPDATE_RESULT", "HERMES_SAFE_UPDATE_HOME",
		"HERMES_SAFE_UPDATE_CHECKOUT", "HERMES_SAFE_UPDATE_PYTHON"} {
		v, ok := os.LookupEnv(k)
		fmt.Printf("%s=%q set=%v\n", k, v, ok)
	}
	_, hermesHome := os.LookupEnv("HERMES_HOME")
	fmt.Printf("HERMES_HOME set=%v\n", hermesHome)
	os.Exit(0)
}

// A real child process (the test binary) gets the five variables through
// ExecHooks and the real runner: the tool's value replaces one inherited
// from the parent, an unknown value arrives as set-but-empty, and
// HERMES_HOME is still not passed on.
func TestHookEnvReachesARealProcess(t *testing.T) {
	t.Setenv("HSU_HOOKENV_HELPER", "1")
	t.Setenv("HERMES_HOME", "/inherited/home")
	t.Setenv("HERMES_SAFE_UPDATE_PYTHON", "/inherited/python")
	root := t.TempDir()
	r := &Run{}
	r.Facts.Install = hermes.Install{Home: filepath.Join(root, "h"), Checkout: filepath.Join(root, "h", "c")} // no Python known
	hooks := &ExecHooks{Runner: execx.New(), Dir: root, Hooks: []config.Hook{{
		Name: "env", When: config.HookPostUpdate, TimeoutS: 60,
		Run: []string{os.Args[0], "-test.run=^TestHookEnvHelperProcess$", "-test.v"},
	}}}
	// the hook's whole output is wanted, not just the last line
	cmd := execx.Cmd{Argv: hooks.Hooks[0].Run, Dir: root, Timeout: time.Minute, Env: r.hookEnv(config.HookPostUpdate, "verified")}
	res, err := execx.New().Run(context.Background(), cmd)
	if err != nil || res.Code != 0 {
		t.Fatalf("helper: err=%v code=%d output=%s", err, res.Code, res.Output)
	}
	for _, want := range []string{
		`HERMES_SAFE_UPDATE_STAGE="post-update" set=true`,
		`HERMES_SAFE_UPDATE_RESULT="verified" set=true`,
		`HERMES_SAFE_UPDATE_HOME=` + fmt.Sprintf("%q", filepath.Join(root, "h")) + ` set=true`,
		`HERMES_SAFE_UPDATE_CHECKOUT=` + fmt.Sprintf("%q", filepath.Join(root, "h", "c")) + ` set=true`,
		`HERMES_SAFE_UPDATE_PYTHON="" set=true`,
		`HERMES_HOME set=false`,
	} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("child output lacks %s\n%s", want, res.Output)
		}
	}
	// and the same command through ExecHooks.Run reports success
	outs := hooks.Run(context.Background(), config.HookPostUpdate, r.hookEnv(config.HookPostUpdate, "verified"))
	if len(outs) != 1 || outs[0].ExitCode != 0 || outs[0].TimedOut {
		t.Errorf("ExecHooks.Run = %+v", outs)
	}
}
