package peoplelinks

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
)

// A sampled provider suggests "same" at 0.9 only when all five samples agree.
// It never dismisses a pair for good, however unanimous the "not same".
func TestASampledProviderSuggestsOnlyUnanimouslyAndNeverDismisses(t *testing.T) {
	for _, c := range []struct {
		answer            string
		agree             int
		suggested, undone int
	}{
		{"true", 5, 1, 0},
		{"true", 4, 0, 1},
		{"false", 5, 0, 1},
	} {
		db := newDB(t)
		seed(t, db,
			person{id: "s1", platform: "X", user: "sam", name: "Sam Ple"},
			person{id: "s2", platform: "LINKEDIN", user: "sam2", name: "Sam Ple"},
		)
		enable(t, db)
		fb := jevtest.NewFakeBackend(nil)
		fb.SetSampled(jevtest.Fixed(map[string]string{QuestionID: c.answer}), c.agree)
		client := &jev.Client{Provider: "local", Backend: fb, Model: "m"}
		pairs, err := Candidates(context.Background(), db, "default", 0)
		if err != nil || len(pairs) != 1 {
			t.Fatalf("pairs = %d, %v", len(pairs), err)
		}
		res, err := Suggest(context.Background(), client, db, "default", pairs, 0.9)
		if err != nil || res.Suggested != c.suggested || res.Undecided != c.undone || res.Dismissed != 0 {
			t.Fatalf("%s %d of 5: %+v %v", c.answer, c.agree, res, err)
		}
		links, _ := db.ListPersonLinks("default", "")
		if len(links) != c.suggested || (c.suggested == 1 && links[0].Status != storage.PersonLinkSuggested) {
			t.Fatalf("%s %d of 5: links = %+v", c.answer, c.agree, links)
		}
	}
}
