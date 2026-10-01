//go:build windows

package platform

import "testing"

func TestApplyAppIDRejectsBadWindow(t *testing.T) {
	if code := ApplyAppID("HermesAgent.SafeUpdate", "not-a-number"); code != 2 {
		t.Errorf("bad hwnd text: exit %d, want 2", code)
	}
	if code := ApplyAppID("HermesAgent.SafeUpdate", "0"); code != 2 {
		t.Errorf("hwnd 0: exit %d, want 2", code)
	}
	// a number that is not a window handle
	if code := ApplyAppID("HermesAgent.SafeUpdate", "123456789"); code != 2 {
		t.Errorf("not a window: exit %d, want 2", code)
	}
}
