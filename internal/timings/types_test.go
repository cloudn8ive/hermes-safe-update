package timings

import (
	"encoding/json"
	"testing"
)

func TestOrderedSecondsKeepsOrder(t *testing.T) {
	in := `{"started":"2026-09-30T08:00:00","kind":"update","ok":true,"total":12.5,
	"steps":{"remote":3.1,"procs":0.4,"local":1},"commits":2,"cond":{"source":"api","mirror":false},"seeded_from_log":true}`
	var r Record
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Steps) != 3 || r.Steps[0].Key != "remote" || r.Steps[2].Key != "local" || r.Steps[1].Seconds != 0.4 {
		t.Fatalf("steps = %+v", r.Steps)
	}
	if r.Cond == nil || r.Cond.Source != "api" || r.Cond.Mirror == nil || *r.Cond.Mirror {
		t.Errorf("cond = %+v", r.Cond)
	}
	out, err := json.Marshal(r.Steps)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"remote":3.1,"procs":0.4,"local":1}` {
		t.Errorf("marshal = %s", out)
	}
	if v, ok := r.Steps.Get("procs"); !ok || v != 0.4 {
		t.Errorf("Get = %v %v", v, ok)
	}
	if _, ok := r.Steps.Get("nope"); ok {
		t.Error("Get on missing key")
	}
}

func TestOrderedSecondsRejectsNonNumbers(t *testing.T) {
	var s OrderedSeconds
	if err := json.Unmarshal([]byte(`{"a":"x"}`), &s); err == nil {
		t.Error("expected an error")
	}
	if err := json.Unmarshal([]byte(`[1]`), &s); err == nil {
		t.Error("expected an error for an array")
	}
}
