package hermes

import (
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestContractVersionFixture(t *testing.T) {
	out := readFixture(t, "version.txt")
	v := ParseVersion(out)
	if v.Version != "v0.21.5+5279.g4e74031" || v.Build != "2026.9.24" || v.Method != "git" ||
		v.InstallDir != `C:\Users\you\AppData\Local\hermes\hermes-agent` {
		t.Errorf("got %+v", v)
	}
	u := InterpretUpdateCheck(out, 0)
	if u.Behind == nil || !*u.Behind || u.Commits == nil || *u.Commits != 58 {
		t.Errorf("update check = %+v", u)
	}
}

func TestContractFalseFleetFixture(t *testing.T) {
	out := readFixture(t, "update-false-fleet.txt")
	r := EvaluateUpdate(VerifyInput{RC: 1, Output: out, Head: sha40, Gateway: GatewayState{CodeSHA: sha40, PID: 3}, Alive: true})
	if !r.OK || !r.KnownFalseFleet {
		t.Errorf("got %+v", r)
	}
	if m := UpdateCompleteRe.FindStringSubmatch(out); m == nil || m[1] != "v0.21.4" || m[2] != "v0.21.5" {
		t.Errorf("complete = %v", m)
	}
}

func TestContractGateBlockedFixture(t *testing.T) {
	if !PauseGateRe.MatchString(readFixture(t, "update-gate-blocked.txt")) {
		t.Error("pause gate error not recognised")
	}
}
