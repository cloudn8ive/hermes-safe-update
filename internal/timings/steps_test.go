package timings

import "testing"

func TestStepListsMatchPythonOrderAndSeeds(t *testing.T) {
	seed := map[string]int{"remote": 75, "desktop": 210, "wait": 0}
	up := UpdateSteps(seed)
	var groups []string
	for _, s := range up {
		if len(groups) == 0 || groups[len(groups)-1] != s.Group {
			groups = append(groups, s.Group)
		}
	}
	want := []string{"Check", "Close Hermes", "Update", "Build", "Finish"}
	if len(groups) != len(want) {
		t.Fatalf("groups = %v", groups)
	}
	for i := range want {
		if groups[i] != want[i] {
			t.Errorf("group %d = %q, want %q", i, groups[i], want[i])
		}
	}
	seen := map[string]bool{}
	for _, s := range up {
		if seen[s.Key] {
			t.Errorf("duplicate key %q", s.Key)
		}
		seen[s.Key] = true
		if s.Key == "tile" {
			t.Error("tile must not be a core step (it is a hook now)")
		}
	}
	for _, s := range up {
		if s.Key == "remote" && s.DefaultS != 75 {
			t.Errorf("seed not applied: %+v", s)
		}
	}
	if len(CheckSteps(seed)) != 6 {
		t.Error("check list should have 6 steps")
	}
}
