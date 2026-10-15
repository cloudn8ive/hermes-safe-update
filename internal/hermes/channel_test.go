package hermes

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

var (
	capsNone    = Caps{Probed: true}
	capsChannel = Caps{Channel: true, InstallID: true, Probed: true}
	capsNoID    = Caps{Channel: true, Probed: true}
	capsFailed  = Caps{Err: "timed out"}

	resMain     = Resolved{Channel: "main", Source: SourceRecord, InstallID: "0123456789abcdef"}
	resStable   = Resolved{Channel: "stable", Source: SourceDefault, InstallID: "0123456789abcdef"}
	resCanary   = Resolved{Channel: "canary", Source: SourceRecord, InstallID: "0123456789abcdef"}
	resUnknown  = Resolved{Source: SourceUnknown}
	mainUpdate  = []string{"update", "--yes", "--keep-stash", "--branch", "main"}
	mainCheck   = []string{"update", "--check", "--branch", "main"}
	baseUpdate  = []string{"update", "--yes", "--keep-stash"}
	bareCheck   = []string{"update", "--check"}
	stableApply = []string{"update", "--yes", "--keep-stash", "--channel", "stable"}
	stableCheck = []string{"update", "--check", "--channel", "stable"}
)

// The legacy variable keeps meaning "main": the argv byte for byte as before
// release channels. A change here would silently change every main run.
func TestUpdateArgvMainStillBranchMain(t *testing.T) {
	if got := strings.Join(UpdateArgv, " "); got != "update --yes --keep-stash --branch main" {
		t.Errorf("UpdateArgv = %q", got)
	}
	if got := BuildUpdateArgv("main", capsChannel, resUnknown); !reflect.DeepEqual(got, UpdateArgv) {
		t.Errorf("main plan %v != legacy UpdateArgv %v", got, UpdateArgv)
	}
	if got := BuildUpdateArgv("main", capsNone, resUnknown); !reflect.DeepEqual(got, UpdateArgv) {
		t.Errorf("main plan on a legacy Hermes %v != legacy UpdateArgv %v", got, UpdateArgv)
	}
}

func TestBuildUpdateArgv(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		caps      Caps
		res       Resolved
		update    []string
		check     []string
		release   bool
		pending   bool
		effective string
		stop      bool
	}{
		{"main with channels", "main", capsChannel, resStable, mainUpdate, mainCheck, false, false, "main", false},
		{"main without channels", "main", capsNone, resUnknown, mainUpdate, mainCheck, false, false, "main", false},
		{"main when the probe failed", "main", capsFailed, resUnknown, mainUpdate, mainCheck, false, false, "main", false},
		{"stable with channels", "stable", capsChannel, resMain, stableApply, stableCheck, true, false, "stable", false},
		{"stable needs no install id", "stable", capsNoID, resUnknown, stableApply, stableCheck, true, false, "stable", false},
		{"stable without channels stops", "stable", capsNone, resUnknown, nil, nil, false, false, "", true},
		{"stable when the probe failed stops", "stable", capsFailed, resUnknown, nil, nil, false, false, "", true},
		{"follow, Hermes has no channels", "follow", capsNone, resStable, mainUpdate, mainCheck, false, false, "main", false},
		{"follow, probe failed", "follow", capsFailed, resUnknown, mainUpdate, mainCheck, false, false, "main", false},
		{"follow resolved main is exactly main", "follow", capsChannel, resMain, mainUpdate, mainCheck, false, false, "main", false},
		{"follow resolved stable passes no flag", "follow", capsChannel, resStable, baseUpdate, bareCheck, true, false, "stable", false},
		{"follow resolved canary passes no flag", "follow", capsChannel, resCanary, baseUpdate, bareCheck, true, false, "canary", false},
		{"follow unresolved asks the bare check", "follow", capsChannel, resUnknown, baseUpdate, bareCheck, true, true, "", false},
		{"follow, no install id support, asks the bare check", "follow", capsNoID, resUnknown, baseUpdate, bareCheck, true, true, "", false},
		{"an unknown mode behaves as follow", "", capsChannel, resStable, baseUpdate, bareCheck, true, false, "stable", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := BuildPlan(c.mode, c.caps, c.res)
			if !reflect.DeepEqual(p.UpdateArgv, c.update) || !reflect.DeepEqual(p.CheckArgv, c.check) {
				t.Errorf("argv = %v / %v, want %v / %v", p.UpdateArgv, p.CheckArgv, c.update, c.check)
			}
			if !reflect.DeepEqual(BuildUpdateArgv(c.mode, c.caps, c.res), c.update) || !reflect.DeepEqual(BuildCheckArgv(c.mode, c.caps, c.res), c.check) {
				t.Error("BuildUpdateArgv / BuildCheckArgv disagree with BuildPlan")
			}
			if p.Release != c.release || p.Pending != c.pending || p.Effective != c.effective || (p.Stop != "") != c.stop {
				t.Errorf("release=%v pending=%v effective=%q stop=%q", p.Release, p.Pending, p.Effective, p.Stop)
			}
			if p.Reason == "" {
				t.Error("no reason to log")
			}
			// Safe Update never writes Hermes' channel setting.
			for _, a := range slices.Concat(p.UpdateArgv, p.CheckArgv) {
				if strings.Contains(a, "set-channel") {
					t.Errorf("argv passes %q", a)
				}
			}
			if c.stop {
				for _, want := range []string{"v0.21.6", "update.channel: main"} {
					if !strings.Contains(p.Stop, want) {
						t.Errorf("stop reason lacks %q: %s", want, p.Stop)
					}
				}
			}
		})
	}
}

