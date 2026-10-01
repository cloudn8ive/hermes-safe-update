package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func noEnv(string) string { return "" }

func TestDefaultsMatchTheCurrentUpdater(t *testing.T) {
	d := Defaults()
	tun := d.Tunables
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"min_free_gb", tun.MinFreeGB, 5.0},
		{"idle_window_s", tun.IdleWindowS, 180},
		{"idle_poll_s", tun.IdlePollS, 60},
		{"idle_wait_max_s", tun.IdleWaitMaxS, 1800},
		{"confirm_timeout_s", tun.ConfirmTimeoutS, 30},
		{"graceful_close_s", tun.GracefulCloseS, 30},
		{"update_timeout_s", tun.UpdateTimeoutS, 5400},
		{"silent_warn_s", tun.SilentWarnS, 600},
		{"verify_wait_s", tun.VerifyWaitS, 120},
		{"marker_refresh_s", tun.MarkerRefreshS, 120},
		{"estimate_window", tun.EstimateWindow, 5},
		{"mirror offer", d.Mirror.OfferTimeoutS, 45},
		{"mirror schedule", d.Mirror.CronSchedule, "every 4h"},
		{"retention", d.Settings.BackupRetention, 3},
		{"auto migrate", d.Settings.AutoMigrate, true},
		{"margin", d.Theme.Margin, 2},
		{"label width", d.Theme.LabelWidth, 15},
		{"status width", d.Theme.StatusWidth, 50},
		{"max width", d.Theme.MaxWidth, 400},
		{"bar band", d.Theme.BarBand, 6},
		{"bar glyph", d.Theme.Glyphs.Bar, "█"},
		{"bar from", d.Theme.BarFrom, [3]int{95, 175, 255}},
		{"bar to", d.Theme.BarTo, [3]int{95, 215, 135}},
		{"accent", d.Theme.Colors.Accent, "38;5;80"},
		{"soft", d.Theme.Colors.Soft, "38;5;250"},
		{"token", d.Theme.Colors.Token, "38;5;183"},
		{"hue Check", d.Theme.Hues["Check"], Hue{Bright: 80, Dark: 30, Icon: "◊"}},
		{"hue Build", d.Theme.Hues["Build"], Hue{Bright: 141, Dark: 97, Icon: "≡"}},
		{"date top right", d.Theme.DateTopRight, true},
		{"taskbar", d.Theme.Taskbar, true},
		{"step desktop", tun.StepSeconds["desktop"], 210},
		{"step remote", tun.StepSeconds["remote"], 75},
		{"log level", d.Logging.Level, "info"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
	if tun.IdleWindow() != 180*time.Second {
		t.Errorf("IdleWindow() = %v", tun.IdleWindow())
	}
	if len(d.Hooks) != 0 {
		t.Errorf("default hooks must be empty (personal hooks come from config), got %d", len(d.Hooks))
	}
}

func TestDefaultMergeListsArePerProfileGlobs(t *testing.T) {
	d := Defaults().Settings
	wantKeep := []string{
		"hermes.desktop.lastRoute.profile.*",
		"hermes.desktop.lastSessionId.profile.*",
		"hermes.desktop.freshDraftKey",
		"hermes.updates.last-passive-check",
		"hermes.desktop.tips.next.v1",
	}
	wantUnion := []string{
		"hermes.desktop.threadScroll.v1.profile.*",
		"hermes.desktop.sessionSeenCounts",
		"hermes.desktop.unreadFinishedSessions",
		"hermes.desktop.sessionOwnerHints.v1",
		"hermes.desktop.toolDisclosure.v1",
	}
	if !reflect.DeepEqual(d.KeepNew, wantKeep) {
		t.Errorf("KeepNew = %v", d.KeepNew)
	}
	if !reflect.DeepEqual(d.Union, wantUnion) {
		t.Errorf("Union = %v", d.Union)
	}
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestMatchKey(t *testing.T) {
	cases := []struct {
		pattern, key string
		want         bool
	}{
		{"hermes.desktop.lastRoute.profile.*", "hermes.desktop.lastRoute.profile.default", true},
		{"hermes.desktop.lastRoute.profile.*", "hermes.desktop.lastRoute.profile.work", true},
		{"hermes.desktop.lastRoute.profile.*", "hermes.desktop.lastRoute", false},
		{"hermes.desktop.freshDraftKey", "hermes.desktop.freshDraftKey", true},
		{"hermes.desktop.freshDraftKey", "hermes.desktop.freshDraftKey2", false},
		{"hermes.*.v1", "hermes.desktop.tips.next.v1", true},
	}
	for _, c := range cases {
		if got := MatchKey(c.pattern, c.key); got != c.want {
			t.Errorf("MatchKey(%q, %q) = %v, want %v", c.pattern, c.key, got, c.want)
		}
	}
}

func TestLoadWithNoFilesGivesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(LoadOptions{
		YAMLPath: filepath.Join(dir, "missing.yaml"),
		JSONPath: filepath.Join(dir, "missing.json"),
		Getenv:   noEnv,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	if !reflect.DeepEqual(cfg.File, want) {
		t.Errorf("config differs from defaults")
	}
	if cfg.Sources.YAML != "" || cfg.Sources.JSON != "" {
		t.Errorf("no file should be recorded as loaded: %+v", cfg.Sources)
	}
}

func TestRequiredYAMLMissingIsInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "typo.yaml")
	_, err := Load(LoadOptions{YAMLPath: p, YAMLRequired: true, Getenv: noEnv})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "typo.yaml") {
		t.Fatalf("explicit config that does not exist must be ErrInvalid naming the path, got %v", err)
	}
	// the default location may be absent
	if _, err := Load(LoadOptions{YAMLPath: p, Getenv: noEnv}); err != nil {
		t.Errorf("default yaml missing must be fine, got %v", err)
	}
}

