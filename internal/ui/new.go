package ui

import (
	"io"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// New picks the renderer once (design.md §7): the VT renderer when stdout is a
// real console, VT processing could be enabled, and neither --plain,
// HERMES_SAFE_UPDATE_PLAIN nor TERM=dumb asks for plain text; NO_COLOR keeps
// the VT layout without colour. Anything else gets the plain renderer. No
// program logic may depend on which one is returned.
//
// w is where the run is drawn (stderr: stdout stays free for data); getenv
// is os.Getenv or a test fake.
func New(cfg *config.Config, p *platform.Platform, w io.Writer, getenv func(string) string) Renderer {
	con := p.Console
	if cfg.Run.Plain || getenv("TERM") == "dumb" || con == nil || !con.IsTerminal() {
		return NewPlain(w, con)
	}
	restore, err := con.EnableVT()
	if err != nil {
		return NewPlain(w, con)
	}
	noColor := cfg.Run.NoColor || getenv("NO_COLOR") != ""
	return NewVT(w, p, cfg.Theme, Options{Color: !noColor, Restore: restore})
}