// Every run owns its argv: appending to one must not reach the shared base.
func TestBuildPlanReturnsFreshSlices(t *testing.T) {
	a := BuildUpdateArgv("stable", capsChannel, resUnknown)
	a[1] = "changed"
	if b := BuildUpdateArgv("stable", capsChannel, resUnknown); b[1] != "--yes" {
		t.Errorf("shared slice: %v", b)
	}
	if UpdateArgv[1] != "--yes" {
		t.Errorf("legacy var changed: %v", UpdateArgv)
	}
}

func TestPlanWithCheckedChannel(t *testing.T) {
	pending := BuildPlan("follow", capsChannel, resUnknown)
	main := pending.WithCheckedChannel("main")
	if main.Release || main.Pending || !reflect.DeepEqual(main.UpdateArgv, mainUpdate) || !reflect.DeepEqual(main.CheckArgv, mainCheck) ||
		main.Resolved.Source != SourceCheck || main.Resolved.Channel != "main" {
		t.Errorf("main: %+v", main)
	}
	st := pending.WithCheckedChannel("stable")
	if !st.Release || st.Pending || st.Effective != "stable" || !reflect.DeepEqual(st.UpdateArgv, baseUpdate) {
		t.Errorf("stable: %+v", st)
	}
	if got := pending.WithCheckedChannel(""); !got.Pending {
		t.Error("an empty channel line must leave the plan pending")
	}
	// a plan that is not pending is never rewritten by a later line
	if got := BuildPlan("stable", capsChannel, resUnknown).WithCheckedChannel("main"); !got.Release || got.Effective != "stable" {
		t.Errorf("stable plan rewritten: %+v", got)
	}
}