func TestEmptyYAMLFileIsNoConfig(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", "# only a comment\n")
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil {
		t.Fatalf("empty yaml must not fail: %v", err)
	}
	if !reflect.DeepEqual(cfg.File, Defaults()) {
		t.Error("empty yaml should give defaults")
	}
}

func TestUnknownKeyFailsWithLineAndName(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", "tunables:\n  min_free_gb: 7\n  idle_windw_s: 100\n")
	_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err == nil {
		t.Fatal("expected an error for a misspelled key")
	}
	msg := err.Error()
	for _, want := range []string{"safe-update.yaml", "line 3", "idle_windw_s"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid")
	}
}

func TestTabIndentGetsAHint(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", "tunables:\n\tmin_free_gb: 7\n")
	_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err == nil || !strings.Contains(err.Error(), "tab") {
		t.Fatalf("want a tab hint, got %v", err)
	}
}

func TestPartialYAMLOverridesOnlyGivenKeys(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", `
tunables:
  min_free_gb: 8.5
  step_seconds:
    desktop: 300
theme:
  hues:
    Build: {bright: 99, dark: 55, icon: "≡"}
settings:
  backup_retention: 7
`)
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunables.MinFreeGB != 8.5 || cfg.Settings.BackupRetention != 7 {
		t.Errorf("override not applied: %+v %+v", cfg.Tunables.MinFreeGB, cfg.Settings.BackupRetention)
	}
	if cfg.Tunables.IdleWindowS != 180 {
		t.Errorf("untouched key changed: %d", cfg.Tunables.IdleWindowS)
	}
	if cfg.Tunables.StepSeconds["desktop"] != 300 || cfg.Tunables.StepSeconds["remote"] != 75 {
		t.Errorf("step_seconds map must merge: %v", cfg.Tunables.StepSeconds)
	}
	if cfg.Theme.Hues["Build"].Bright != 99 || cfg.Theme.Hues["Check"].Bright != 80 {
		t.Errorf("hues map must merge: %v", cfg.Theme.Hues)
	}
	if cfg.Sources.YAML != y {
		t.Errorf("Sources.YAML = %q", cfg.Sources.YAML)
	}
}

func TestListsReplaceDefaults(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", "settings:\n  keep_new: [\"a.*\"]\n")
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Settings.KeepNew, []string{"a.*"}) {
		t.Errorf("KeepNew = %v", cfg.Settings.KeepNew)
	}
	if len(cfg.Settings.Union) != 5 {
		t.Errorf("Union should keep defaults, got %v", cfg.Settings.Union)
	}
}

func TestHooksParse(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", `
hooks:
  - name: "restore-start-tile"
    when: "post-update"
    run: ["powershell.exe", "-NoProfile", "-File", "C:\\Users\\you\\tiles.ps1", "-Apply"]
    timeout_s: 120
  - name: "say-hello"
    when: "pre-close"
    run: ["echo", "hi"]
`)
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks) != 2 {
		t.Fatalf("hooks = %d", len(cfg.Hooks))
	}
	h := cfg.Hooks[0]
	if h.Name != "restore-start-tile" || h.When != HookPostUpdate || h.TimeoutS != 120 || len(h.Run) != 5 {
		t.Errorf("hook 0 = %+v", h)
	}
	if cfg.Hooks[1].TimeoutS != DefaultHookTimeoutS {
		t.Errorf("missing timeout_s should default to %d, got %d", DefaultHookTimeoutS, cfg.Hooks[1].TimeoutS)
	}
	if got := cfg.HooksFor(HookPreClose); len(got) != 1 || got[0].Name != "say-hello" {
		t.Errorf("HooksFor(pre-close) = %+v", got)
	}
}

func TestValidationReportsAllProblemsWithLines(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", `tunables:
  min_free_gb: -1
  idle_window_s: 0
settings:
  backup_retention: 0
  keep_new: ["bad[glob"]
hooks:
  - name: "x"
    when: "sometime"
    run: []
logging:
  level: "loud"
`)
	_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
	want := map[string]int{
		"tunables.min_free_gb":      2,
		"tunables.idle_window_s":    3,
		"settings.backup_retention": 5,
		"settings.keep_new[0]":      6,
		"hooks[0].when":             9,
		"hooks[0].run":              10,
		"logging.level":             12,
	}
	got := map[string]int{}
	for _, p := range ve.Problems {
		got[p.Path] = p.Line
	}
	for path, line := range want {
		l, ok := got[path]
		if !ok {
			t.Errorf("missing problem for %s (got %v)", path, got)
			continue
		}
		if l != line {
			t.Errorf("%s reported at line %d, want %d", path, l, line)
		}
	}
	if !strings.Contains(err.Error(), "safe-update.yaml:2:") {
		t.Errorf("error text should use file:line, got %q", err.Error())
	}
}

func TestMachineStateExistingFormat(t *testing.T) {
	dir := t.TempDir()
	j := writeFile(t, dir, "safe-update.json", `{
 "mirror": {
  "path": "C:\\hermes-mirror\\hermes-agent.git",
  "set_up": "2026-09-30T08:36:19-07:00"
 },
 "mirror_offer": "declined",
 "something_new": {"a": 1}
}`)
	st, err := LoadMachineState(j)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mirror == nil || st.Mirror.Path != `C:\hermes-mirror\hermes-agent.git` || st.Mirror.SetUp != "2026-09-30T08:36:19-07:00" {
		t.Errorf("mirror = %+v", st.Mirror)
	}
	if !st.MirrorOfferDeclined() {
		t.Error("mirror_offer declined not read")
	}
	// round trip keeps unknown fields
	st.MirrorOffer = ""
	if err := SaveMachineState(j, st); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(j)
	s := string(b)
	if !strings.Contains(s, "something_new") {
		t.Errorf("unknown field lost on save: %s", s)
	}
	if strings.Contains(s, "mirror_offer") {
		t.Errorf("cleared mirror_offer should be omitted: %s", s)
	}
	st2, err := LoadMachineState(j)
	if err != nil || st2.Mirror.Path != st.Mirror.Path {
		t.Errorf("round trip failed: %+v %v", st2, err)
	}
}

func TestMachineStateMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadMachineState(filepath.Join(dir, "nope.json"))
	if err != nil || st.Mirror != nil {
		t.Errorf("missing file = empty state, got %+v %v", st, err)
	}
	bad := writeFile(t, dir, "bad.json", "{not json")
	_, err = LoadMachineState(bad)
	if err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("corrupt file must be an error naming the path, got %v", err)
	}
	arr := writeFile(t, dir, "arr.json", "[1,2]")
	if _, err := LoadMachineState(arr); err == nil {
		t.Error("non-object JSON must be an error")
	}
}

func TestCorruptMachineStateIsAWarningNotAnError(t *testing.T) {
	dir := t.TempDir()
	j := writeFile(t, dir, "safe-update.json", `{"mirror": {"path": "X:\\m`) // truncated write
	cfg, err := Load(LoadOptions{JSONPath: j, Getenv: noEnv})
	if err != nil {
		t.Fatalf("corrupt machine state must not stop Load: %v", err)
	}
	if cfg.MachineErr == nil || !strings.Contains(cfg.MachineErr.Error(), "safe-update.json") {
		t.Errorf("MachineErr should name the file, got %v", cfg.MachineErr)
	}
	if cfg.Machine.Mirror != nil || cfg.Sources.JSON != "" {
		t.Errorf("corrupt state must load as empty: %+v %+v", cfg.Machine, cfg.Sources)
	}
	if !reflect.DeepEqual(cfg.File, Defaults()) {
		t.Error("defaults should be used")
	}
}

func TestSaveKeepsACorruptFileAside(t *testing.T) {
	dir := t.TempDir()
	const garbage = `{"mirror": {"path": "X:\\m`
	j := writeFile(t, dir, "safe-update.json", garbage)
	if err := SaveMachineState(j, MachineState{MirrorOffer: "declined"}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadMachineState(j)
	if err != nil || !st.MirrorOfferDeclined() {
		t.Fatalf("new state not written: %+v %v", st, err)
	}
	kept, _ := filepath.Glob(filepath.Join(dir, "safe-update.json.corrupt-*"))
	if len(kept) != 1 {
		t.Fatalf("want one .corrupt-<stamp> copy, got %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != garbage {
		t.Errorf("corrupt copy changed: %q", b)
	}
	// a valid file is simply replaced, nothing set aside
	if err := SaveMachineState(j, MachineState{}); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(filepath.Join(dir, "safe-update.json.corrupt-*")); len(again) != 1 {
		t.Errorf("valid file must not be set aside: %v", again)
	}
}

func TestPrecedenceDefaultsYAMLJSONEnvFlags(t *testing.T) {
	dir := t.TempDir()
	y := writeFile(t, dir, "safe-update.yaml", `
tunables:
  confirm_timeout_s: 40
mirror:
  path: "C:\\from-yaml"
logging:
  level: "warn"
`)
	j := writeFile(t, dir, "safe-update.json", `{"mirror": {"path": "C:\\from-json", "set_up": "x"}}`)

	env := map[string]string{"HERMES_SAFE_UPDATE_PLAIN": "1"}
	getenv := func(k string) string { return env[k] }

	cfg, err := Load(LoadOptions{YAMLPath: y, JSONPath: j, Getenv: getenv})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunables.ConfirmTimeoutS != 40 {
		t.Errorf("yaml should beat defaults: %d", cfg.Tunables.ConfirmTimeoutS)
	}
	if cfg.Mirror.Path != `C:\from-json` {
		t.Errorf("json should beat yaml for the mirror path: %q", cfg.Mirror.Path)
	}
	if !cfg.Run.Plain {
		t.Error("HERMES_SAFE_UPDATE_PLAIN should turn on plain output")
	}

	delay, plain, level := 10, false, "debug"
	cfg, err = Load(LoadOptions{YAMLPath: y, JSONPath: j, Getenv: getenv, Flags: Overrides{
		ConfirmTimeoutS: &delay, Plain: &plain, LogLevel: &level,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunables.ConfirmTimeoutS != 10 {
		t.Errorf("--delay should beat yaml: %d", cfg.Tunables.ConfirmTimeoutS)
	}
	if cfg.Run.Plain {
		t.Error("--plain=false flag should beat the env var")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("--log-level should beat yaml: %q", cfg.Logging.Level)
	}
}

func TestNoColorAndDumbTerminal(t *testing.T) {
	for _, env := range []map[string]string{{"NO_COLOR": "1"}, {"TERM": "dumb"}} {
		cfg, err := Load(LoadOptions{Getenv: func(k string) string { return env[k] }})
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Run.NoColor {
			t.Errorf("env %v should disable colour", env)
		}
	}
	cfg, _ := Load(LoadOptions{Getenv: noEnv})
	if cfg.Run.NoColor || cfg.Run.Plain {
		t.Error("colour should be on by default")
	}
}

func TestRunFlagsCopied(t *testing.T) {
	tr := true
	origin := "http://127.0.0.1:47892"
	cfg, err := Load(LoadOptions{Getenv: noEnv, Flags: Overrides{
		Yes: &tr, Unattended: &tr, AcceptUntested: &tr, NoRelaunch: &tr, Force: &tr, NoElevate: &tr, Origin: &origin,
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Run
	if !r.Yes || !r.Unattended || !r.AcceptUntested || !r.NoRelaunch || !r.Force || !r.NoElevate {
		t.Errorf("run flags not copied: %+v", r)
	}
	if cfg.Settings.Origin != origin {
		t.Errorf("--origin not applied: %q", cfg.Settings.Origin)
	}
}

func TestBadFlagValuesAreValidated(t *testing.T) {
	neg := -5
	_, err := Load(LoadOptions{Getenv: noEnv, Flags: Overrides{ConfirmTimeoutS: &neg}})
	if err == nil || !strings.Contains(err.Error(), "confirm_timeout_s") {
		t.Errorf("negative --delay should fail validation: %v", err)
	}
}
