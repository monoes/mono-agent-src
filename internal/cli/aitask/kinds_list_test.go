package aitask

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
	_ "github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/storage"
)

// `aitask kinds` needs no database, no profile and no operator check, and lists every kind.
func TestKindsListsEveryKindWithItsContract(t *testing.T) {
	d := &Deps{
		JSONOutput:      func() bool { return true },
		RequireOperator: func(string) error { t.Error("kinds asked for the operator"); return nil },
		OpenDB:          func() (*storage.Database, error) { t.Error("kinds opened the database"); return nil, nil },
	}
	out, err := run(t, d, "kinds")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Kinds []kindInfo `json:"kinds"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	byName := map[string]kindInfo{}
	for _, k := range got.Kinds {
		byName[k.Name] = k
	}
	for _, name := range []string{"generic", "application-apply", "workflow-generate", "recording-analyze", "failed-run-fix"} {
		k, ok := byName[name]
		if !ok || k.Description == "" || k.DefaultAccess == "" || len(k.ContextClasses) != len(aitask.ContextSources) ||
			k.ResultSchema.Name != name || len(k.ResultSchema.Required) == 0 {
			t.Errorf("%s: %+v", name, k)
		}
	}
	if len(got.Kinds) != len(aitask.Names()) {
		t.Errorf("%d kinds listed, %d registered", len(got.Kinds), len(aitask.Names()))
	}
}
