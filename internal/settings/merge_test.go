package settings

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

func testRules() Rules {
	s := config.Defaults().Settings
	return Rules{KeepNew: s.KeepNew, Union: s.Union}
}

func actionOf(p Plan, key string) (KeyPlan, bool) {
	for _, k := range p.Keys {
		if k.Key == key {
			return k, true
		}
	}
	return KeyPlan{}, false
}

func TestBuildPlanRules(t *testing.T) {
	old := map[string]string{
		"only.old":   "1",
		"same":       "x",
		"plain.pref": "old",
		"hermes.desktop.lastRoute.profile.default":  "/old",
		"hermes.desktop.lastRoute.profile.work":     "/old-work", // second profile via the * glob
		"hermes.desktop.freshDraftKey":              "d-old",
		"hermes.desktop.sessionSeenCounts":          `{"s1":1,"s2":2}`,
		"hermes.desktop.threadScroll.v1.profile.ab": `[["s1",10],["s2",20]]`,
		"hermes.desktop.toolDisclosure.v1":          `not json`,
		"hermes.desktop.unreadFinishedSessions":     `["s1"]`, // not a pair list
		"hermes.desktop.sessionOwnerHints.v1":       `{"s1":"a"}`,
	}
	newer := map[string]string{
		"same":       "x",
		"plain.pref": "new",
		"only.new":   "n",
		"hermes.desktop.lastRoute.profile.default":  "/new",
		"hermes.desktop.lastRoute.profile.work":     "/new-work",
		"hermes.desktop.freshDraftKey":              "d-new",
		"hermes.desktop.sessionSeenCounts":          `{"s2":5,"s3":3}`,
		"hermes.desktop.threadScroll.v1.profile.ab": `[["s2",99],["s3",30]]`,
		"hermes.desktop.toolDisclosure.v1":          `{}`,
		"hermes.desktop.unreadFinishedSessions":     `["s2"]`,
		"hermes.desktop.sessionOwnerHints.v1":       `{"s1":"a","s9":"b"}`, // already holds old's entries
	}
	tests := []struct {
		name     string
		firstRun bool
		key      string
		want     Action
		conflict bool
		write    *string
	}{
		{"one-side key is copied", true, "only.old", ActAdd, false, strp("1")},
		{"identical key is left", true, "same", ActSame, false, nil},
		{"plain pref old wins on first run", true, "plain.pref", ActOldWins, true, strp("old")},
		{"plain pref new wins later", false, "plain.pref", ActNewWins, true, nil},
		{"live-state key keeps new", true, "hermes.desktop.lastRoute.profile.default", ActKeepNew, true, nil},
		{"live-state glob matches other profiles", true, "hermes.desktop.lastRoute.profile.work", ActKeepNew, true, nil},
		{"live-state exact key keeps new", true, "hermes.desktop.freshDraftKey", ActKeepNew, true, nil},
		{"union of objects new wins per entry", true, "hermes.desktop.sessionSeenCounts", ActUnion, true, strp(`{"s1":1,"s2":5,"s3":3}`)},
		{"union of pair lists keeps old order, new wins and moves to end", true, "hermes.desktop.threadScroll.v1.profile.ab", ActUnion, true, strp(`[["s1",10],["s2",99],["s3",30]]`)},
		{"union of unparseable value falls back to plain rule (first run)", true, "hermes.desktop.toolDisclosure.v1", ActOldWins, true, strp(`not json`)},
		{"union of unparseable value falls back to plain rule (later)", false, "hermes.desktop.toolDisclosure.v1", ActNewWins, true, nil},
		{"union of non-pair arrays falls back to plain rule", true, "hermes.desktop.unreadFinishedSessions", ActOldWins, true, strp(`["s1"]`)},
		{"union already contained in new is same", true, "hermes.desktop.sessionOwnerHints.v1", ActSame, false, nil},
		{"key only in new is untouched", true, "only.new", ActOnlyInNew, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, writes := BuildPlan("file://", Target{URL: "http://127.0.0.1:47891"}, old, newer, testRules(), tc.firstRun)
			kp, ok := actionOf(p, tc.key)
			if !ok {
				t.Fatalf("key %q not in plan", tc.key)
			}
			if kp.Action != tc.want || kp.Conflict != tc.conflict {
				t.Errorf("got %s conflict=%v, want %s conflict=%v", kp.Action, kp.Conflict, tc.want, tc.conflict)
			}
			got, has := writes[tc.key]
			switch {
			case tc.write == nil && has:
				t.Errorf("unexpected write %q", got)
			case tc.write != nil && !has:
				t.Errorf("missing write, want %q", *tc.write)
			case tc.write != nil && !jsonEqualOrSame(got, *tc.write):
				t.Errorf("write = %q, want %q", got, *tc.write)
			}
		})
	}
}

func strp(s string) *string { return &s }

// Sandbox finding (C2): the real store's sessionOwnerHints.v1 holds several
// entries for one session id (one per profile/connection; the app keys the
// list by all three). Deduplicating old entries by id dropped them.
func TestUnionPairListKeepsSameIDEntriesWithDifferentValues(t *testing.T) {
	old := `[["s1",{"connectionId":"local","profile":"default"}],["s1",{"connectionId":"local","profile":"work"}],["s2",{"connectionId":"local","profile":"default"}],["s2",{"connectionId":"local","profile":"default"}]]`
	newer := `[["s3",{"connectionId":"local","profile":"default"}]]`
	got, ok := unionValues(sessionOwnerHintsKey, old, newer)
	if !ok {
		t.Fatal("not a union")
	}
	want := `[["s1",{"connectionId":"local","profile":"default"}],["s1",{"connectionId":"local","profile":"work"}],["s2",{"connectionId":"local","profile":"default"}],["s3",{"connectionId":"local","profile":"default"}]]`
	if got != want {
		t.Errorf("union =\n %s\nwant\n %s", got, want)
	}
	// idempotent: merging again into the result writes nothing new
	if again, _ := unionValues(sessionOwnerHintsKey, old, got); again != got {
		t.Errorf("second union changed the value:\n %s", again)
	}
}

