package ui

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// vtConsole is a FakeConsole whose EnableVT can fail and counts restores.
type vtConsole struct {
	platform.FakeConsole
	vtErr    error
	restored int
}

func (c *vtConsole) EnableVT() (func(), error) {
	if c.vtErr != nil {
		return nil, c.vtErr
	}
	return func() { c.restored++ }, nil
}

func pick(t *testing.T, mut func(*config.Config, *vtConsole, map[string]string)) (Renderer, *vtConsole) {
	t.Helper()
	cfg := &config.Config{File: config.Defaults()}
	con := &vtConsole{FakeConsole: platform.FakeConsole{Terminal: true, Cols: 100, Rows: 30}}
	env := map[string]string{}
	mut(cfg, con, env)
	p := platform.NewFake()
	p.Console = con
	r := New(cfg, p, &bytes.Buffer{}, func(k string) string { return env[k] })
	return r, con
}

func TestNewPicksVTOnARealConsole(t *testing.T) {
	r, _ := pick(t, func(*config.Config, *vtConsole, map[string]string) {})
	if _, ok := r.(*VT); !ok {
		t.Fatalf("got %T, want *VT", r)
	}
	if !r.(*VT).st.color {
		t.Error("colour must be on by default")
	}
}

func TestNewPicksPlainWhenNotARealConsoleOrAskedTo(t *testing.T) {
	cases := map[string]func(*config.Config, *vtConsole, map[string]string){
		"pipe":    func(_ *config.Config, c *vtConsole, _ map[string]string) { c.Terminal = false },
		"--plain": func(cfg *config.Config, _ *vtConsole, _ map[string]string) { cfg.Run.Plain = true },
		"TERM=dumb": func(cfg *config.Config, _ *vtConsole, e map[string]string) {
			e["TERM"] = "dumb"
			cfg.Run.NoColor = true
		},
		"VT enable fails": func(_ *config.Config, c *vtConsole, _ map[string]string) { c.vtErr = errors.New("no vt") },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := pick(t, mut)
			if _, ok := r.(*Plain); !ok {
				t.Errorf("got %T, want *Plain", r)
			}
		})
	}
}

func TestNewNoColorKeepsVTLayoutWithoutColour(t *testing.T) {
	r, _ := pick(t, func(cfg *config.Config, _ *vtConsole, e map[string]string) {
		cfg.Run.NoColor = true
		e["NO_COLOR"] = "1"
	})
	v, ok := r.(*VT)
	if !ok {
		t.Fatalf("got %T, want *VT", r)
	}
	if v.st.color {
		t.Error("NO_COLOR must drop colour")
	}
}

func TestNewVTRestoresConsoleOnClose(t *testing.T) {
	r, con := pick(t, func(*config.Config, *vtConsole, map[string]string) {})
	r.Close()
	if con.restored != 1 {
		t.Errorf("restored = %d", con.restored)
	}
}
