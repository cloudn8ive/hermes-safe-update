package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The repo ships safe-update.example.yaml. It must decode strictly, equal the
// built-in defaults (it documents them), and mention every key of the schema.
const examplePath = "../../safe-update.example.yaml"

func TestExampleDecodesToDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{YAMLPath: examplePath, Getenv: noEnv})
	if err != nil {
		t.Fatalf("example must load strictly: %v", err)
	}
	if cfg.Sources.YAML == "" {
		t.Fatal("example was not found or is empty")
	}
	if !reflect.DeepEqual(cfg.File, Defaults()) {
		t.Errorf("example values drifted from Defaults()\n got: %+v\nwant: %+v", cfg.File, Defaults())
	}
}

func TestExampleMentionsEveryKey(t *testing.T) {
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, key := range yamlKeys(reflect.TypeOf(File{})) {
		re := regexp.MustCompile(`(?m)(^[ #-]*|[{,] *)` + regexp.QuoteMeta(key) + `:`)
		if !re.MatchString(text) {
			t.Errorf("example does not document key %q", key)
		}
	}
}

func TestExampleHookSnippetIsValid(t *testing.T) {
	// The commented hook example must be valid once uncommented.
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	in := false
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(l, "# BEGIN HOOK EXAMPLE") {
			in = true
			continue
		}
		if strings.HasPrefix(l, "# END HOOK EXAMPLE") {
			break
		}
		if in {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(l, "#"), " "))
		}
	}
	if len(lines) == 0 {
		t.Fatal("no hook example block found")
	}
	p := filepath.Join(t.TempDir(), "hooks.yaml")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{YAMLPath: p, Getenv: noEnv})
	if err != nil {
		t.Fatalf("uncommented hook example is invalid: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if len(cfg.Hooks) == 0 {
		t.Error("hook example defines no hooks")
	}
}

func yamlKeys(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, tag)
		ft := f.Type
		if ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, yamlKeys(ft)...)
		}
	}
	return out
}
