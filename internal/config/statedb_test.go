package config

import (
	"strings"
	"testing"
)

func TestStateDBDefaultsAndKeys(t *testing.T) {
	d := Defaults().StateDB
	if d.Compact != StateDBCompactAsk || d.Backup != StateDBBackupOn || d.MinFreeGB != 10 {
		t.Fatalf("defaults = %+v", d)
	}
	y := writeFile(t, t.TempDir(), "safe-update.yaml", "statedb:\n  compact: \"off\"\n  backup: \"off\"\n  min_free_gb: 20\n")
	cfg, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
	if err != nil || cfg.StateDB.Compact != StateDBCompactOff || cfg.StateDB.Backup != StateDBBackupOff || cfg.StateDB.MinFreeGB != 20 {
		t.Errorf("got %+v, %v", cfg.StateDB, err)
	}
	// a file without the section keeps the defaults
	y = writeFile(t, t.TempDir(), "safe-update.yaml", "logging:\n  level: \"debug\"\n")
	if cfg, err = Load(LoadOptions{YAMLPath: y, Getenv: noEnv}); err != nil || cfg.StateDB != d {
		t.Errorf("untouched section: %+v, %v", cfg.StateDB, err)
	}
}

func TestStateDBRejectsBadValuesAndMisspeltKeys(t *testing.T) {
	for body, want := range map[string]string{
		"statedb:\n  compact: \"yes\"\n":      "statedb.compact: must be one of ask, off",
		"statedb:\n  backup: \"ask\"\n":       "statedb.backup: must be one of on, off",
		"statedb:\n  min_free_gb: 0\n":        "statedb.min_free_gb: must be at least 1",
		"statedb:\n  min_free_gb: -3\n":       "statedb.min_free_gb: must be at least 1",
		"statedb:\n  compat: \"ask\"\n":       "compat",
		"statedb:\n  backup: \"sometimes\"\n": "statedb.backup",
	} {
		y := writeFile(t, t.TempDir(), "safe-update.yaml", body)
		_, err := Load(LoadOptions{YAMLPath: y, Getenv: noEnv})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}
