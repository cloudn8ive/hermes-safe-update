package hermes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ToolsFactsFile is Hermes' record of the managed tools it keeps in
// <home>/tools: packages.<name>.entry is the folder name of the live version
// (pm/cli.py `pm gc` is the only thing that ever removes a superseded one, so
// after a pin bump two python-* or git-* folders sit side by side).
const ToolsFactsFile = "facts.json"

// toolFacts is the part of tools/facts.json this tool reads.
type toolFacts struct {
	Packages map[string]struct {
		Entry string `json:"entry"`
	} `json:"packages"`
}

// toolEntry returns packages.<pkg>.entry from <home>/tools/facts.json, or ""
// when the file or the key is missing, unreadable or not a plain folder name.
func toolEntry(home, pkg string) string {
	b, err := os.ReadFile(filepath.Join(home, "tools", ToolsFactsFile))
	if err != nil {
		return ""
	}
	var f toolFacts
	if json.Unmarshal(b, &f) != nil {
		return ""
	}
	e := strings.TrimSpace(f.Packages[pkg].Entry)
	if e == "" || e == "." || e == ".." || strings.ContainsAny(e, `/\`) || filepath.Base(e) != e {
		return ""
	}
	return e
}

// pickManaged chooses one executable out of the managed-tool glob patterns
// (each has one wildcard folder, e.g. <home>/tools/python-*/python.exe):
//
//  1. the folder Hermes' own facts.json names for pkg ("python", "git"), when
//     it fits a pattern's wildcard and the executable is on disk;
//  2. else the match whose folder name is the highest VERSION (numeric parts
//     compared as numbers: python-3.14.10 beats python-3.14.7);
//  3. ties and non-numeric differences fall back to plain name order, the
//     behaviour before facts.json was consulted.
//
// "" if nothing exists.
func pickManaged(home, pkg string, patterns []string) string {
	if e := toolEntry(home, pkg); e != "" {
		for _, pat := range patterns {
			if p := withWildcardFolder(pat, e); p != "" && exists(p) {
				return p
			}
		}
	}
	var all []string
	for _, pat := range patterns {
		m, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, p := range m {
			if exists(p) {
				all = append(all, p)
			}
		}
	}
	if len(all) == 0 {
		return ""
	}
	sort.SliceStable(all, func(i, j int) bool { return versionLess(all[i], all[j]) })
	return all[len(all)-1]
}

// withWildcardFolder replaces the first path element of pattern that holds a
// wildcard by entry, provided entry matches that element. "" otherwise.
func withWildcardFolder(pattern, entry string) string {
	sep := string(filepath.Separator)
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	for i, part := range parts {
		if !strings.ContainsAny(part, "*?[") {
			continue
		}
		if ok, err := filepath.Match(part, entry); err != nil || !ok {
			return ""
		}
		parts[i] = entry
		return strings.Join(parts, sep)
	}
	return ""
}

// versionLess orders two paths by natural (version-aware) order: runs of
// digits compare as numbers, everything else as text; plain string order
// breaks a tie so the result is deterministic.
func versionLess(a, b string) bool {
	if c := naturalCompare(a, b); c != 0 {
		return c < 0
	}
	return a < b
}

// naturalCompare returns -1, 0 or 1.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := isDigit(a[0]), isDigit(b[0])
		if da && db {
			na, ra := splitDigits(a)
			nb, rb := splitDigits(b)
			na, nb = strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			switch {
			case len(na) != len(nb):
				if len(na) < len(nb) {
					return -1
				}
				return 1
			case na != nb:
				if na < nb {
					return -1
				}
				return 1
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}
			return 1
		}
		a, b = a[1:], b[1:]
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	}
	return 1
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func splitDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}
