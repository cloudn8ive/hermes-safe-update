//go:build linux || darwin

package platform

import (
	"bytes"
	"testing"
)

func newTitleConsole(tty bool) (*unixConsole, *bytes.Buffer) {
	var b bytes.Buffer
	return &unixConsole{out: &b, tty: func() bool { return tty }}, &b
}

// The title set while the tool runs must not outlive the run: EnableVT saves
// the terminal's title and the restore func (called by VT.Close) puts it back.
func TestUnixConsoleRestoresTheTitle(t *testing.T) {
	c, out := newTitleConsole(true)
	restore, err := c.EnableVT()
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != titlePush {
		t.Fatalf("EnableVT wrote %q, want the title push %q", out.String(), titlePush)
	}
	if err := c.SetTitle("Hermes Update Check · stopped"); err != nil {
		t.Fatal(err)
	}
	// a redraw re-enables VT (children reset the mode): no second push
	if r, _ := c.EnableVT(); r == nil {
		t.Fatal("EnableVT returned a nil restore")
	} else {
		r()
	}
	if got := out.String(); got != titlePush+"\x1b]0;Hermes Update Check · stopped\a" {
		t.Fatalf("a repeated EnableVT or its no-op restore wrote more: %q", got)
	}
	restore()
	restore() // twice is harmless
	want := titlePush + "\x1b]0;Hermes Update Check · stopped\a" + titlePop
	if out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}
	// a later run on the same console pushes again
	out.Reset()
	if _, err := c.EnableVT(); err != nil || out.String() != titlePush {
		t.Errorf("second run: %q %v", out.String(), err)
	}
}

func TestUnixConsoleLeavesTheTitleAloneOffATerminal(t *testing.T) {
	c, out := newTitleConsole(false)
	restore, _ := c.EnableVT()
	_ = c.SetTitle("x")
	restore()
	if out.Len() != 0 {
		t.Errorf("wrote %q to a pipe or file", out.String())
	}
}
