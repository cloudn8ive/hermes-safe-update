package update

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
)

// internal/mirror cannot import internal/hermes, so it keeps its own copy of
// Hermes' STABLE_TAG_RE. The two must agree on what a release tag is: this
// is the one place that sees both.
func TestStableTagShapeIsTheSameInHermesAndTheMirror(t *testing.T) {
	for _, tag := range []string{
		"v0.0.0", "v0.22.1", "v1.2.3", "v10.20.30", "v999.0.0", "v0.22.10", "v1.0.100",
		"", "v", "0.22.1", "v1", "v1.2", "v1.2.3.4", "v01.2.3", "v1.02.3", "v1.2.03", "v1000.0.0",
		"v2026.9.24", "v1.2.3+canary.20261001T000000Z", "v1.2.3-rc.1", "rc.1-v1.2.3", "V1.2.3", " v1.2.3", "v1.2.3\n",
		"v1.2.x", "main", "refs/tags/v1.2.3",
	} {
		if a, b := hermes.StableTagRe.MatchString(tag), mirror.StableTag(tag); a != b {
			t.Errorf("%q: hermes.StableTagRe=%v mirror.StableTag=%v", tag, a, b)
		}
	}
}
