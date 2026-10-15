package config

import (
	"errors"
	"strings"
	"testing"
)

func TestPreflightDefaults(t *testing.T) {
	d := Defaults().Preflight
	if d.ACLCheck != PreflightStop || d.ARM64Toolchain != PreflightWarn {
		t.Errorf("defaults = %+v, want acl_check stop and arm64_toolchain warn", d)
	}
	// a file without the section keeps both defaults
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "logging:\n  level: \"debug\"\n")
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil || cfg.Preflight != d {
		t.Errorf("untouched section: %+v, %v", cfg.Preflight, err)
	}
}

func TestPreflightModesAreRead(t *testing.T) {
	for _, mode := range []string{PreflightStop, PreflightWarn, PreflightOff} {
		y := writeFile(t, t.TempDir(), "safe-update.yaml",
			"preflight:\n  acl_check: \""+mode+"\"\n  arm64_toolchain: \""+mode+"\"\n")
		cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
		if err != nil || cfg.Preflight.ACLCheck != mode || cfg.Preflight.ARM64Toolchain != mode {
			t.Errorf("%q: got %+v, %v", mode, cfg.Preflight, err)
		}
	}
}

func TestPreflightBadValueNamesTheLine(t *testing.T) {
	for _, key := range []string{"acl_check", "arm64_toolchain"} {
		for _, v := range []string{"error", "Stop", "true", ""} {
			y := writeFile(t, t.TempDir(), "safe-update.yaml", "tunables:\n  min_free_gb: 7\npreflight:\n  "+key+": \""+v+"\"\n")
			_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("%s=%q: want *ValidationError, got %v", key, v, err)
			}
			if len(ve.Problems) != 1 || ve.Problems[0].Path != "preflight."+key || ve.Problems[0].Line != 4 {
				t.Errorf("%s=%q: problems = %+v", key, v, ve.Problems)
			}
			if !strings.Contains(err.Error(), "safe-update.yaml:4: preflight."+key+": must be one of stop, warn, off") {
				t.Errorf("%s=%q: error text %q", key, v, err.Error())
			}
		}
	}
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "preflight:\n  acl_chek: \"stop\"\n")
	if _, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv}); err == nil || !strings.Contains(err.Error(), "acl_chek") {
		t.Errorf("misspelt key: %v", err)
	}
}

func TestAllowElevatedIsAFlagOnly(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: noEnv})
	if err != nil || cfg.Run.AllowElevated {
		t.Fatalf("default: %v, %v", cfg.Run.AllowElevated, err)
	}
	tr := true
	cfg, err = Load(LoadOptions{Getenv: noEnv, Flags: Overrides{AllowElevated: &tr}})
	if err != nil || !cfg.Run.AllowElevated {
		t.Errorf("flag: %v, %v", cfg.Run.AllowElevated, err)
	}
}

// The confirmation waits ten minutes unless --delay says otherwise.
func TestConfirmTimeoutDefaultIsTenMinutesAndDelayOverrides(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: noEnv})
	if err != nil || cfg.Tunables.ConfirmTimeoutS != 600 || cfg.Tunables.ConfirmTimeout().Minutes() != 10 {
		t.Fatalf("default = %d s, %v", cfg.Tunables.ConfirmTimeoutS, err)
	}
	n := 45
	cfg, err = Load(LoadOptions{Getenv: noEnv, Flags: Overrides{ConfirmTimeoutS: &n}})
	if err != nil || cfg.Tunables.ConfirmTimeoutS != 45 {
		t.Errorf("--delay 45: %d, %v", cfg.Tunables.ConfirmTimeoutS, err)
	}
}
