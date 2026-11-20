package config

import (
	"errors"
	"strings"
	"testing"
)

func TestExtrasDefaults(t *testing.T) {
	d := Defaults().Extras
	if d.Plugins != ExtrasAsk || d.Skills != ExtrasAsk || d.Tools != ExtrasReport || d.StaleDays != 14 {
		t.Errorf("defaults = %+v, want ask, ask, report, 14", d)
	}
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "logging:\n  level: \"debug\"\n")
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil || cfg.Extras != d {
		t.Errorf("untouched section: %+v, %v", cfg.Extras, err)
	}
}

func TestExtrasValuesAreRead(t *testing.T) {
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "extras:\n  plugins: report\n  skills: off\n  tools: off\n  stale_days: 0\n")
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil || cfg.Extras != (Extras{Plugins: ExtrasReport, Skills: ExtrasOff, Tools: ExtrasOff, StaleDays: 0}) {
		t.Errorf("got %+v, %v", cfg.Extras, err)
	}
	// a partial section keeps the other defaults
	y = writeFile(t, t.TempDir(), "safe-update.yaml", "extras:\n  stale_days: 30\n")
	cfg, err = Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil || cfg.Extras.Plugins != ExtrasAsk || cfg.Extras.StaleDays != 30 {
		t.Errorf("partial: %+v, %v", cfg.Extras, err)
	}
}

func TestExtrasBadValuesNameTheLine(t *testing.T) {
	cases := []struct{ key, val, want string }{
		{"plugins", "yes", "must be one of ask, report, off"},
		{"skills", "Ask", "must be one of ask, report, off"},
		{"tools", "ask", "must be one of report, off"},
		{"stale_days", "-1", "must not be negative"},
	}
	for _, c := range cases {
		y := writeFile(t, t.TempDir(), "safe-update.yaml", "tunables:\n  min_free_gb: 7\nextras:\n  "+c.key+": "+c.val+"\n")
		_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s=%s: want *ValidationError, got %v", c.key, c.val, err)
		}
		if len(ve.Problems) != 1 || ve.Problems[0].Path != "extras."+c.key || ve.Problems[0].Line != 4 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s=%s: %+v / %v", c.key, c.val, ve.Problems, err)
		}
	}
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "extras:\n  plugin: ask\n")
	if _, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv}); err == nil || !strings.Contains(err.Error(), "plugin") {
		t.Errorf("misspelt key: %v", err)
	}
}
