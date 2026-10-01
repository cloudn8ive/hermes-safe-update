package logx

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactPath(t *testing.T) {
	r := NewRedactor(`C:\Users\you`, `C:\Users\you\AppData\Local`)
	cases := map[string]string{
		`C:\Users\you\AppData\Local\hermes\state.db`: `%LOCALAPPDATA%\hermes\state.db`,
		`c:\users\YOU\AppData\Local\hermes`:          `%LOCALAPPDATA%\hermes`,
		`C:\Users\you\Documents\x`:                   `~\Documents\x`,
		`C:/Users/you/Documents/x`:                   `~/Documents/x`,
		`D:\other\place`:                             `D:\other\place`,
		`see C:\Users\you\a and C:\Users\you\b`:      `see ~\a and ~\b`,
		`C:\Users\yours\x`:                           `C:\Users\yours\x`,
	}
	for in, want := range cases {
		if got := r.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
	posix := NewRedactor("/home/you", "")
	if got := posix.String("/home/you/.hermes/logs"); got != "~/.hermes/logs" {
		t.Errorf("posix: %q", got)
	}
	if got := posix.String("/home/youngster/x"); got != "/home/youngster/x" {
		t.Errorf("prefix must end at a separator: %q", got)
	}
	if got := NewRedactor("", "").String("x"); got != "x" {
		t.Errorf("empty redactor changed text: %q", got)
	}
}

func TestHandlerRedactsMessagesAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	r := NewRedactor("/home/you", "")
	log := slog.New(NewHandler(&buf, slog.LevelDebug, r))
	log.Info("opened /home/you/.hermes/state.db", slog.String("path", "/home/you/x"), slog.Int("n", 3))
	out := buf.String()
	if strings.Contains(out, "/home/you/") {
		t.Errorf("home path leaked: %s", out)
	}
	if !strings.Contains(out, "~/.hermes/state.db") || !strings.Contains(out, "path=~/x") || !strings.Contains(out, "n=3") {
		t.Errorf("unexpected log line: %s", out)
	}
}

func TestOpenFileRotates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "safe-update.log")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 2000), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFile(p, 1000, 2)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf("old log not rotated to .1: %v", err)
	}
	st, _ := os.Stat(p)
	if st.Size() != 0 {
		t.Errorf("new log should be empty, size %d", st.Size())
	}
	// rotate twice more: keep only 2 old files
	for i := 0; i < 2; i++ {
		_ = os.WriteFile(p, bytes.Repeat([]byte("y"), 2000), 0o600)
		f, err = OpenFile(p, 1000, 2)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := os.Stat(p + ".3"); err == nil {
		t.Error("keep=2 must not leave a .3")
	}
	if _, err := os.Stat(p + ".2"); err != nil {
		t.Error(".2 expected")
	}
}