func TestChannelCapabilityFromHelp(t *testing.T) {
	with := ParseCaps(readFixture(t, "update-help-channels.txt"))
	if !with.Channel || !with.InstallID || !with.Probed {
		t.Errorf("channel help: %+v", with)
	}
	legacy := ParseCaps(readFixture(t, "update-help-legacy.txt"))
	if legacy.Channel || legacy.InstallID || !legacy.Probed {
		t.Errorf("legacy help: %+v", legacy)
	}
	// "--set-channel" alone is not "--channel"
	if c := ParseCaps("  --set-channel CHANNEL  persist\n  --install-id\n"); c.Channel || !c.InstallID {
		t.Errorf("set-channel only: %+v", c)
	}
	if c := ParseCaps("[--channel CHANNEL]"); !c.Channel {
		t.Errorf("bracketed: %+v", c)
	}

	launcher := "hermes.exe"
	help := []string{launcher, "update", "--help"}
	run := func(res execx.Result) Caps {
		f := (&execx.Fake{}).On(help, res)
		return ProbeCaps(context.Background(), f, launcher)
	}
	if c := run(execx.Result{Output: readFixture(t, "update-help-channels.txt")}); !c.Channel || !c.InstallID {
		t.Errorf("probe: %+v", c)
	}
	for name, res := range map[string]execx.Result{
		"non-zero exit": {Code: 2, Output: readFixture(t, "update-help-channels.txt")},
		"timeout":       {Code: execx.CodeTimeout, TimedOut: true, Output: readFixture(t, "update-help-channels.txt")},
		"cannot start":  {Code: execx.CodeNotFound, Output: "no such file"},
		"empty":         {Output: "  \n"},
	} {
		if c := run(res); c.Channel || c.InstallID || c.Probed || c.Err == "" {
			t.Errorf("%s must mean no capabilities: %+v", name, c)
		}
	}
	// the probe is bounded and runs with UTF-8 output
	f := (&execx.Fake{}).On(help, execx.Result{Output: "x"})
	ProbeCaps(context.Background(), f, launcher)
	if c := f.Calls()[0]; c.Timeout != 45*time.Second || c.Env["PYTHONIOENCODING"] != "utf-8" {
		t.Errorf("probe cmd: timeout=%v env=%v", c.Timeout, c.Env)
	}
}

// ---- reading Hermes' channel ----

const testID = "0f1e2d3c4b5a6978"

// channelWorld is a fake install: a home with an optional config.yaml and a
// checkout whose .git/config names an origin.
type channelWorld struct {
	home, checkout string
	run            *execx.Fake
}

