// Package gc runs Hermes' own dependency-generation collectors under
// Hermes' managed Python, with the delete step replaced by "move locked
// files to a trash dir" so Windows image locks cannot stop the cleanup
// (python-behaviour §6.2). The Python script is embedded. Missing managed
// Python = WARN skip (D7). On macOS/Linux the step is N/A.
//
// Ownership: this file = T1; everything else (including the embedded
// script under gc/script/) = I3.
package gc

import "context"

// Result of a dry run or a real run.
type Result struct {
	Count   int    // generations removed (or that would be)
	MB      int    // disk freed (or unique MB that would be freed)
	Line    string // summary row text
	Lines   []string
	Skipped string // non-empty = not run, with the reason (shown as an info row)
}

// Collector previews or runs the cleanup.
type Collector interface {
	Run(ctx context.Context, dryRun bool) (Result, error)
}