// Review finding (C2): an old hint whose session id is also in the NEW list
// under another route must survive; the app keys the list by
// connection+profile+id, so they are two hints.
func TestUnionSessionOwnerHintsKeepsOldRouteWhenNewHasSameID(t *testing.T) {
	old := `[["s1",{"connectionId":"local","profile":"work"}]]`
	newer := `[["s1",{"connectionId":"local","profile":"default"}]]`
	got, ok := unionValues(sessionOwnerHintsKey, old, newer)
	if !ok {
		t.Fatal("not a union")
	}
	want := `[["s1",{"connectionId":"local","profile":"work"}],["s1",{"connectionId":"local","profile":"default"}]]`
	if got != want {
		t.Errorf("union =\n %s\nwant\n %s", got, want)
	}
}

// Control: same id and same route (after the app's trim / empty-profile
// normalisation) is one hint; the new value wins and appears once.
func TestUnionSessionOwnerHintsSameRouteNewWins(t *testing.T) {
	old := `[["s1",{"connectionId":" local ","profile":"","mode":"local"}],["s2",{"connectionId":"local","profile":"default"}]]`
	newer := `[["s1",{"connectionId":"local","profile":"default","mode":"remote"}]]`
	got, ok := unionValues(sessionOwnerHintsKey, old, newer)
	if !ok {
		t.Fatal("not a union")
	}
	want := `[["s2",{"connectionId":"local","profile":"default"}],["s1",{"connectionId":"local","profile":"default","mode":"remote"}]]`
	if got != want {
		t.Errorf("union =\n %s\nwant\n %s", got, want)
	}
}

// Other pair lists (threadScroll, ...) are Maps keyed by element 0 in the
// app: new wins per id, and of duplicate old ids the last one is kept, as
// the app's load (later set wins) would.
func TestUnionPairListOtherKeysUseElementZero(t *testing.T) {
	old := `[["s1",1],["s2",2],["s1",3]]`
	newer := `[["s2",20]]`
	got, ok := unionValues("hermes.desktop.threadScroll.v1.profile.ab", old, newer)
	if !ok {
		t.Fatal("not a union")
	}
	want := `[["s1",3],["s2",20]]`
	if got != want {
		t.Errorf("union =\n %s\nwant\n %s", got, want)
	}
}

// A route the app cannot read is identified by the whole entry: never
// merged away by a new entry with the same id.
func TestUnionSessionOwnerHintsUnreadableRouteKept(t *testing.T) {
	old := `[["s1","bogus"]]`
	newer := `[["s1",{"connectionId":"local","profile":"default"}]]`
	got, _ := unionValues(sessionOwnerHintsKey, old, newer)
	want := `[["s1","bogus"],["s1",{"connectionId":"local","profile":"default"}]]`
	if got != want {
		t.Errorf("union =\n %s\nwant\n %s", got, want)
	}
}

func jsonEqualOrSame(a, b string) bool {
	if a == b {
		return true
	}
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func TestBuildPlanWritesMatchPlanAndConflictsListed(t *testing.T) {
	old := map[string]string{"a": "1", "b": "2", "hermes.desktop.freshDraftKey": "x"}
	newer := map[string]string{"b": "3", "hermes.desktop.freshDraftKey": "y"}
	p, writes := BuildPlan("file://", Target{URL: "http://h"}, old, newer, testRules(), true)
	if p.Writes() != len(writes) {
		t.Errorf("Writes()=%d, len(writes)=%d", p.Writes(), len(writes))
	}
	if !p.FirstRun || p.Source != "file://" || p.Target.URL != "http://h" {
		t.Errorf("plan header wrong: %+v", p)
	}
	if got := Conflicts(p); !reflect.DeepEqual(got, []string{"b", "hermes.desktop.freshDraftKey"}) {
		t.Errorf("Conflicts = %v", got)
	}
}

func TestBuildPlanIsIdempotentAfterApplyingWrites(t *testing.T) {
	old := map[string]string{
		"a": "1", "plain": "old",
		"hermes.desktop.sessionSeenCounts": `{"s1":1}`,
	}
	newer := map[string]string{"plain": "new", "hermes.desktop.sessionSeenCounts": `{"s2":2}`}
	for _, first := range []bool{true, false} {
		_, writes := BuildPlan("file://", Target{URL: "http://h"}, old, newer, testRules(), first)
		after := map[string]string{}
		for k, v := range newer {
			after[k] = v
		}
		for k, v := range writes {
			after[k] = v
		}
		p2, w2 := BuildPlan("file://", Target{URL: "http://h"}, old, after, testRules(), first)
		if p2.Writes() != 0 || len(w2) != 0 {
			t.Errorf("firstRun=%v: re-plan writes %d (%v)", first, p2.Writes(), w2)
		}
	}
}

func TestPlanCounts(t *testing.T) {
	p := Plan{Keys: []KeyPlan{{Action: ActAdd}, {Action: ActAdd}, {Action: ActOldWins}, {Action: ActUnion}, {Action: ActSame}}}
	c := p.Counts()
	if c[ActAdd] != 2 || c[ActOldWins] != 1 || c[ActUnion] != 1 || c[ActSame] != 1 || c[ActKeepNew] != 0 {
		t.Errorf("Counts = %v", c)
	}
}
