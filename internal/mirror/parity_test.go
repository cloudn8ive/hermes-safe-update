package mirror

import (
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

func TestParityAgeText(t *testing.T) {
	var cases []struct {
		AgoS *float64 `json:"ago_s"`
		Want string
	}
	testutil.ParityFixture(t, "age_text", &cases)
	now := time.Unix(1_790_000_000, 0)
	for _, c := range cases {
		var last time.Time
		if c.AgoS != nil {
			last = now.Add(-time.Duration(*c.AgoS * float64(time.Second)))
		}
		if got := AgeText(last, now); got != c.Want {
			t.Errorf("AgeText(ago=%v) = %q, Python %q", c.AgoS, got, c.Want)
		}
	}
}
