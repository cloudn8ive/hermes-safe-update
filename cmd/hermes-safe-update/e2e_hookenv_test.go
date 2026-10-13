package main

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

const hookEnvYAML = `mirror:
  offer: false
hooks:
  - name: "before"
    when: "pre-close"
    run: ["hermes-tiles.exe", "--before"]
  - name: "tiles"
    when: "post-update"
    run: ["hermes-tiles.exe", "--refresh"]
  - name: "notify"
    when: "post-relaunch"
    run: ["hermes-tiles.exe", "--notify"]
`

// hookEnvByStage maps STAGE to the environment that hook was started with.
func (w *e2eWorld) hookEnvByStage() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, env := range w.hookEnv {
		out[env["HERMES_SAFE_UPDATE_STAGE"]] = env
	}
	return out
}

func (w *e2eWorld) wantHookPaths(stage, python string, env map[string]string) {
	w.t.Helper()
	if env == nil {
		w.t.Fatalf("no %s hook ran (mutations %v)", stage, w.Mutations())
	}
	for k, want := range map[string]string{
		"HERMES_SAFE_UPDATE_HOME": w.home, "HERMES_SAFE_UPDATE_CHECKOUT": w.checkout, "HERMES_SAFE_UPDATE_PYTHON": python,
	} {
		if got, ok := env[k]; !ok || got != want {
			w.t.Errorf("%s hook: %s = %q (set=%v), want %q", stage, k, got, ok, want)
		}
	}
}

// Through the real wiring: every hook gets the Hermes home, the checkout
// and the managed Python. The update installs a newer Python mid-run, so
// the pre-close hook sees the old one and the post-update and post-relaunch
// hooks the new one (looked up when the hook runs).
func TestE2EHookEnvHomeCheckoutPython(t *testing.T) {
	w := newWorld(t)
	w.yaml = hookEnvYAML
	newPython := filepath.Join(w.home, "python", "3.13", "python.exe")
	w.update = append(happyUpdate(), updateLine{time.Second, "→ Installed a new managed Python", func(w *e2eWorld) {
		writeFile(w.t, newPython, "x")
	}})
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	byStage := w.hookEnvByStage()
	if len(byStage) != 3 {
		t.Fatalf("hooks ran for stages %v, want pre-close, post-update, post-relaunch", slices.Collect(mapKeys(byStage)))
	}
	w.wantHookPaths("pre-close", w.python, byStage["pre-close"])
	w.wantHookPaths("post-update", newPython, byStage["post-update"])
	w.wantHookPaths("post-relaunch", newPython, byStage["post-relaunch"])
}

// When verification fails, only the pre-close and post-relaunch hooks run;
// both still get the three paths (and RESULT is problem for the second).
func TestE2EHookEnvOnTheFailurePath(t *testing.T) {
	w := newWorld(t)
	w.yaml = hookEnvYAML
	w.staleGW = true // the restarted gateway still reports the old code
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.Failure)
	byStage := w.hookEnvByStage()
	if _, ran := byStage["post-update"]; ran {
		t.Fatal("post-update hook ran after an unverified update")
	}
	w.wantHookPaths("pre-close", w.python, byStage["pre-close"])
	w.wantHookPaths("post-relaunch", w.python, byStage["post-relaunch"])
	if got := byStage["post-relaunch"]["HERMES_SAFE_UPDATE_RESULT"]; got != "problem" {
		t.Errorf("RESULT = %q, want problem", got)
	}
}

func mapKeys[K comparable, V any](m map[K]V) func(yield func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
