package config

import (
	"testing"
)

func TestExpandPath(t *testing.T) {
	env := map[string]string{"LOCALAPPDATA": `C:\Users\you\AppData\Local`, "HOME": "/home/you"}
	getenv := func(k string) string { return env[k] }
	cases := []struct{ in, home, want string }{
		{"", "/home/you", ""},
		{"~", "/home/you", "/home/you"},
		{"~/x/y", "/home/you", "/home/you/x/y"},
		{`~\x`, `C:\Users\you`, `C:\Users\you\x`},
		{`%LOCALAPPDATA%\hermes`, "", `C:\Users\you\AppData\Local\hermes`},
		{`$LOCALAPPDATA/hermes`, "", `C:\Users\you\AppData\Local/hermes`},
		{`${HOME}/a`, "", "/home/you/a"},
		{`%UNSET%\a`, "", `%UNSET%\a`},
		{"/plain/path", "", "/plain/path"},
		{"~user/x", "/home/you", "~user/x"},
	}
	for _, c := range cases {
		if got := ExpandPath(c.in, c.home, getenv); got != c.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
