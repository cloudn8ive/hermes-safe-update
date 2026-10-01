package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrInvalid marks every configuration error (bad YAML, unknown key, value
// out of range). cmd maps it to the usage exit code.
var ErrInvalid = errors.New("invalid configuration")

// Run holds per-invocation switches that come from flags and env only.
type Run struct {
	Yes            bool // skip the final Y confirmation (never skips the idle check)
	Unattended     bool // no prompts at all; wait for idle, then proceed
	AcceptUntested bool // accept running on macOS/Linux without the prompt
	Plain          bool // plain text output (no VT footer, colours, progress)
	NoColor        bool // NO_COLOR or TERM=dumb
	NoRelaunch     bool
	Force          bool
	NoElevate      bool
}

// Sources records which files were actually loaded (empty = not present).
type Sources struct {
	YAML string
	JSON string
}

// Config is the resolved configuration handed to the rest of the program.
type Config struct {
	File
	Run     Run
	Machine MachineState
	// MachineErr is set when safe-update.json exists but cannot be read or
	// parsed. Machine is then empty; the run goes on (the file is tool-owned
	// state, not user config) and SaveMachineState keeps the bad file aside.
	MachineErr error
	Sources    Sources
}

// Overrides are the CLI flags; nil = flag not given.
type Overrides struct {
	ConfirmTimeoutS *int    // --delay
	Plain           *bool   // --plain
	LogLevel        *string // --log-level
	Yes             *bool
	Unattended      *bool
	AcceptUntested  *bool
	NoRelaunch      *bool
	Force           *bool
	NoElevate       *bool
	Origin          *string // migrate-settings --origin
}

// LoadOptions says where to read from. Empty paths are skipped. A missing
// YAML file is "no config" unless YAMLRequired (set for --config); a missing
// JSON file is empty state. Getenv defaults to os.Getenv.
type LoadOptions struct {
	YAMLPath     string
	YAMLRequired bool
	JSONPath     string
	Getenv       func(string) string
	Flags        Overrides
}

// Load resolves defaults < YAML < machine JSON < env < flags and validates
// the result, reporting every problem at once with file:line.
func Load(opt LoadOptions) (*Config, error) {
	getenv := opt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := &Config{File: Defaults()}

	var root *yaml.Node
	if opt.YAMLPath != "" {
		n, loaded, err := decodeYAML(opt.YAMLPath, &cfg.File)
		if err != nil {
			return nil, err
		}
		if !loaded && opt.YAMLRequired {
			if _, statErr := os.Stat(opt.YAMLPath); errors.Is(statErr, os.ErrNotExist) {
				return nil, fmt.Errorf("%w: config file %q does not exist (check the --config path)", ErrInvalid, opt.YAMLPath)
			}
		}
		if loaded {
			cfg.Sources.YAML = opt.YAMLPath
			root = n
		}
	}
	if len(cfg.Hooks) == 0 {
		cfg.Hooks = nil // `hooks: []` and no key mean the same
	}
	for i := range cfg.Hooks {
		if cfg.Hooks[i].TimeoutS == 0 {
			cfg.Hooks[i].TimeoutS = DefaultHookTimeoutS
		}
	}

	if opt.JSONPath != "" {
		st, err := LoadMachineState(opt.JSONPath)
		if err != nil {
			cfg.MachineErr = err // keep going with empty state; see design.md §4
		} else {
			cfg.Machine = st
			if _, statErr := os.Stat(opt.JSONPath); statErr == nil {
				cfg.Sources.JSON = opt.JSONPath
			}
			if st.Mirror != nil && st.Mirror.Path != "" {
				cfg.Mirror.Path = st.Mirror.Path
			}
		}
	}

	if getenv("HERMES_SAFE_UPDATE_PLAIN") != "" {
		cfg.Run.Plain = true
	}
	if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		cfg.Run.NoColor = true
	}

	applyFlags(cfg, opt.Flags)

	if err := cfg.File.Validate(); err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			ve.File = opt.YAMLPath
			if root != nil {
				for i := range ve.Problems {
					ve.Problems[i].Line = lineOf(root, ve.Problems[i].Path)
				}
			}
		}
		return nil, err
	}
	return cfg, nil
}

func applyFlags(cfg *Config, f Overrides) {
	setB := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	if f.ConfirmTimeoutS != nil {
		cfg.Tunables.ConfirmTimeoutS = *f.ConfirmTimeoutS
	}
	if f.LogLevel != nil {
		cfg.Logging.Level = *f.LogLevel
	}
	if f.Origin != nil {
		cfg.Settings.Origin = *f.Origin
	}
	setB(&cfg.Run.Plain, f.Plain)
	setB(&cfg.Run.Yes, f.Yes)
	setB(&cfg.Run.Unattended, f.Unattended)
	setB(&cfg.Run.AcceptUntested, f.AcceptUntested)
	setB(&cfg.Run.NoRelaunch, f.NoRelaunch)
	setB(&cfg.Run.Force, f.Force)
	setB(&cfg.Run.NoElevate, f.NoElevate)
}

// decodeYAML strictly decodes path into dst (pre-filled with defaults).
// loaded is false for a missing or empty file.
func decodeYAML(path string, dst *File) (*yaml.Node, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read config %q: %w", path, err)
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, false, nil
		}
		return nil, false, yamlError(path, data, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false, yamlError(path, data, err)
	}
	return &root, true, nil
}

func yamlError(path string, data []byte, err error) error {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.ReplaceAll(msg, "\n", "\n  ")
	hint := ""
	if hasTabIndent(data) {
		hint = "\n  hint: indent with spaces; YAML does not allow tab characters for indentation"
	}
	return fmt.Errorf("%w: %s: %s%s", ErrInvalid, path, msg, hint)
}

func hasTabIndent(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimLeft(line, " ")
		if len(trimmed) > 0 && trimmed[0] == '\t' {
			return true
		}
	}
	return false
}

// lineOf walks a dotted path like "hooks[0].run" or "settings.keep_new[2]"
// through the YAML tree and returns the line of the deepest node found.
func lineOf(root *yaml.Node, path string) int {
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	line := 0
	for _, seg := range strings.Split(path, ".") {
		name, idx := seg, -1
		if i := strings.IndexByte(seg, '['); i >= 0 && strings.HasSuffix(seg, "]") {
			name = seg[:i]
			if v, err := strconv.Atoi(seg[i+1 : len(seg)-1]); err == nil {
				idx = v
			}
		}
		if n.Kind != yaml.MappingNode {
			return line
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == name {
				line = n.Content[i].Line
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return line
		}
		n = next
		if idx >= 0 {
			if n.Kind != yaml.SequenceNode || idx >= len(n.Content) {
				return line
			}
			n = n.Content[idx]
			line = n.Line
		}
	}
	return line
}