func newChannelWorld(t *testing.T, origin string) *channelWorld {
	t.Helper()
	root := t.TempDir()
	w := &channelWorld{home: filepath.Join(root, "home"), checkout: filepath.Join(root, "home", "hermes-agent")}
	if err := os.MkdirAll(filepath.Join(w.checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		w.gitConfig("[core]\n\tbare = false\n[remote \"origin\"]\n\turl = " + origin + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")
	}
	w.run = (&execx.Fake{}).On([]string{"hermes.exe", "update", "--install-id"}, execx.Result{Output: testID + "\r\n"})
	return w
}

func (w *channelWorld) gitConfig(text string) {
	if err := os.WriteFile(filepath.Join(w.checkout, ".git", "config"), []byte(text), 0o644); err != nil {
		panic(err)
	}
}

func (w *channelWorld) config(text string) {
	if err := os.WriteFile(filepath.Join(w.home, "config.yaml"), []byte(text), 0o644); err != nil {
		panic(err)
	}
}

func (w *channelWorld) resolve() Resolved {
	return ResolveChannel(context.Background(), w.run, "hermes.exe", w.home, w.checkout)
}

const officialURL = "https://github.com/NousResearch/hermes-agent.git"

func TestReadChannelRecordWins(t *testing.T) {
	w := newChannelWorld(t, officialURL)
	w.config("model: x\nupdate:\n  installs:\n    " + testID + ":\n      path: somewhere\n      channel: main\n    ffffffffffffffff:\n      channel: stable\n")
	if got := w.resolve(); got != (Resolved{Channel: "main", Source: SourceRecord, InstallID: testID}) {
		t.Errorf("got %+v", got)
	}
	// another install's record is not ours; the official origin default applies
	w.config("update:\n  installs:\n    ffffffffffffffff:\n      channel: main\n")
	if got := w.resolve(); got != (Resolved{Channel: "stable", Source: SourceDefault, InstallID: testID}) {
		t.Errorf("other install's record: %+v", got)
	}
	// a record with a Hermes channel name that is not main/stable is passed on
	w.config("update:\n  installs:\n    " + testID + ":\n      channel: canary\n")
	if got := w.resolve(); got.Channel != "canary" || got.Source != SourceRecord {
		t.Errorf("canary: %+v", got)
	}
}

func TestReadChannelDefaultByOrigin(t *testing.T) {
	cases := map[string]string{
		officialURL: "stable",
		"https://github.com/NousResearch/hermes-agent":          "stable",
		"https://github.com/nousresearch/Hermes-Agent.git":      "stable",
		"https://github.com/NousResearch/hermes-agent.git/":     "stable",
		"git@github.com:NousResearch/hermes-agent.git":          "stable",
		"ssh://git@github.com/NousResearch/hermes-agent.git":    "stable",
		"https://github.com/someone/hermes-agent.git":           "main", // a fork
		"https://github.com/NousResearch/hermes-agent-fork.git": "main",
		"https://example.com/NousResearch/hermes-agent.git":     "main",
		"C:/Users/you/mirrors/hermes-agent.git":                 "main", // a local mirror as origin
		"file:///c:/mirror/hermes-agent.git":                    "main",
	}
	for url, want := range cases {
		w := newChannelWorld(t, url)
		got := w.resolve()
		if got.Channel != want || got.Source != SourceDefault || got.InstallID != testID {
			t.Errorf("origin %s: %+v, want %s", url, got, want)
		}
	}
}

func TestReadChannelNoConfigFileMeansNoRecord(t *testing.T) {
	w := newChannelWorld(t, officialURL) // no config.yaml at all
	if got := w.resolve(); got.Channel != "stable" || got.Source != SourceDefault {
		t.Errorf("got %+v", got)
	}
}

func TestReadChannelOddShapesAreUnknown(t *testing.T) {
	odd := map[string]string{
		"not yaml":                "update: [unclosed\n",
		"top level list":          "- a\n- b\n",
		"update is a string":      "update: main\n",
		"installs is a list":      "update:\n  installs: [a]\n",
		"channel is a number":     "update:\n  installs:\n    " + testID + ":\n      channel: 3\n",
		"channel is a boolean":    "update:\n  installs:\n    " + testID + ":\n      channel: false\n",
		"channel is a list":       "update:\n  installs:\n    " + testID + ":\n      channel: [main]\n",
		"channel has a bad shape": "update:\n  installs:\n    " + testID + ":\n      channel: \"Main Branch\"\n",
		"channel too long":        "update:\n  installs:\n    " + testID + ":\n      channel: " + strings.Repeat("a", 33) + "\n",
	}
	for name, text := range odd {
		w := newChannelWorld(t, officialURL)
		w.config(text)
		if got := w.resolve(); got.Channel != "" || got.Source != SourceUnknown {
			t.Errorf("%s: %+v, want unknown", name, got)
		}
	}
	// shapes Hermes itself treats as "no record" fall through to the default
	benign := map[string]string{
		"empty file":              "",
		"comment only":            "# nothing\n",
		"no update key":           "model: x\n",
		"update null":             "update:\n",
		"installs null":           "update:\n  installs:\n",
		"record is a string":      "update:\n  installs:\n    " + testID + ": main\n",
		"record without channel":  "update:\n  installs:\n    " + testID + ":\n      path: x\n",
		"record channel is null":  "update:\n  installs:\n    " + testID + ":\n      channel:\n",
		"BOM and CRLF line ends":  "\ufeffmodel: x\r\nupdate:\r\n  installs: {}\r\n",
		"explicit empty installs": "update:\n  installs: {}\n",
	}
	for name, text := range benign {
		w := newChannelWorld(t, officialURL)
		w.config(text)
		if got := w.resolve(); got.Channel != "stable" || got.Source != SourceDefault {
			t.Errorf("%s: %+v, want the default", name, got)
		}
	}
}

func TestReadChannelNeverGuesses(t *testing.T) {
	// no install id
	w := newChannelWorld(t, officialURL)
	for name, res := range map[string]execx.Result{
		"garbage":         {Output: "not an id\n"},
		"failing command": {Code: 2, Output: testID},
		"timeout":         {Code: execx.CodeTimeout, TimedOut: true},
		"cannot start":    {Code: execx.CodeNotFound, Output: "x"},
		"15 digits":       {Output: "0f1e2d3c4b5a697\n"},
		"uppercase":       {Output: "0F1E2D3C4B5A6978\n"},
	} {
		w.run = (&execx.Fake{}).On([]string{"hermes.exe", "update", "--install-id"}, res)
		if got := w.resolve(); got.Source != SourceUnknown || got.Channel != "" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// an id surrounded by launcher noise is still found
	w.run = (&execx.Fake{}).On([]string{"hermes.exe", "update", "--install-id"}, execx.Result{Output: "warning: something\n" + testID + "\n"})
	if got := w.resolve(); got.Channel != "stable" || got.InstallID != testID {
		t.Errorf("noisy output: %+v", got)
	}
	// no readable .git/config: neither stable nor main is a safe guess
	w = newChannelWorld(t, "")
	if got := w.resolve(); got.Source != SourceUnknown || got.InstallID != testID {
		t.Errorf("no git config: %+v", got)
	}
	// an origin-less config is not the official repo, but it is readable: main
	w = newChannelWorld(t, "")
	w.gitConfig("[core]\n\tbare = false\n")
	if got := w.resolve(); got.Channel != "main" || got.Source != SourceDefault {
		t.Errorf("no origin: %+v", got)
	}
}

func TestReadChannelNeverWrites(t *testing.T) {
	w := newChannelWorld(t, officialURL)
	w.config("update:\n  installs: {}\n")
	before, _ := os.ReadFile(filepath.Join(w.home, "config.yaml"))
	w.resolve()
	after, _ := os.ReadFile(filepath.Join(w.home, "config.yaml"))
	if string(before) != string(after) {
		t.Error("config.yaml changed")
	}
	for _, c := range w.run.Calls() {
		for _, a := range c.Argv {
			if strings.Contains(a, "set-channel") {
				t.Errorf("ran %v", c.Argv)
			}
		}
	}
}

func TestOfficialOriginReadsOnlyTheOriginUrl(t *testing.T) {
	cfg := "[remote \"upstream\"]\n\turl = " + officialURL + "\n[remote \"origin\"]\n\turl = https://github.com/someone/fork.git\n"
	if OfficialOrigin(cfg) {
		t.Error("only [remote \"origin\"] counts")
	}
	if !OfficialOrigin("[remote \"origin\"]\r\n\turl = " + officialURL + "\r\n") {
		t.Error("CRLF config")
	}
	if !OfficialOrigin("\ufeff[remote \"origin\"]\n\turl=" + officialURL + "\n") {
		t.Error("BOM and no spaces around =")
	}
	if OfficialOrigin("") {
		t.Error("empty")
	}
}

func TestReadChannelFollowsAGitdirFile(t *testing.T) {
	w := newChannelWorld(t, "")
	gitdir := filepath.Join(filepath.Dir(w.checkout), "elsewhere.git")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "config"), []byte("[remote \"origin\"]\n\turl = "+officialURL+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(w.checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.checkout, ".git"), []byte("gitdir: "+filepath.ToSlash(gitdir)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := w.resolve(); got.Channel != "stable" || got.Source != SourceDefault {
		t.Errorf("gitdir file: %+v", got)
	}
}

func TestAheadOfRelease(t *testing.T) {
	cases := []struct {
		cur, rel string
		n        int
		ok       bool
	}{
		{"v0.22.0+38.g1f6c02c", "0.22.0", 38, true},
		{"0.22.0+38.g1f6c02c", "v0.22.0", 38, true},
		{"v0.22.0+38.g1f6c02c", "0.22.1", 0, false}, // based on another release
		{"v0.22.0", "0.22.0", 0, false},             // exactly on the tag: no distance
		{"v0.21.5+5279.g4e74031", "0.21.5", 5279, true},
		{"", "0.22.0", 0, false},
		{"dev", "0.22.0", 0, false},
	}
	for _, c := range cases {
		if n, ok := AheadOfRelease(c.cur, c.rel); n != c.n || ok != c.ok {
			t.Errorf("AheadOfRelease(%q, %q) = %d, %v; want %d, %v", c.cur, c.rel, n, ok, c.n, c.ok)
		}
	}
}
