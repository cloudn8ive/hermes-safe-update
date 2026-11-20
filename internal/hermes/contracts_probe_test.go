package hermes

import (
	"reflect"
	"testing"
)

func TestFTSStoredFormMatchesHermes(t *testing.T) {
	// hermes_state_fts.py:19-24: spacing collapsed, none around ( ) and
	// commas, the quoted name of an ALTER TABLE RENAME round trip unquoted.
	for in, want := range map[string]string{
		"CREATE VIRTUAL TABLE messages_fts USING fts5(\n    content,\n    tool_name,\n    tool_calls,\n    content='messages_fts_src',\n    content_rowid='id'\n)": "CREATE VIRTUAL TABLE messages_fts USING fts5(content,tool_name,tool_calls,content='messages_fts_src',content_rowid='id')",
		`CREATE VIRTUAL TABLE "messages_fts" USING fts5(content)`: "CREATE VIRTUAL TABLE messages_fts USING fts5(content)",
		`CREATE VIEW "messages_fts_src" AS SELECT 1`:              "CREATE VIEW messages_fts_src AS SELECT 1",
		"": "",
	} {
		if got := FTSStoredForm(in); got != want {
			t.Errorf("FTSStoredForm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFTSLayoutDriftMatchesHermes(t *testing.T) {
	cur := map[string]string{
		FTSBaseTable:    "CREATE VIRTUAL TABLE messages_fts USING fts5(\n content,\n tool_name,\n tool_calls,\n content='messages_fts_src',\n content_rowid='id'\n)",
		FTSTrigramTable: "CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content, tool_name, content='messages_fts_trigram_src', content_rowid='id', tokenize='trigram')",
	}
	cases := []struct {
		name           string
		mod            func(m map[string]string)
		drift, unknown []string
	}{
		{"current", func(map[string]string) {}, nil, nil},
		{"no trigram index", func(m map[string]string) { delete(m, FTSTrigramTable) }, nil, nil},
		{"nothing", func(m map[string]string) { clear(m) }, nil, nil},
		{"inline base", func(m map[string]string) { m[FTSBaseTable] = "CREATE VIRTUAL TABLE messages_fts USING fts5(content)" }, []string{FTSBaseTable}, nil},
		{"v10 external base", func(m map[string]string) {
			m[FTSBaseTable] = "CREATE VIRTUAL TABLE messages_fts USING fts5(content, content=messages, content_rowid=id)"
		}, []string{FTSBaseTable}, nil},
		{"base reading messages: realigned on open, not work", func(m map[string]string) {
			m[FTSBaseTable] = "CREATE VIRTUAL TABLE messages_fts USING fts5(content,tool_name,tool_calls,content='messages',content_rowid='id')"
		}, nil, nil},
		{"unknown older base shape without tool_name", func(m map[string]string) {
			m[FTSBaseTable] = "CREATE VIRTUAL TABLE messages_fts USING fts5(content,foo)"
		}, []string{FTSBaseTable}, nil},
		{"inline trigram", func(m map[string]string) {
			m[FTSTrigramTable] = "CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content, tokenize='trigram')"
		}, []string{FTSTrigramTable}, nil},
		{"trigram with tool_calls", func(m map[string]string) {
			m[FTSTrigramTable] = "CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,tool_name,tool_calls,content='messages_fts_trigram_src',content_rowid='id',tokenize='trigram')"
		}, []string{FTSTrigramTable}, nil},
		{"shape with TOOL_CALLS in any case is old (lower-cased check)", func(m map[string]string) {
			m[FTSTrigramTable] = "CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,TOOL_CALLS,tokenize='trigram')"
		}, []string{FTSTrigramTable}, nil},
		{"newer release's trigram", func(m map[string]string) {
			m[FTSTrigramTable] = "CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,tool_name,role,content='messages_fts_trigram_src',content_rowid='id',tokenize='trigram')"
		}, nil, []string{FTSTrigramTable}},
		{"both old", func(m map[string]string) {
			m[FTSBaseTable] = "CREATE VIRTUAL TABLE messages_fts USING fts5(content)"
			m[FTSTrigramTable] = "CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,tokenize='trigram')"
		}, []string{FTSBaseTable, FTSTrigramTable}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range cur {
				m[k] = v
			}
			c.mod(m)
			d, u := FTSLayoutDrift(m)
			if !reflect.DeepEqual(d, c.drift) || !reflect.DeepEqual(u, c.unknown) {
				t.Errorf("drift %v unknown %v, want %v %v", d, u, c.drift, c.unknown)
			}
		})
	}
}

func TestProbeVerifiedIsTheHermesLineTheCheckWasReadFrom(t *testing.T) {
	for v, want := range map[string]bool{
		"Hermes Agent v0.23.1 (2026.11.5)": false, // the banner is not a version: callers pass `hermes --version`
		"v0.23.0":                          true,
		"v0.23.1":                          true,
		"0.23.1+canary.1109":               true,
		"v0.23.0+canary.7":                 true,
		"0.23.1+12.gabc1234":               false, // derived build = unreleased main code
		"0.23.0+5.gabc1234":                false,
		"v0.23.2":                          true, // source diff vs v0.23.1: FTS files identical
		"0.23.2+canary.3":                  true,
		"0.23.2+4.gabc1234":                false,
		"v0.23.3":                          true, // re-read: persistence/ moved the code, same terms
		"0.23.3+canary.2":                  true,
		"0.23.3+2.gabc1234":                false,
		"0.23.4":                           false,
		"v0.23.4+canary.1":                 false,
		"0.23.9+5.gabc1234":                false,
		"0.23":                             false,
		"0.23.10":                          false,
		"0.23.1-rc1":                       false,
		"v0.22.7":                          false,
		"v0.24.0":                          false,
		"v1.23.0":                          false,
		"":                                 false,
	} {
		if got := ProbeVerified(v); got != want {
			t.Errorf("ProbeVerified(%q) = %v, want %v", v, got, want)
		}
	}
}
