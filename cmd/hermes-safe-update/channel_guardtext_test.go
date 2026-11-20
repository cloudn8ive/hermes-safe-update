package main

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

// F1 suggestion (v0.8.0): the warning for `channel set stable` while ahead of
// the release names what Safe Update does (per update.older_release) and that
// Hermes' own update has no guard.
func TestRollbackGuardTextPerSetting(t *testing.T) {
	wait := rollbackGuardText(config.OlderReleaseWait)
	ask := rollbackGuardText(config.OlderReleaseAsk)
	if !strings.Contains(wait, "wait on this build and never move Hermes back") || strings.Contains(wait, "will ask first") {
		t.Errorf("wait: %s", wait)
	}
	if !strings.Contains(ask, "rollback guard will ask first") {
		t.Errorf("ask: %s", ask)
	}
	for _, s := range []string{wait, ask} {
		if !strings.Contains(s, "moves Hermes back without asking") {
			t.Errorf("the unguarded Hermes update is not named: %s", s)
		}
	}
}
