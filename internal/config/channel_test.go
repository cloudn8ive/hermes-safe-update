package config

import (
	"errors"
	"strings"
	"testing"
)

func TestUpdateChannelDefaultsAndValidation(t *testing.T) {
	if got := Defaults().Update.Channel; got != ChannelFollow {
		t.Errorf("default = %q, want %q", got, ChannelFollow)
	}
	for _, v := range []string{ChannelFollow, ChannelStable, ChannelMain} {
		y := writeFile(t, t.TempDir(), "safe-update.yaml", "update:\n  channel: \""+v+"\"\n")
		cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
		if err != nil || cfg.Update.Channel != v {
			t.Errorf("%q: got %q, %v", v, cfg.Update.Channel, err)
		}
	}
	// a file without the section keeps the default
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "logging:\n  level: \"debug\"\n")
	if cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv}); err != nil || cfg.Update.Channel != ChannelFollow {
		t.Errorf("untouched section: %q, %v", cfg.Update.Channel, err)
	}
}

func TestUpdateChannelBadValueNamesTheLine(t *testing.T) {
	for _, v := range []string{"beta", "Stable", "canary", "latest", ""} {
		y := writeFile(t, t.TempDir(), "safe-update.yaml", "tunables:\n  min_free_gb: 7\nupdate:\n  channel: \""+v+"\"\n")
		_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%q: want *ValidationError, got %v", v, err)
		}
		if len(ve.Problems) != 1 || ve.Problems[0].Path != "update.channel" || ve.Problems[0].Line != 4 {
			t.Errorf("%q: problems = %+v", v, ve.Problems)
		}
		if !strings.Contains(err.Error(), "safe-update.yaml:4: update.channel: must be one of follow, stable, main") {
			t.Errorf("%q: error text %q", v, err.Error())
		}
	}
	// a misspelt key is an unknown-key error
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "update:\n  chanel: \"main\"\n")
	if _, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv}); err == nil || !strings.Contains(err.Error(), "chanel") {
		t.Errorf("misspelt key: %v", err)
	}
}

func TestChannelFlagBeatsTheYaml(t *testing.T) {
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "update:\n  channel: \"main\"\n")
	stable := ChannelStable
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv, Flags: Overrides{Channel: &stable}})
	if err != nil || cfg.Update.Channel != ChannelStable || !cfg.Run.ChannelFlag {
		t.Errorf("flag over yaml: %q flag=%v err=%v", cfg.Update.Channel, cfg.Run.ChannelFlag, err)
	}
	cfg, err = Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil || cfg.Update.Channel != ChannelMain || cfg.Run.ChannelFlag {
		t.Errorf("yaml alone: %q flag=%v err=%v", cfg.Update.Channel, cfg.Run.ChannelFlag, err)
	}
	// a bad flag value is validated like a bad yaml value
	bad := "beta"
	_, err = Load(LoadOptions{Getenv: noEnv, Flags: Overrides{Channel: &bad}})
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "update.channel") {
		t.Errorf("bad flag: %v", err)
	}
}
