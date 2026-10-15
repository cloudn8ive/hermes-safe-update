package hermes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// A launcher start-up takes ~20 s on a loaded machine: both probes get 45 s,
// so a timeout is a rare event.
func TestProbeTimeoutsAre45Seconds(t *testing.T) {
	f := (&execx.Fake{}).On([]string{"hermes.exe", "update", "--help"}, execx.Result{Output: "x"}).
		On([]string{"hermes.exe", "update", "--install-id"}, execx.Result{Output: testID})
	ProbeCaps(context.Background(), f, "hermes.exe")
	ReadInstallID(context.Background(), f, "hermes.exe")
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls: %v", calls)
	}
	for _, c := range calls {
		if c.Timeout != 45*time.Second {
			t.Errorf("%v: timeout %v, want 45s", c.Argv, c.Timeout)
		}
	}
}

// sourceCheckout writes the two files a channel-capable Hermes checkout has.
func sourceCheckout(t *testing.T, updateSub string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hermes-agent")
	if err := os.MkdirAll(filepath.Join(dir, "hermes_cli", "subcommands"), 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(rel, text string) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("hermes_cli/update_channel.py", "# release channels\n")
	if updateSub != "" {
		put("hermes_cli/subcommands/update.py", updateSub)
	}
	return dir
}

const updateSubChannels = `    update.add_argument(
        "--set-channel",
    )
    update.add_argument(
        "--install-id",
    )
    update.add_argument(
        "--channel",
    )
`

func TestFileCaps(t *testing.T) {
	t.Run("a channel-capable checkout", func(t *testing.T) {
		c := FileCaps(sourceCheckout(t, updateSubChannels))
		if !c.Channel || !c.InstallID || !c.SetChannel || !c.Probed || c.Source != CapsFromSource {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("the flags are read one by one", func(t *testing.T) {
		c := FileCaps(sourceCheckout(t, "    update.add_argument(\n        \"--channel\",\n    )\n"))
		if !c.Channel || c.InstallID || c.SetChannel {
			t.Errorf("got %+v", c)
		}
	})
	for name, mk := range map[string]func(t *testing.T) string{
		"update_channel.py missing (a Hermes from before channels)": func(t *testing.T) string {
			d := sourceCheckout(t, updateSubChannels)
			if err := os.Remove(filepath.Join(d, "hermes_cli", "update_channel.py")); err != nil {
				t.Fatal(err)
			}
			return d
		},
		"the update sub-command does not name --channel": func(t *testing.T) string {
			return sourceCheckout(t, "    update.add_argument(\"--set-channel\")\n    update.add_argument(\"--branch\")\n")
		},
		"the update sub-command file is missing": func(t *testing.T) string { return sourceCheckout(t, "") },
		"no checkout":                            func(t *testing.T) string { return "" },
		"a checkout that does not exist":         func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
	} {
		t.Run(name, func(t *testing.T) {
			if c := FileCaps(mk(t)); c.Channel || c.Probed || c.Source != CapsFromNone {
				t.Errorf("got %+v, want no capabilities", c)
			}
		})
	}
}

func TestProbeCapsOrSource(t *testing.T) {
	help := []string{"hermes.exe", "update", "--help"}
	ctx := context.Background()

	t.Run("help read: the files are not consulted", func(t *testing.T) {
		f := (&execx.Fake{}).On(help, execx.Result{Output: "usage: hermes update [--branch BRANCH]\n"})
		c := ProbeCapsOrSource(ctx, f, "hermes.exe", sourceCheckout(t, updateSubChannels))
		if c.Channel || !c.Probed || c.Source != CapsFromHelp {
			t.Errorf("a readable help decides: %+v", c)
		}
	})
	for name, res := range map[string]execx.Result{
		"timeout":      {Code: execx.CodeTimeout, TimedOut: true},
		"cannot start": {Code: execx.CodeNotFound, Output: "no such file"},
		"non-zero":     {Code: 2},
		"empty":        {Output: " \n"},
	} {
		t.Run("help fails ("+name+"), the source says channels", func(t *testing.T) {
			f := (&execx.Fake{}).On(help, res)
			c := ProbeCapsOrSource(ctx, f, "hermes.exe", sourceCheckout(t, updateSubChannels))
			if !c.Channel || !c.InstallID || !c.SetChannel || !c.Probed || c.Source != CapsFromSource || c.Err == "" {
				t.Errorf("got %+v", c)
			}
		})
	}
	t.Run("help fails and the source cannot say: none", func(t *testing.T) {
		f := (&execx.Fake{}).On(help, execx.Result{Code: execx.CodeTimeout, TimedOut: true})
		c := ProbeCapsOrSource(ctx, f, "hermes.exe", filepath.Join(t.TempDir(), "nope"))
		if c.Channel || c.Probed || c.Source != CapsFromNone || c.Err != "timed out" {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("a cancelled run does not fall back", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		f := (&execx.Fake{}).On(help, execx.Result{Code: execx.CodeTimeout, TimedOut: true})
		if c := ProbeCapsOrSource(cctx, f, "hermes.exe", sourceCheckout(t, updateSubChannels)); c.Probed {
			t.Errorf("got %+v", c)
		}
	})
}

// The help probe and the install-id read are two Python start-ups: they run
// side by side, so the channel command costs one start-up of wall time, not
// two.
func TestProbeChannelRunsBothReadsAtOnce(t *testing.T) {
	var mu sync.Mutex
	started := 0
	both := make(chan struct{})
	waited := make(map[string]bool) // handler -> saw the other one start in time
	handler := func(name string, out string) func(execx.Cmd) execx.Result {
		return func(execx.Cmd) execx.Result {
			mu.Lock()
			started++
			if started == 2 {
				close(both)
			}
			mu.Unlock()
			select {
			case <-both:
				mu.Lock()
				waited[name] = true
				mu.Unlock()
			case <-time.After(3 * time.Second):
			}
			return execx.Result{Output: out}
		}
	}
	f := (&execx.Fake{}).
		OnFunc([]string{"hermes.exe", "update", "--help"}, handler("help", "[--channel CHANNEL] [--install-id]\n")).
		OnFunc([]string{"hermes.exe", "update", "--install-id"}, handler("id", testID+"\n"))
	w := newChannelWorld(t, officialURL)
	caps, res := ProbeChannel(context.Background(), f, "hermes.exe", w.home, w.checkout, true)
	if !waited["help"] || !waited["id"] {
		t.Errorf("the two reads ran one after the other: %v", waited)
	}
	if !caps.Channel || res.Channel != "stable" || res.InstallID != testID {
		t.Errorf("caps %+v res %+v", caps, res)
	}
}

func TestProbeChannelReadsNoIDWhenNotWanted(t *testing.T) {
	f := (&execx.Fake{}).On([]string{"hermes.exe", "update", "--help"}, execx.Result{Output: "[--channel CHANNEL] [--install-id]\n"})
	w := newChannelWorld(t, officialURL)
	caps, res := ProbeChannel(context.Background(), f, "hermes.exe", w.home, w.checkout, false)
	if !caps.Channel || res.Source != SourceUnknown {
		t.Errorf("caps %+v res %+v", caps, res)
	}
	for _, c := range f.Calls() {
		if strings.Contains(strings.Join(c.Argv, " "), "--install-id") {
			t.Errorf("ran %v", c.Argv)
		}
	}
}

// ---- the record found by path when --install-id fails ----

const (
	idA = "0f1e2d3c4b5a6978"
	idB = "a1b2c3d4e5f60718"
)

func record(id, path, channel string) string {
	s := "    " + id + ":\n"
	if path != "" {
		s += "      path: '" + path + "'\n"
	}
	if channel != "" {
		s += "      channel: " + channel + "\n"
	}
	return s
}

func TestRecordChannelByPath(t *testing.T) {
	const co = `C:\Users\you\AppData\Local\hermes\hermes-agent`
	cfg := func(records ...string) []byte { return []byte("update:\n  installs:\n" + strings.Join(records, "")) }
	cases := []struct {
		name     string
		cfg      []byte
		checkout string
		fold     bool
		wantID   string
		wantCh   string
	}{
		{"exact path", cfg(record(idA, co, "stable")), co, true, idA, "stable"},
		{"the other installs are ignored", cfg(record(idB, `C:\Users\you\other`, "main"), record(idA, co, "stable")), co, true, idA, "stable"},
		{"trailing separator on the record", cfg(record(idA, co+`\`, "main")), co, true, idA, "main"},
		{"trailing separator on the checkout", cfg(record(idA, co, "main")), co + `\`, true, idA, "main"},
		{"slashes against backslashes", cfg(record(idA, "C:/Users/you/AppData/Local/hermes/hermes-agent", "main")), co, true, idA, "main"},
		{"case-insensitive on Windows", cfg(record(idA, strings.ToUpper(co), "stable")), co, true, idA, "stable"},
		{"case matters elsewhere", cfg(record(idA, strings.ToUpper(co), "stable")), co, false, "", ""},
		{"a canary record is passed on", cfg(record(idA, co, "canary")), co, true, idA, "canary"},
		{"no record has this path", cfg(record(idB, `C:\Users\you\other`, "main")), co, true, "", ""},
		{"two records share the path", cfg(record(idA, co, "stable"), record(idB, co, "main")), co, true, "", ""},
		{"the record has no channel", cfg(record(idA, co, "")), co, true, "", ""},
		{"the record has a bad channel name", cfg(record(idA, co, `"Main Branch"`)), co, true, "", ""},
		{"the record key is not an install id", cfg(record("not-an-id", co, "stable")), co, true, "", ""},
		{"no installs", []byte("update:\n"), co, true, "", ""},
		{"not yaml", []byte("update: [unclosed\n"), co, true, "", ""},
		{"empty checkout path", cfg(record(idA, co, "stable")), "", true, "", ""},
		{"a record without a path", cfg(record(idA, "", "stable")), co, true, "", ""},
	}
	// A backslash and a trailing `\` are path separators only on Windows;
	// elsewhere filepath (rightly) treats them as ordinary characters.
	windowsSeparators := map[string]bool{
		"trailing separator on the record":   true,
		"trailing separator on the checkout": true,
		"slashes against backslashes":        true,
	}
	for _, c := range cases {
		if windowsSeparators[c.name] && filepath.Separator != '\\' {
			continue
		}
		id, ch, ok := RecordChannelByPath(c.cfg, c.checkout, c.fold)
		if (ok != (c.wantID != "")) || id != c.wantID || ch != c.wantCh {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q)", c.name, id, ch, ok, c.wantID, c.wantCh)
		}
	}
}

// When `hermes update --install-id` fails, the channel is still found by the
// record whose path is this checkout; the install id stays the primary source.
func TestResolveFallsBackToTheRecordPath(t *testing.T) {
	failing := func() *execx.Fake {
		return (&execx.Fake{}).On([]string{"hermes.exe", "update", "--install-id"}, execx.Result{Code: 2, Output: "Traceback"})
	}
	t.Run("id fails, one record has the path", func(t *testing.T) {
		w := newChannelWorld(t, officialURL) // official origin: the default would be stable
		w.config("update:\n  installs:\n" + record(idB, w.checkout+string(filepath.Separator), "main"))
		w.run = failing()
		got := w.resolve()
		if got != (Resolved{Channel: "main", Source: SourceRecord, InstallID: idB}) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("id fails, no record has the path: unknown, no guess", func(t *testing.T) {
		w := newChannelWorld(t, officialURL)
		w.config("update:\n  installs:\n" + record(idB, filepath.Join(w.home, "elsewhere"), "main"))
		w.run = failing()
		if got := w.resolve(); got.Source != SourceUnknown || got.Channel != "" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("id fails, two records have the path: unknown", func(t *testing.T) {
		w := newChannelWorld(t, officialURL)
		w.config("update:\n  installs:\n" + record(idA, w.checkout, "main") + record(idB, w.checkout, "stable"))
		w.run = failing()
		if got := w.resolve(); got.Source != SourceUnknown {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("the id wins when it is known", func(t *testing.T) {
		w := newChannelWorld(t, officialURL) // --install-id answers testID
		w.config("update:\n  installs:\n" + record(testID, w.checkout, "stable") + record(idB, w.checkout, "main"))
		if got := w.resolve(); got != (Resolved{Channel: "stable", Source: SourceRecord, InstallID: testID}) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("no config file: unknown", func(t *testing.T) {
		w := newChannelWorld(t, officialURL)
		w.run = failing()
		if got := w.resolve(); got.Source != SourceUnknown {
			t.Errorf("got %+v", got)
		}
	})
}
