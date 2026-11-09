package peoplereview

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

func suggestEnv(t *testing.T) *jevtest.Server {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	return jevtest.NewServer(t, func(req jev.Request) map[string]string {
		raw, _ := json.Marshal(req.State)
		if strings.Contains(string(raw), "spam") {
			return map[string]string{QSuggest: "reject", QIntroFit: IntroOff}
		}
		return map[string]string{QSuggest: "approve", QIntroFit: IntroOnTopic}
	})
}

func TestWithSuggestionsDisabled(t *testing.T) {
	srv := suggestEnv(t)
	db := testDB(t)
	addPerson(t, db, "p1", "prof", Pending, "hi")
	people, _ := ListPending(context.Background(), db, "prof")
	out, warns := WithSuggestions(context.Background(), db, "prof", people, false)
	if len(out) != 1 || out[0].Suggestion != nil || len(warns) != 1 || srv.Calls() != 0 {
		t.Fatalf("disabled: %+v %v calls=%d", out, warns, srv.Calls())
	}
}

func TestWithSuggestionsCachedPerPerson(t *testing.T) {
	srv := suggestEnv(t)
	ctx := context.Background()
	db := testDB(t)
	addPerson(t, db, "p1", "prof", Pending, "Hi, loved your talk on Go")
	addPerson(t, db, "p2", "prof", Pending, "buy spam now")
	if err := jevconf.SetEnabled(db, "prof", jevconf.PeopleReview, true); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		people, _ := ListPending(ctx, db, "prof")
		out, warns := WithSuggestions(ctx, db, "prof", people, false)
		if len(warns) != 0 {
			t.Fatalf("warnings: %v", warns)
		}
		for _, r := range out {
			s := r.Suggestion
			want, fit := "approve", IntroOnTopic
			if r.ID == "p2" {
				want, fit = "reject", IntroOff
			}
			if s == nil || s.Suggest != want || s.IntroFit != fit || s.P < 0.9 || s.IntroFitP < 0.9 || s.Model != "jev-test" {
				t.Fatalf("pass %d %s: %+v", pass, r.ID, s)
			}
		}
	}
	if srv.Calls() != 2 {
		t.Fatalf("calls = %d, want one per person", srv.Calls())
	}
	req := srv.Requests()[0]
	if _, ok := req.State.(map[string]any)["untrusted_intro"]; !ok {
		t.Fatalf("state = %+v", req.State)
	}
	// A changed introduction is asked about again; --resuggest recomputes all.
	if _, err := Approve(ctx, db, "prof", "p1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE people SET category = ?, introduction = 'new text' WHERE id = 'p1'`, Pending); err != nil {
		t.Fatal(err)
	}
	people, _ := ListPending(ctx, db, "prof")
	WithSuggestions(ctx, db, "prof", people, false)
	if srv.Calls() != 3 {
		t.Fatalf("changed intro: calls = %d, want 3", srv.Calls())
	}
	WithSuggestions(ctx, db, "prof", people, true)
	if srv.Calls() != 5 {
		t.Fatalf("resuggest: calls = %d, want 5", srv.Calls())
	}
	// Suggestions never move anyone out of the queue.
	if people, _ := ListPending(ctx, db, "prof"); len(people) != 2 {
		t.Fatalf("queue = %d", len(people))
	}
}
