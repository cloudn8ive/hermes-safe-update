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

func TestUpdateOlderReleaseDefaultAndValidation(t *testing.T) {
	if got := Defaults().Update.OlderRelease; got != OlderReleaseWait {
		t.Errorf("default = %q, want %q", got, OlderReleaseWait)
	}
	for _, v := range []string{OlderReleaseWait, OlderReleaseAsk} {
		y := writeFile(t, t.TempDir(), "safe-update.yaml", "update:\n  older_release: \""+v+"\"\n")
		cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
		if err != nil || cfg.Update.OlderRelease != v || cfg.Update.Channel != ChannelFollow {
			t.Errorf("%q: got %q/%q, %v", v, cfg.Update.OlderRelease, cfg.Update.Channel, err)
		}
	}
	for _, v := range []string{"always", "Wait", "rollback", ""} {
		y := writeFile(t, t.TempDir(), "safe-update.yaml", "update:\n  older_release: \""+v+"\"\n")
		_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
		if err == nil || !strings.Contains(err.Error(), "safe-update.yaml:2: update.older_release: must be one of wait, ask") {
			t.Errorf("%q: error text %v", v, err)
		}
	}
}

func TestAllowRollbackFlagMeansAskForOneRun(t *testing.T) {
	yes := true
	cfg, err := Load(LoadOptions{Getenv: noEnv, Flags: Overrides{AllowRollback: &yes}})
	if err != nil || cfg.Update.OlderRelease != OlderReleaseAsk {
		t.Errorf("flag: %q, %v", cfg.Update.OlderRelease, err)
	}
	cfg, err = Load(LoadOptions{Getenv: noEnv})
	if err != nil || cfg.Update.OlderRelease != OlderReleaseWait {
		t.Errorf("no flag: %q, %v", cfg.Update.OlderRelease, err)
	}
}

// --allow-rollback=false does not turn anything on, and never turns ask off.
func TestAllowRollbackFalseChangesNothing(t *testing.T) {
	no := false
	cfg, err := Load(LoadOptions{Getenv: noEnv, Flags: Overrides{AllowRollback: &no}})
	if err != nil || cfg.Update.OlderRelease != OlderReleaseWait {
		t.Errorf("default + false: %q, %v", cfg.Update.OlderRelease, err)
	}
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "update:\n  older_release: ask\n")
	cfg, err = Load(LoadOptions{YAMLPath: y, Getenv: noEnv, Flags: Overrides{AllowRollback: &no}})
	if err != nil || cfg.Update.OlderRelease != OlderReleaseAsk {
		t.Errorf("ask + false: %q, %v", cfg.Update.OlderRelease, err)
	}
}
