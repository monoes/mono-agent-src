package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStampPassStopsAtEligibleNameCap(t *testing.T) {
	old := maxStampNames
	t.Cleanup(func() { maxStampNames = old })
	maxStampNames = 3
	dir := t.TempDir()
	originals := map[string]string{}
	// Creation order differs from ReadDir's name order; every file is eligible.
	for _, n := range []int{4, 1, 3, 0, 2} {
		id := fmt.Sprintf("wf-%02d", n)
		raw := fmt.Sprintf(`{"id":%q,"nodes":[],"connections":[]}`, id)
		originals[id] = raw
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var considered []string
	stamped, err := StampFileWorkflowProfilesFor(dir, "default", nil, func(id string) bool {
		considered = append(considered, id)
		return true
	}, nil)
	want := []string{"wf-00", "wf-01", "wf-02"}
	if err != nil || !slices.Equal(stamped, want) || !slices.Equal(considered, want) {
		t.Fatalf("stamped=%v considered=%v err=%v; want first %d names %v", stamped, considered, err, maxStampNames, want)
	}
	for n := 0; n < 5; n++ {
		id := fmt.Sprintf("wf-%02d", n)
		raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			ProfileID string `json:"profile_id"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if n < maxStampNames {
			if doc.ProfileID != "default" {
				t.Errorf("%s: profile=%q", id, doc.ProfileID)
			}
		} else if doc.ProfileID != "" || string(raw) != originals[id] {
			t.Errorf("%s beyond the cap was touched: %s", id, raw)
		}
	}
}
