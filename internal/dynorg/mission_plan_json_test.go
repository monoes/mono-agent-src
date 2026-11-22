package dynorg

import (
	"encoding/json"
	"testing"
)

func TestPlanTaskAcceptsAcceptanceAsArray(t *testing.T) {
	var p PlanTask
	if err := json.Unmarshal([]byte(`{"ref":"a","title":"T","acceptance":["`+"`go test ./...`"+` exits 0"," go vet is clean ",""]}`), &p); err != nil {
		t.Fatalf("array acceptance refused: %v", err)
	}
	if want := "`go test ./...` exits 0\ngo vet is clean"; p.Acceptance != want {
		t.Fatalf("acceptance = %q, want %q", p.Acceptance, want)
	}
	var s PlanTask
	if err := json.Unmarshal([]byte(`{"ref":"a","acceptance":"one string"}`), &s); err != nil || s.Acceptance != "one string" {
		t.Fatalf("string acceptance: %q, %v", s.Acceptance, err)
	}
	if err := json.Unmarshal([]byte(`{"ref":"a","acceptance":7}`), &s); err == nil {
		t.Fatal("a number acceptance must still be refused")
	}
}

func TestSplitPartAcceptsAcceptanceAsArray(t *testing.T) {
	var sp SplitPart
	if err := json.Unmarshal([]byte(`{"title":"T","acceptance":["a","b"]}`), &sp); err != nil {
		t.Fatalf("array acceptance refused: %v", err)
	}
	if sp.Acceptance != "a\nb" {
		t.Fatalf("acceptance = %q", sp.Acceptance)
	}
}
