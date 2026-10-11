package update

import (
	"regexp"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

var bannerRe = regexp.MustCompile(`(?m)^== (\d)/\d (\S[^\n]*)$`)

// `hermes update` output where the Python dependency step comes after the
// desktop build (newer Hermes): the stage banners only ever count up.
func TestPydepsAfterBuildNeverGoesBackToTheUpdateStage(t *testing.T) {
	h := newHarness(t)
	h.onUpdate(0, gcLockedOut(t), true)
	h.exists = func(string) bool { return false }
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	last := 0
	var seen []string
	for _, m := range bannerRe.FindAllStringSubmatch(h.output(), -1) {
		n := int(m[1][0] - '0')
		seen = append(seen, m[1]+" "+m[2])
		if n < last {
			t.Errorf("stage banner went back: %v", seen)
		}
		last = n
	}
	if !strings.Contains(strings.Join(seen, "|"), "4 Build") {
		t.Errorf("banners = %v", seen)
	}
}
