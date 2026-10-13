package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMachineStateRemembersUntestedAcceptance(t *testing.T) {
	dir := t.TempDir()
	j := filepath.Join(dir, "safe-update.json")
	var st MachineState
	if st.UntestedAccepted("linux") {
		t.Fatal("empty state accepts nothing")
	}
	st.AcceptUntested("linux", time.Date(2026, 10, 11, 12, 0, 0, 0, time.FixedZone("x", 7200)))
	if err := SaveMachineState(j, st); err != nil {
		t.Fatal(err)
	}
	got, err := LoadMachineState(j)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UntestedAccepted("linux") || got.UntestedAccepted("darwin") || got.AcceptedUntested["linux"] != "2026-10-11T10:00:00Z" {
		t.Errorf("round trip: %+v", got.AcceptedUntested)
	}
}

// A value of an unexpected shape is not interpreted, and not lost.
func TestUntestedAcceptanceOfUnexpectedShapeSurvives(t *testing.T) {
	dir := t.TempDir()
	odd := writeFile(t, dir, "odd.json", `{"accepted_untested": "yes"}`)
	st, err := LoadMachineState(odd)
	if err != nil || st.UntestedAccepted("linux") {
		t.Fatalf("odd shape: %+v %v", st, err)
	}
	if err := SaveMachineState(odd, st); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(odd); !strings.Contains(string(b), `"yes"`) {
		t.Errorf("unexpected value lost: %s", b)
	}
}
