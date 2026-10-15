package hermes

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// `--set-channel` is listed in the help of a Hermes that can switch; the
// capability is separate from `--channel`.
func TestCapsSetChannel(t *testing.T) {
	if c := ParseCaps(readFixture(t, "update-help-channels.txt")); !c.SetChannel || !c.Channel {
		t.Errorf("channels help: %+v", c)
	}
	if c := ParseCaps(readFixture(t, "update-help-legacy.txt")); c.SetChannel {
		t.Errorf("legacy help: %+v", c)
	}
	if c := ParseCaps("[--branch BRANCH] [--channel CHANNEL] [--install-id]"); c.SetChannel || !c.Channel {
		t.Errorf("channel without set-channel: %+v", c)
	}
	if c := ParseCaps("[--set-channel CHANNEL]"); !c.SetChannel || c.Channel {
		t.Errorf("set-channel alone: %+v", c)
	}
}

func TestSetChannelConfirmationFixture(t *testing.T) {
	out := readFixture(t, "update-set-channel-stable.txt")
	m := SetChannelDoneRe.FindStringSubmatch(out)
	if m == nil || m[1] != "0f1e2d3c4b5a6978" || m[2] != "stable" {
		t.Fatalf("confirmation not read from %q: %v", out, m)
	}
	for _, no := range []string{"", "Update channel: stable\n", "Update channel for abc: main\n", "channels don't apply here\n"} {
		if SetChannelDoneRe.MatchString(no) {
			t.Errorf("%q read as a confirmation", no)
		}
	}
}

func TestSetChannelRunsHermesOwnCommandOnly(t *testing.T) {
	launcher := "hermes.exe"
	f := (&execx.Fake{}).On([]string{launcher, "update", "--set-channel", "stable"}, execx.Result{Output: readFixture(t, "update-set-channel-stable.txt")})
	r, err := SetChannel(context.Background(), f, launcher, "stable")
	if err != nil || r.Code != 0 || r.Name != "stable" || r.Done != "Update channel for 0f1e2d3c4b5a6978: stable" {
		t.Fatalf("got %+v, %v", r, err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Timeout <= 0 || calls[0].Env["PYTHONIOENCODING"] != "utf-8" {
		t.Errorf("calls: %+v", calls)
	}

	// Hermes' refusal: its exit code and text come back, no confirmation line
	f = (&execx.Fake{}).On([]string{launcher, "update", "--set-channel", "main"}, execx.Result{Code: 2, Output: readFixture(t, "update-set-channel-refused.txt")})
	r, err = SetChannel(context.Background(), f, launcher, "main")
	if err != nil || r.Code != 2 || r.Done != "" || !strings.Contains(r.Output, "channels don't apply here") {
		t.Errorf("refusal: %+v, %v", r, err)
	}
	// cannot start / timeout are errors
	f = (&execx.Fake{}).On([]string{launcher, "update", "--set-channel", "main"}, execx.Result{Code: execx.CodeTimeout, TimedOut: true})
	if _, err = SetChannel(context.Background(), f, launcher, "main"); err == nil {
		t.Error("a timeout is not an error")
	}
}

func TestCheckStableIsTheReadOnlyChannelCheck(t *testing.T) {
	launcher := "hermes.exe"
	f := (&execx.Fake{}).On([]string{launcher, "update", "--check", "--channel", "stable"}, execx.Result{Output: readFixture(t, "update-check-stable-rollback.txt")})
	ch, res, err := CheckStable(context.Background(), f, launcher)
	if err != nil || res.Code != 0 || ch.Verdict != VerdictRollback || ch.Label() != "stable v0.22.0" {
		t.Fatalf("got %+v %+v, %v", ch, res, err)
	}
	for _, c := range f.Calls() {
		if j := strings.Join(c.Argv, " "); strings.Contains(j, "--set-channel") || strings.Contains(j, "--yes") {
			t.Errorf("the check may write: %s", j)
		}
	}
	f = (&execx.Fake{}).On([]string{launcher, "update", "--check", "--channel", "stable"}, execx.Result{Code: execx.CodeTimeout, TimedOut: true})
	if _, _, err = CheckStable(context.Background(), f, launcher); err == nil {
		t.Error("a timeout is not an error")
	}
}
