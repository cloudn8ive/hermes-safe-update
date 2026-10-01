//go:build windows

package platform

import (
	"encoding/base64"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
)

// Parity with the Python updater (task C3): the cua-driver logon task XML is
// decoded (UTF-16 with/without BOM, or UTF-8) and searched exactly as
// cua_task_binary() does.
func TestParityCuaTaskBinary(t *testing.T) {
	var cases []struct {
		Name string
		B64  string
		Want *string // nil in the fixture = the schtasks call itself failed (not used)
	}
	testutil.ParityFixture(t, "cua_task", &cases)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			raw, err := base64.StdEncoding.DecodeString(c.B64)
			if err != nil {
				t.Fatal(err)
			}
			got := taskExe(decodeSchtasks(raw))
			want := ""
			if c.Want != nil {
				want = *c.Want
			}
			if got != want {
				t.Errorf("task XML %q: Go %q, Python %q", c.Name, got, want)
			}
		})
	}
}
