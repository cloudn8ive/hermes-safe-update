package main

import (
	"strings"
	"testing"
)

// --no-elevate is about UAC, which only Windows has.
func TestHelpSaysNoElevateIsWindowsOnly(t *testing.T) {
	i := strings.Index(helpText, "--no-elevate")
	if i < 0 {
		t.Fatal("help does not list --no-elevate")
	}
	line := helpText[i:]
	if j := strings.Index(line, "\n\n"); j >= 0 {
		line = line[:j]
	}
	if !strings.Contains(strings.SplitN(line, "\n\n", 2)[0], "Windows only") {
		t.Errorf("--no-elevate entry does not say it is Windows only:\n%s", line)
	}
}
