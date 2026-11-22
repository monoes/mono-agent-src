package peoplelinks

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
)

// With a provider set for people_links, the suggested link comes from it and
// its source and model name "<provider>/<model>". A provider with no
// probability records nothing: never a dismissed "not same" (plan D7).
func TestSuggestFromAProvider(t *testing.T) {
	db := newDB(t)
	seed(t, db,
		person{id: "h1", platform: "X", user: "hi", name: "High Match"},
		person{id: "h2", platform: "LINKEDIN", user: "hi2", name: "High Match"},
	)
	enable(t, db)
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{QuestionID: "0.95"}))
	t.Cleanup(jevconf.RegisterKind("peoplelinks-fake", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	ctx := context.Background()
	if err := jevconf.SaveProvider(ctx, db.DB, jevconf.Provider{Name: "local", Kind: "peoplelinks-fake", BaseURL: "http://localhost", Model: "qwen2.5-7b"}); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetSurfaceProvider(ctx, db.DB, "default", jevconf.PeopleLinks, "local"); err != nil {
		t.Fatal(err)
	}
	c, err := jevconf.NewClient(ctx, db.DB, "default", "", "", jevconf.PeopleLinks)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := Candidates(ctx, db, "default", 0)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs = %d, %v", len(pairs), err)
	}

	// No probability: asked, undecided, nothing stored.
	fb.Caps.Probability = jev.ProbNone
	fb.SetHandler(jevtest.Fixed(map[string]string{QuestionID: "0.01"}))
	res, err := Suggest(ctx, c, db, "default", pairs, 0.9)
	if err != nil || res.Undecided != 1 || res.Dismissed != 0 || len(fb.Requests()) != 1 {
		t.Fatalf("no-probability result = %+v, %v", res, err)
	}
	if links, _ := db.ListPersonLinks("default", ""); len(links) != 0 {
		t.Fatalf("no-probability links = %+v", links)
	}

	fb.Caps.Probability = jev.ProbLogprob
	fb.SetHandler(jevtest.Fixed(map[string]string{QuestionID: "0.95"}))
	res, err = Suggest(ctx, c, db, "default", pairs, 0.9)
	if err != nil || res.Suggested != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	links, _ := db.ListPersonLinks("default", "")
	if len(links) != 1 || links[0].Status != storage.PersonLinkSuggested || links[0].Source != "jev:local/qwen2.5-7b" || links[0].Model != "local/qwen2.5-7b" {
		t.Fatalf("links = %+v", links)
	}
}

// C4: a permanent "not same" dismissal needs a calibrated probability
// (native or logprob): a sampled or softmax p, however low, leaves the
// pair undecided.
func TestOnlyACalibratedProbabilityDismisses(t *testing.T) {
	db := newDB(t)
	seed(t, db,
		person{id: "d1", platform: "X", user: "lo", name: "Low Match"},
		person{id: "d2", platform: "LINKEDIN", user: "lo2", name: "Low Match"},
	)
	enable(t, db)
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{QuestionID: "0.01"}))
	t.Cleanup(jevconf.RegisterKind("peoplelinks-fake", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	ctx := context.Background()
	if err := jevconf.SaveProvider(ctx, db.DB, jevconf.Provider{Name: "local", Kind: "peoplelinks-fake", BaseURL: "http://localhost", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	c, err := jevconf.NewClientFor(ctx, db.DB, "default", "local", "", "", jevconf.PeopleLinks)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := Candidates(ctx, db, "default", 0)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs = %d, %v", len(pairs), err)
	}
	for _, kind := range []jev.ProbabilityKind{jev.ProbSampled, "softmax"} {
		fb.Caps.Probability = kind
		if res, err := Suggest(ctx, c, db, "default", pairs, 0.9); err != nil || res.Dismissed != 0 || res.Undecided != 1 {
			t.Fatalf("%s: result = %+v, %v", kind, res, err)
		}
	}
	if links, _ := db.ListPersonLinks("default", ""); len(links) != 0 {
		t.Fatalf("an uncalibrated p dismissed a pair: %+v", links)
	}
	fb.Caps.Probability = jev.ProbLogprob
	if res, err := Suggest(ctx, c, db, "default", pairs, 0.9); err != nil || res.Dismissed != 1 {
		t.Fatalf("logprob: result = %+v, %v", res, err)
	}
}
