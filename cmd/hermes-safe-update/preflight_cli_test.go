package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// --allow-elevated overrides the elevation stop of run and check only.
func TestParseAllowElevated(t *testing.T) {
	for _, args := range [][]string{{"--allow-elevated"}, {"run", "--allow-elevated"}, {"check", "--allow-elevated"}, {"--check", "--allow-elevated"}} {
		o, err := parseArgs(args)
		if err != nil || !o.allowElev || !o.set["allow-elevated"] {
			t.Errorf("%v: got %+v, %v", args, o, err)
		}
	}
	for _, args := range [][]string{{"migrate-settings", "--allow-elevated"}, {"mirror", "setup", "--allow-elevated"}, {"channel", "--allow-elevated"}, {"version", "--allow-elevated"}} {
		if _, err := parseArgs(args); !errors.Is(err, apperr.ErrUsage) {
			t.Errorf("%v: want a usage error, got %v", args, err)
		}
	}
	if o, err := parseArgs([]string{"run"}); err != nil || o.allowElev {
		t.Errorf("default: %+v, %v", o, err)
	}
}

// The flag reaches the configuration the flow reads.
func TestAllowElevatedFlagReachesTheRunConfig(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{{[]string{"--allow-elevated"}, true}, {nil, false}} {
		o, err := parseArgs(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(o, &environment{platform: platform.NewFake(), getenv: func(string) string { return "" }})
		if err != nil || cfg.Run.AllowElevated != tc.want {
			t.Errorf("%v: AllowElevated = %v, %v", tc.args, cfg.Run.AllowElevated, err)
		}
	}
}

func TestHelpDescribesTheNewPreflightStops(t *testing.T) {
	for _, want := range []string{"--allow-elevated", "elevated", "checkout lock", "tools folder entries"} {
		if !strings.Contains(helpText, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	// the confirmation waits ten minutes now; the old number must be gone
	if !strings.Contains(helpText, "waits (default 600)") || strings.Contains(helpText, "waits (default 30)") {
		t.Error("help must say the confirmation waits 600 seconds by default")
	}
}
