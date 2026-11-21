package matching

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// evalProvider saves provider "local", a fake answering with answers.
func evalProvider(t *testing.T, db *sql.DB, answers map[string]string, asDefault bool) *jevtest.FakeBackend {
	t.Helper()
	fb := jevtest.NewFakeBackend(jevtest.Fixed(answers))
	t.Cleanup(jevconf.RegisterKind("fake-matching", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{
		Name: "local", Kind: "fake-matching", BaseURL: "http://localhost", Model: "fake-model",
	}); err != nil {
		t.Fatal(err)
	}
	if asDefault {
		if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
			t.Fatal(err)
		}
	}
	return fb
}

func storedRuntime(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var runtime string
	if err := db.QueryRow(`SELECT runtime FROM application_evaluations WHERE application_id = ? ORDER BY rowid DESC LIMIT 1`, id).Scan(&runtime); err != nil {
		t.Fatal(err)
	}
	return runtime
}

// runtime "jev" follows the default provider; the verdict is the same Go
// computation and the stored runtime names the provider.
func TestEvaluateJevFollowsTheDefaultProvider(t *testing.T) {
	db, id, srv := setupJevTest(t, goldenAnswers())
	fb := evalProvider(t, db, goldenAnswers(), true)
	v, err := Evaluate(context.Background(), db, "default", id, "jev")
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != "Strong Fit" || fb.Calls() != 1 || srv.Calls() != 0 {
		t.Fatalf("verdict %q, fake %d, TypeSafe %d calls", v.Verdict, fb.Calls(), srv.Calls())
	}
	if got := storedRuntime(t, db, id); got != "jev@local:fake-model" {
		t.Errorf("stored runtime = %q", got)
	}
}

// runtime "jev@<provider>[:<model>]" names the provider; plain "jev" stays
// TypeSafe when nothing else is configured.
func TestEvaluateJevAtProvider(t *testing.T) {
	db, id, srv := setupJevTest(t, goldenAnswers())
	fb := evalProvider(t, db, goldenAnswers(), false)
	if _, err := Evaluate(context.Background(), db, "default", id, "jev"); err != nil {
		t.Fatal(err)
	}
	if srv.Calls() != 1 || fb.Calls() != 0 || storedRuntime(t, db, id) != "jev:jev-test" {
		t.Fatalf("plain jev: TypeSafe %d, fake %d, runtime %q", srv.Calls(), fb.Calls(), storedRuntime(t, db, id))
	}
	if _, err := Evaluate(context.Background(), db, "default", id, "jev@local:llama3"); err != nil {
		t.Fatal(err)
	}
	if fb.Calls() != 1 || fb.Requests()[0].Model != "llama3" || storedRuntime(t, db, id) != "jev@local:llama3" {
		t.Fatalf("jev@local:llama3: fake %d, runtime %q", fb.Calls(), storedRuntime(t, db, id))
	}
	if _, err := Evaluate(context.Background(), db, "default", id, "jev@nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("unknown provider: err = %v", err)
	}
}

// D7: a provider without probabilities is no evidence that a gate fails.
func TestEvaluateJevNoProbabilityPassesTheGates(t *testing.T) {
	db, id, _ := setupJevTest(t, goldenAnswers())
	fb := evalProvider(t, db, with(goldenAnswers(), "eligibility", "0.9"), true)
	fb.Caps.Probability = jev.ProbNone
	v, err := Evaluate(context.Background(), db, "default", id, "jev")
	if err != nil {
		t.Fatal(err)
	}
	if !v.EligibilityPass || v.Verdict == VerdictIneligible {
		t.Fatalf("verdict = %+v", v)
	}
}
