package settings

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

// Rules are the key lists from config (settings.keep_new / settings.union),
// matched with config.MatchAny (`*` is the only wildcard, D8).
type Rules struct {
	KeepNew []string
	Union   []string
}

// RulesFrom returns the merge rules of a config.
func RulesFrom(s config.Settings) Rules { return Rules{KeepNew: s.KeepNew, Union: s.Union} }

// BuildPlan decides every key (brief "Settings migration (spec)", D10):
//   - only in old: copy (add); only in new: untouched
//   - identical: nothing
//   - keep_new keys: the new value stays
//   - union keys: objects / [id, value] pair lists are unioned, new wins per
//     entry; a value that is not such JSON falls back to the plain rule
//   - plain prefs: old wins on the first migration after an origin change,
//     new wins on later runs
//
// It returns the plan (key names only) and the values to write into the
// target origin. Values never leave this package except to the migrator.
func BuildPlan(source string, target Target, old, newer map[string]string, r Rules, firstRun bool) (Plan, map[string]string) {
	p := Plan{Source: source, Target: target, FirstRun: firstRun}
	writes := map[string]string{}
	keys := make([]string, 0, len(old)+len(newer))
	for k := range old {
		keys = append(keys, k)
	}
	for k := range newer {
		if _, ok := old[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		ov, inOld := old[k]
		nv, inNew := newer[k]
		kp := KeyPlan{Key: k}
		switch {
		case !inOld:
			kp.Action = ActOnlyInNew
		case !inNew:
			kp.Action = ActAdd
			writes[k] = ov
		case ov == nv:
			kp.Action = ActSame
		case config.MatchAny(r.KeepNew, k):
			kp.Action, kp.Conflict = ActKeepNew, true
		default:
			kp.Conflict = true
			merged, ok := "", false
			if config.MatchAny(r.Union, k) {
				merged, ok = unionValues(k, ov, nv)
			}
			switch {
			case ok && jsonEqual(merged, nv):
				// new already holds every old entry: nothing to do, no conflict
				kp.Action, kp.Conflict = ActSame, false
			case ok:
				kp.Action = ActUnion
				writes[k] = merged
			case firstRun:
				kp.Action = ActOldWins
				writes[k] = ov
			default:
				kp.Action = ActNewWins
			}
		}
		p.Keys = append(p.Keys, kp)
	}
	return p, writes
}

// Conflicts returns the names of keys whose values differed on both sides.
func Conflicts(p Plan) []string {
	var out []string
	for _, k := range p.Keys {
		if k.Conflict {
			out = append(out, k.Key)
		}
	}
	return out
}

// Counts returns the number of keys per action.
func (p Plan) Counts() map[Action]int {
	c := map[Action]int{}
	for _, k := range p.Keys {
		c[k.Action]++
	}
	return c
}

// unionValues merges two JSON values of the localStorage key `key` the way
// the proven migrator did: two objects -> old keys in old order (new value
// where new has it), then new-only keys; two [string, value] pair lists ->
// old-only entries in old order, then every new entry in new order (new wins
// per identity and moves to the end). Anything else -> ok=false. Raw member
// values are kept byte for byte.
//
// A pair-list entry's identity is what the app keys the list by when it
// loads it (pairIdentity): element 0 for most lists, connection+profile+id
// for sessionOwnerHints.v1. An old entry is dropped only when the new list
// holds the same identity; among old-only entries with the same identity the
// last one is kept at its last position, as the app's Map load does
// (delete + set).
func unionValues(key, oldRaw, newRaw string) (string, bool) {
	if oo, ok := decodeObject(oldRaw); ok {
		no, ok := decodeObject(newRaw)
		if !ok {
			return "", false
		}
		idx := map[string]int{}
		out := make([]member, 0, len(oo)+len(no))
		for _, m := range oo {
			idx[m.key] = len(out)
			out = append(out, m)
		}
		for _, m := range no {
			if i, seen := idx[m.key]; seen {
				out[i].val = m.val
				continue
			}
			idx[m.key] = len(out)
			out = append(out, m)
		}
		return encodeObject(out), true
	}
	op, ok := decodePairs(oldRaw)
	if !ok {
		return "", false
	}
	np, ok := decodePairs(newRaw)
	if !ok {
		return "", false
	}
	identity := pairIdentity(key)
	inNew := map[string]bool{}
	for _, m := range np {
		inNew[identity(m)] = true
	}
	lastOld := map[string]int{}
	for i, m := range op {
		lastOld[identity(m)] = i
	}
	var b bytes.Buffer
	b.WriteByte('[')
	n := 0
	emit := func(m member) {
		if n > 0 {
			b.WriteByte(',')
		}
		n++
		k, _ := json.Marshal(m.key)
		b.WriteByte('[')
		b.Write(k)
		b.WriteByte(',')
		b.Write(m.val)
		b.WriteByte(']')
	}
	for i, m := range op {
		id := identity(m)
		if !inNew[id] && lastOld[id] == i {
			emit(m)
		}
	}
	for _, m := range np {
		emit(m)
	}
	b.WriteByte(']')
	return b.String(), true
}

// sessionOwnerHintsKey is keyed by the app by connection+profile+session id
// (apps/desktop/src/store/session.ts sessionOwnerHintKey), not by id alone.
const sessionOwnerHintsKey = "hermes.desktop.sessionOwnerHints.v1"

// pairIdentity returns the identity function of a pair-list key's entries.
func pairIdentity(key string) func(member) string {
	if key != sessionOwnerHintsKey {
		return func(m member) string { return m.key }
	}
	return sessionOwnerHintIdentity
}

// sessionOwnerHintIdentity mirrors sessionOwnerHintKey: JSON of
// [trim(connectionId), trim(profile) || "default", id]. A route the app
// cannot read (no string connectionId/profile; the app skips it on load) is
// identified by the whole entry, so it is never merged with another one.
func sessionOwnerHintIdentity(m member) string {
	var route struct {
		ConnectionID *string `json:"connectionId"`
		Profile      *string `json:"profile"`
	}
	if err := json.Unmarshal(m.val, &route); err != nil || route.ConnectionID == nil || route.Profile == nil {
		return "raw\x00" + m.key + "\x00" + string(m.val)
	}
	profile := strings.TrimSpace(*route.Profile)
	if profile == "" {
		profile = "default"
	}
	id, _ := json.Marshal([]string{strings.TrimSpace(*route.ConnectionID), profile, strings.TrimSpace(m.key)})
	return string(id)
}

type member struct {
	key string
	val json.RawMessage
}

// decodeObject decodes a JSON object keeping member order; later duplicate
// keys replace earlier ones (as JSON.parse does).
func decodeObject(s string) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []member
	idx := map[string]int{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, false
		}
		k, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		if i, ok := idx[k]; ok {
			out[i].val = v
			continue
		}
		idx[k] = len(out)
		out = append(out, member{k, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false // trailing data
	}
	return out, true
}

func encodeObject(ms []member) string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.String()
}

// decodePairs decodes a JSON array whose every element is [string, value].
func decodePairs(s string) ([]member, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(s), &arr); err != nil || arr == nil {
		return nil, false
	}
	out := make([]member, 0, len(arr))
	for _, e := range arr {
		var pair []json.RawMessage
		if err := json.Unmarshal(e, &pair); err != nil || len(pair) != 2 {
			return nil, false
		}
		var k string
		if err := json.Unmarshal(pair[0], &k); err != nil {
			return nil, false
		}
		out = append(out, member{k, pair[1]})
	}
	return out, true
}

// jsonEqual reports semantic JSON equality (object member order ignored).
func jsonEqual(a, b string) bool {
	if a == b {
		return true
	}
	var x, y any
	da := json.NewDecoder(bytes.NewReader([]byte(a)))
	da.UseNumber()
	db := json.NewDecoder(bytes.NewReader([]byte(b)))
	db.UseNumber()
	if da.Decode(&x) != nil || db.Decode(&y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
