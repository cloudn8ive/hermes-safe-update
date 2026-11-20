package extras

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

const refHeldRecord = `{"memory-demo": {"pinned": true, "revision": "89abcdef0123456789abcdef0123456789abcdef", "source": "https://example.invalid/m",
 "catalog": {"name": "memory-demo", "pin": "", "repo": "https://example.invalid/m", "sha": "89abcdef0123456789abcdef0123456789abcdef", "tier": "community"}}}`

func TestProbeLeavesARefHeldPluginOutOfTheOffers(t *testing.T) {
	home := t.TempDir()
	writeRecord(t, home, refHeldRecord)
	f := &execx.Fake{}
	f.On(argv("plugins", "check-updates", "--json"), execx.Result{Output: pluginsJSON})
	rep := New(Deps{Runner: f, Launcher: launcher, Home: home}).Probe(context.Background(), Options{Plugins: true})
	if up := rep.Plugins.Updatable(); len(up) != 0 {
		t.Errorf("offered: %+v", up)
	}
	held := rep.Plugins.Held()
	if len(held) != 1 || held[0].Row.Name != "memory-demo" || held[0].Pin.Kind != hermes.PinRef {
		t.Fatalf("held = %+v", held)
	}
	if got := held[0].HeldText(); got != "memory-demo pinned to 89abcdef (not updated; hermes plugins update memory-demo moves it off the pin)" {
		t.Errorf("text = %q", got)
	}
	for _, o := range rep.Plugins.Other() {
		if o.Name == "memory-demo" {
			t.Error("the held plugin is also listed as an update Hermes does not apply")
		}
	}
}

func TestUpdatePluginRechecksThePinAndRunsNothing(t *testing.T) {
	for _, c := range []struct{ name, record, want string }{
		{"ref-held after the probe", refHeldRecord, "pinned with --ref to 89abcdef"},
		{"record gone", "", "its pin state is unknown"},
		{"record unreadable", "{{{", "its pin state is unknown"},
	} {
		w := newWorld(t)
		if c.record == "" {
			w.write("plugins/.install-metadata.json", "{}")
		} else {
			w.write("plugins/.install-metadata.json", c.record)
		}
		out := w.svc.UpdatePlugin(context.Background(), "memory-demo")
		if out.Status != StatusPinned || out.Bad() || !strings.Contains(out.Detail, c.want) {
			t.Errorf("%s: %+v", c.name, out)
		}
		for _, call := range w.f.Calls() {
			t.Errorf("%s: ran %v", c.name, call.Argv)
		}
	}
	// an unpinned catalog install still updates
	w := newWorld(t)
	w.f.On(argv("plugins", "update", "memory-demo"), execx.Result{Output: "Plugin memory-demo updated.\n"})
	if out := w.svc.UpdatePlugin(context.Background(), "memory-demo"); out.Status != StatusUpdated {
		t.Errorf("unpinned: %+v", out)
	}
}
