package peoplereview

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// R14-19: one run asks for at most MaxSuggestionsPerRun paid suggestions.
func TestWithSuggestionsCapsRequestsPerRun(t *testing.T) {
	srv := suggestEnv(t)
	ctx := context.Background()
	db := testDB(t)
	for i := 0; i < MaxSuggestionsPerRun+10; i++ {
		addPerson(t, db, fmt.Sprintf("p%03d", i), "prof", Pending, "hello")
	}
	if err := jevconf.SetEnabled(db, "prof", jevconf.PeopleReview, true); err != nil {
		t.Fatal(err)
	}
	people, _ := ListPending(ctx, db, "prof")
	out, warns := WithSuggestions(ctx, db, "prof", people, false)
	if srv.Calls() != MaxSuggestionsPerRun {
		t.Fatalf("calls = %d, want %d", srv.Calls(), MaxSuggestionsPerRun)
	}
	if len(out) != len(people) {
		t.Fatalf("queue shrank: %d", len(out))
	}
	if len(warns) == 0 || !strings.Contains(strings.Join(warns, " "), "10") {
		t.Fatalf("warns = %v", warns)
	}
	// The next run picks up the rest from the cache plus the 10 left.
	WithSuggestions(ctx, db, "prof", people, false)
	if srv.Calls() != MaxSuggestionsPerRun+10 {
		t.Fatalf("second run calls = %d", srv.Calls())
	}
}
