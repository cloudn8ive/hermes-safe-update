// Package ui renders the run: the VT console renderer with the user's look
// (python-behaviour.md §5) and a plain renderer for pipes, NO_COLOR, --plain
// and non-console hosts. No program logic may depend on which renderer is
// active (rule 9).
//
// Ownership: this file = T1 (interface); everything else = I2.
package ui

import (
	"context"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// Level of a message line.
type Level int

const (
	LevelInfo   Level = iota // plain text
	LevelNote                // "i note: ..."
	LevelWarn                // "! ..." (counted in the summary's Warnings row)
	LevelStop                // "× STOP ..."
	LevelOK                  // "√ ..."
	LevelResult              // RESULT line (ok/problem decided by Row.OK)
)

// Row is one key/value line ("disk free: 120 GB (enough)"). Rows replace the
// Python's regex rewrites of log text (§5.8): callers emit structured rows.
type Row struct {
	Key   string
	Value string // may contain "\n" for continuation lines
	// Verdict overrides the automatic dot colour: nil = derive from Value.
	Verdict *Verdict
	Nested  bool // no dot, indented
}

// Verdict colours a row's dot.
type Verdict int

const (
	VerdictNeutral Verdict = iota
	VerdictGood
	VerdictAttention
	VerdictBad
)

// Card is the summary at the end of a run.
type Card struct {
	OK       *bool // true √, false ×, nil i
	Headline string
	Rows     []Row
}

// PromptSpec is a single-key question with a timeout.
type PromptSpec struct {
	Text           string        // wrapped by the renderer
	Keys           []rune        // accepted keys, lower-case (D3: everything else is ignored)
	Timeout        time.Duration // 0 = no timeout
	CountdownLabel string        // "auto-cancel in", "checking again in", "skipping in"
	// AbortOnCtrlC: Ctrl+C returns Answer{Abort: true}.
	AbortOnCtrlC bool
}

// Answer of a prompt.
type Answer struct {
	Key      rune // one of PromptSpec.Keys, or 0
	TimedOut bool
	Abort    bool // Ctrl+C
}

// Renderer is everything the flow may show. Implementations are safe for
// concurrent use (the update output pump and the ticker both call them).
type Renderer interface {
	// Start prints the header (title, date top-right) and sets up the footer.
	Start(title string, steps []timings.Step, est *timings.Estimator, banners bool)
	// Stage moves the footer to step key; prints a numbered banner when the
	// group changes (banners on).
	Stage(key string)
	// Section prints a "== Title" style rule with its icon.
	Section(title string)
	// Line prints a message at a level.
	Line(level Level, text string)
	// Rows prints aligned key/value rows.
	Rows(rows ...Row)
	// Output prints one line of child output (`hermes update`) in the gutter.
	Output(line string)
	// Command prints "» command  <argv>".
	Command(argv string)
	// Waiting shows a paused footer ("Waiting for your answer", countdown).
	Waiting(label string, deadline time.Time, countdownLabel string)
	// Prompt asks a question and waits for an accepted key or the timeout.
	Prompt(ctx context.Context, p PromptSpec) (Answer, error)
	// Complete marks the run finished (ok=nil unknown) and freezes the footer.
	Complete(ok *bool)
	// Summary prints the card.
	Summary(c Card)
	// Warnings returns how many LevelWarn lines were shown.
	Warnings() int
	// Close restores the console (cursor, colours, mode, progress).
	Close()
}
