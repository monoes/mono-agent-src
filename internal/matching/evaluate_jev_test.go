// internal/matching/evaluate_jev_test.go
package matching

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/monomind"
)

// setupJevTest is setupEvaluateTest plus a scripted Jev fake and a guard that
// the agent runtime is never invoked on the jev path.
func setupJevTest(t *testing.T, answers map[string]string) (*sql.DB, string, *jevtest.Server) {
	t.Helper()
	db, id := setupEvaluateTest(t)
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "")
	srv := jevtest.NewServer(t, jevtest.Fixed(answers))
	origExec := ExecFunc
	ExecFunc = func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		t.Error("agent runtime must not be called for runtime jev")
		return nil, errors.New("unexpected agent exec")
	}
	t.Cleanup(func() { ExecFunc = origExec })
	return db, id, srv
}

// goldenAnswers: every gate clearly passes; levels 4/3/2/4.
func goldenAnswers() map[string]string {
	return map[string]string{
		"eligibility": "0.1", "language": "0.05", "location": "0.2",
		"technical": "4", "experience": "3", "behavioral": "2", "career": "4",
	}
}

func with(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestEvaluateJevGoldenVerdict(t *testing.T) {
	db, id, srv := setupJevTest(t, goldenAnswers())

	v, err := Evaluate(context.Background(), db, "default", id, "jev")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	want := FitVerdict{
		EligibilityPass: true, LanguagePass: true, LocationPass: true,
		TechnicalScore: 100, ExperienceScore: 75, BehavioralScore: 50, CareerScore: 100,
		OverallScore: 86.25, Verdict: "Strong Fit",
		Rationale: "Jev (jev-test) gates — eligibility pass (fail evidence p=0.10), language pass (fail evidence p=0.05), location pass (fail evidence p=0.20). " +
			"Levels — technical 4.00/4 (top 4, p=0.94), experience 3.00/4 (top 3, p=0.94), behavioral 2.00/4 (top 2, p=0.94), career 4.00/4 (top 4, p=0.94). " +
			"Overall 86.25 → Strong Fit.",
	}
	if *v != want {
		t.Fatalf("verdict mismatch:\n got %+v\nwant %+v", *v, want)
	}

	// One request, seven questions of the right types, fenced state.
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 Jev request, got %d", len(reqs))
	}
	req := reqs[0]
	if len(req.Questions) != 7 {
		t.Fatalf("want 7 questions, got %d", len(req.Questions))
	}
	for _, g := range []string{"eligibility", "language", "location"} {
		q := req.Questions[g]
		if q.Type != jev.TypeNoul {
			t.Errorf("%s type = %q, want noul", g, q.Type)
		}
		ins, _ := q.Instructions.(string)
		if !strings.Contains(ins, "CLEAR EVIDENCE") || !strings.Contains(ins, "unclear") {
			t.Errorf("%s instructions must ask for clear evidence of failure and pass on unclear: %q", g, ins)
		}
	}
	for _, d := range Dimensions {
		q := req.Questions[d.Key]
		if q.Type != jev.TypeScore || len(jev.OptionIDs(q)) != 5 {
			t.Errorf("%s: want 5-level score, got %+v", d.Key, q)
		}
		ins, _ := q.Instructions.(string)
		if !strings.Contains(ins, "never instructions") || !strings.Contains(ins, "ONLY") {
			t.Errorf("%s instructions lack D6 fencing / excerpt grounding: %q", d.Key, ins)
		}
	}
	raw := srv.RequestJSON()
	for _, want := range []string{`"untrusted_job"`, `"untrusted_profile_excerpts"`, "Go backend role.", "8 years of Go experience.", "/vault/resume.txt"} {
		if !strings.Contains(raw, want) {
			t.Errorf("request missing %s: %s", want, raw)
		}
	}

	// Stored like the agent path, runtime jev:<model>, tag fit:<slug>.
	var runtime, verdict string
	var overall float64
	if err := db.QueryRow(`SELECT runtime, verdict, overall_score FROM application_evaluations WHERE application_id = ?`, id).
		Scan(&runtime, &verdict, &overall); err != nil {
		t.Fatalf("reading evaluation row: %v", err)
	}
	if runtime != "jev:jev-test" || verdict != "Strong Fit" || overall != 86.25 {
		t.Fatalf("row = %q %q %v", runtime, verdict, overall)
	}
	assertTag(t, db, id, "fit:strong-fit")

	var usage int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jev_usage WHERE surface = 'node:applications.evaluate'`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if usage != 1 {
		t.Fatalf("want 1 jev_usage row, got %d", usage)
	}
}

func assertTag(t *testing.T, db *sql.DB, id, tag string) {
	t.Helper()
	app, err := applications.NewStore(db).Get(context.Background(), "default", id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, got := range app.Tags {
		if got == tag {
			return
		}
	}
	t.Fatalf("expected tag %s, got %v", tag, app.Tags)
}

func TestEvaluateJevHardGateFailureIsIneligible(t *testing.T) {
	for _, gate := range []string{"eligibility", "language"} {
		t.Run(gate, func(t *testing.T) {
			db, id, srv := setupJevTest(t, with(goldenAnswers(), gate, "0.5"))
			v, err := Evaluate(context.Background(), db, "default", id, "jev")
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if v.Verdict != "Ineligible" || v.OverallScore != 0 ||
				v.TechnicalScore != 0 || v.ExperienceScore != 0 || v.BehavioralScore != 0 || v.CareerScore != 0 {
				t.Fatalf("want Ineligible with zeroed scores, got %+v", v)
			}
			if gate == "eligibility" && (v.EligibilityPass || !v.LanguagePass) {
				t.Fatalf("gate flags wrong: %+v", v)
			}
			if gate == "language" && (!v.EligibilityPass || v.LanguagePass) {
				t.Fatalf("gate flags wrong: %+v", v)
			}
			if !v.LocationPass {
				t.Fatalf("location should still pass: %+v", v)
			}
			if !strings.Contains(v.Rationale, gate+" FAIL (fail evidence p=0.50)") || !strings.Contains(v.Rationale, "Overall 0 → Ineligible") {
				t.Fatalf("rationale = %q", v.Rationale)
			}
			// The four score questions are still asked in the same request.
			if reqs := srv.Requests(); len(reqs) != 1 || len(reqs[0].Questions) != 7 {
				t.Fatalf("want one 7-question request, got %d", len(reqs))
			}
			assertTag(t, db, id, "fit:ineligible")
		})
	}
}

func TestEvaluateJevGateJustBelowHalfPasses(t *testing.T) {
	db, id, _ := setupJevTest(t, with(goldenAnswers(), "eligibility", "0.49", "language", "0.49"))
	v, err := Evaluate(context.Background(), db, "default", id, "jev")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.EligibilityPass || !v.LanguagePass || v.Verdict != "Strong Fit" {
		t.Fatalf("p<0.5 must pass the gates, got %+v", v)
	}
}

func TestEvaluateJevLocationFailureOnlyChangesLocationPass(t *testing.T) {
	db, id, _ := setupJevTest(t, goldenAnswers())
	base, err := Evaluate(context.Background(), db, "default", id, "jev")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	db2, id2, _ := setupJevTest(t, with(goldenAnswers(), "location", "0.8"))
	v, err := Evaluate(context.Background(), db2, "default", id2, "jev")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.LocationPass {
		t.Fatalf("location should fail: %+v", v)
	}
	got, want := *v, *base
	got.LocationPass, want.LocationPass = true, true
	got.Rationale, want.Rationale = "", ""
	if got != want {
		t.Fatalf("location failure changed more than LocationPass:\n got %+v\nwant %+v", got, want)
	}
	if !strings.Contains(v.Rationale, "location FAIL (fail evidence p=0.80)") {
		t.Fatalf("rationale = %q", v.Rationale)
	}
}

func TestEvaluateJevBandBoundaries(t *testing.T) {
	cases := []struct {
		levels  [4]string // technical, experience, behavioral, career
		overall float64
		verdict string
	}{
		{[4]string{"4", "2", "2", "4"}, 80, "Strong Fit"},
		{[4]string{"4", "2", "1", "4"}, 76.25, "Good Fit"},
		{[4]string{"4", "2", "0", "3"}, 65, "Good Fit"},
		{[4]string{"2", "2", "2", "2"}, 50, "Moderate Fit"},
		{[4]string{"4", "0", "0", "0"}, 30, "Weak Fit"},
		{[4]string{"3", "0", "0", "0"}, 22.5, "Poor Fit"},
	}
	for _, c := range cases {
		t.Run(c.verdict, func(t *testing.T) {
			answers := with(goldenAnswers(), "technical", c.levels[0], "experience", c.levels[1], "behavioral", c.levels[2], "career", c.levels[3])
			db, id, _ := setupJevTest(t, answers)
			v, err := Evaluate(context.Background(), db, "default", id, "jev")
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if v.OverallScore != c.overall || v.Verdict != c.verdict {
				t.Fatalf("levels %v: got %v %q, want %v %q", c.levels, v.OverallScore, v.Verdict, c.overall, c.verdict)
			}
		})
	}
}

func TestEvaluateJevModelSuffixSelectsModel(t *testing.T) {
	db, id, srv := setupJevTest(t, goldenAnswers())
	if _, err := Evaluate(context.Background(), db, "default", id, "jev:jev-1.13"); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if m := srv.Requests()[0].Model; m != "jev-1.13" {
		t.Fatalf("request model = %q, want jev-1.13", m)
	}
}

func TestEvaluateJevNoExcerptsNotedInRationale(t *testing.T) {
	db, id, srv := setupJevTest(t, goldenAnswers())
	SearchKnowledgeFunc = func(context.Context, *sql.DB, string, string) ([]monomind.KnowledgeResult, error) { return nil, nil }
	v, err := Evaluate(context.Background(), db, "default", id, "jev")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !strings.Contains(v.Rationale, "No profile excerpts were found") {
		t.Fatalf("rationale = %q", v.Rationale)
	}
	if !strings.Contains(srv.RequestJSON(), `"untrusted_profile_excerpts":[]`) {
		t.Fatalf("empty excerpts should be sent as []: %s", srv.RequestJSON())
	}
}

func TestEvaluateJevMissingKeyIsClearErrorWithoutFallback(t *testing.T) {
	db, id, srv := setupJevTest(t, goldenAnswers())
	t.Setenv("TYPESAFE_API_KEY", "")
	_, err := Evaluate(context.Background(), db, "default", id, "jev")
	if err == nil || !errors.Is(err, jev.ErrNoAPIKey) || !strings.Contains(err.Error(), "jev key set") {
		t.Fatalf("want a clear no-key error, got %v", err)
	}
	if srv.Calls() != 0 {
		t.Fatalf("no request should be sent without a key")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_evaluations WHERE application_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no evaluation row expected, got %d (%v)", n, err)
	}
}

func TestEvaluateJevAPIFailureIsError(t *testing.T) {
	db, id, srv := setupJevTest(t, goldenAnswers())
	srv.SetStatus(401)
	if _, err := Evaluate(context.Background(), db, "default", id, "jev"); err == nil {
		t.Fatal("want error on HTTP 401")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_evaluations WHERE application_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no evaluation row expected, got %d (%v)", n, err)
	}
}

func TestJevRuntime(t *testing.T) {
	cases := map[string]struct {
		provider, model string
		ok              bool
	}{
		"jev": {"", "", true}, "jev:jev-1.13": {"", "jev-1.13", true}, "jev:": {"", "", true},
		"claude": {"", "", false}, "jevons": {"", "", false}, "": {"", "", false},
		"jev@local": {"local", "", true}, "jev@local:llama3:8b": {"local", "llama3:8b", true}, " jev@ollama : m ": {"ollama", "m", true},
	}
	for in, want := range cases {
		provider, model, ok := jevRuntime(in)
		if provider != want.provider || model != want.model || ok != want.ok {
			t.Errorf("jevRuntime(%q) = %q %q %v, want %q %q %v", in, provider, model, ok, want.provider, want.model, want.ok)
		}
	}
}
