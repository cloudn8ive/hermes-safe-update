package ui

import "testing"

func TestCountdownText(t *testing.T) {
	for _, c := range []struct {
		sec  int
		want string
	}{
		{-5, "0s"}, {0, "0s"}, {1, "1s"}, {30, "30s"}, {119, "119s"},
		{120, "2 min"}, {121, "3 min"}, {180, "3 min"}, {599, "10 min"}, {600, "10 min"}, {601, "11 min"}, {3600, "60 min"},
	} {
		if got := CountdownText(c.sec); got != c.want {
			t.Errorf("CountdownText(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
}
