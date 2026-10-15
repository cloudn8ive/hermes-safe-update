package main

import (
	"strings"
	"testing"
)

// --channel is a flag of run and check; the help lists it.
func TestHelpListsTheChannelFlag(t *testing.T) {
	i := strings.Index(helpText, "--channel CHANNEL")
	if i < 0 {
		t.Fatal("help does not list --channel")
	}
	entry := helpText[i:]
	if j := strings.Index(entry, "\n\n"); j >= 0 {
		entry = entry[:j]
	}
	for _, want := range []string{"follow", "stable", "main", "update.channel"} {
		if !strings.Contains(strings.SplitN(entry, "\n  --", 2)[0], want) {
			t.Errorf("the --channel entry does not mention %q:\n%s", want, entry)
		}
	}
}

func TestParseChannelFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--channel", "stable"}, {"--channel=main", "check"}, {"check", "--channel", "follow"}, {"run", "--channel", "stable", "--unattended"}, {"--check", "--channel", "stable"},
	} {
		o, err := parseArgs(args)
		if err != nil || !o.set["channel"] || (o.command != "run" && o.command != "check") {
			t.Errorf("%v: %+v, %v", args, o, err)
		}
	}
	o, _ := parseArgs([]string{"--channel", "stable"})
	if o.channel != "stable" {
		t.Errorf("channel = %q", o.channel)
	}
	for _, args := range [][]string{
		{"--channel", "stable", "list-backups"}, {"migrate-settings", "--channel", "main"}, {"mirror", "status", "--channel", "main"},
	} {
		if _, err := parseArgs(args); err == nil || !strings.Contains(err.Error(), "--channel applies to run and check only") {
			t.Errorf("%v: %v", args, err)
		}
	}
}
