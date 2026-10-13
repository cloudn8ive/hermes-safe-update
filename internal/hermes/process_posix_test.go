package hermes

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// POSIX backends are started through the entry script, so the command line
// carries neither hermes.exe nor hermes_cli.
func TestClassifyPOSIXBackend(t *testing.T) {
	const home, checkout = "/home/u/.hermes", "/home/u/.hermes/hermes-agent"
	cases := []struct {
		name string
		p    platform.Process
		want string
	}{
		{"venv python running the entry script", platform.Process{Name: "python3", Exe: checkout + "/venv/bin/python3", Cmdline: checkout + "/venv/bin/python3 " + checkout + "/venv/bin/hermes serve --port 9119"}, ClassBackend},
		{"entry script itself", platform.Process{Name: "hermes", Exe: home + "/bin/hermes", Cmdline: home + "/bin/hermes serve --port 9119"}, ClassBackend},
		{"managed python, script on PATH name", platform.Process{Name: "python3", Exe: home + "/tools/python-3.12/bin/python3", Cmdline: "python3 /usr/local/bin/hermes serve"}, ClassBackend},
		{"argv0 looked up on PATH, kernel gave the exe", platform.Process{Name: "hermes", Exe: home + "/bin/hermes", Cmdline: "hermes serve"}, ClassBackend},
		// look-alikes that must stay unclaimed (they would be killed)
		{"editor opening a file named like the command", platform.Process{Name: "vim", Exe: "/usr/bin/vim", Cmdline: "vim " + checkout + "/venv/bin/hermes serve"}, ""},
		{"system python outside the install", platform.Process{Name: "python3", Exe: "/usr/bin/python3", Cmdline: "/usr/bin/python3 /opt/other/bin/hermes serve"}, ""},
		{"serve of another program under the install", platform.Process{Name: "python3", Exe: checkout + "/venv/bin/python3", Cmdline: checkout + "/venv/bin/python3 " + checkout + "/web/server.py serve"}, ""},
		{"hermes subcommand that is not serve", platform.Process{Name: "hermes", Exe: home + "/bin/hermes", Cmdline: home + "/bin/hermes update --check"}, ""},
		{"hermes in another home", platform.Process{Name: "hermes", Exe: "/home/other/.hermes/bin/hermes", Cmdline: "/home/other/.hermes/bin/hermes serve"}, ""},
		{"servers is not serve", platform.Process{Name: "hermes", Exe: home + "/bin/hermes", Cmdline: home + "/bin/hermes servers list"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.p, home, checkout, nil); got != c.want {
				t.Errorf("Classify = %q, want %q", got, c.want)
			}
		})
	}
}
