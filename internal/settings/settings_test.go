package settings

import (
	"io/fs"
	"testing"
)

func TestMigratorIsEmbedded(t *testing.T) {
	b, err := fs.ReadFile(MigratorFiles(), "main.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Error("main.js is empty")
	}
}

func TestPlanWritesCountsOnlyWritingActions(t *testing.T) {
	p := Plan{Keys: []KeyPlan{
		{Action: ActAdd}, {Action: ActOldWins}, {Action: ActUnion},
		{Action: ActSame}, {Action: ActKeepNew}, {Action: ActNewWins}, {Action: ActOnlyInNew},
	}}
	if p.Writes() != 3 {
		t.Errorf("Writes() = %d", p.Writes())
	}
}
