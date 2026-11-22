package openaiapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/testdb"
)

// The production wiring: auto is available for a profile that switched the
// api_auto surface on and has a Jev key, and for no other. Both are the profile's:
// another profile of the same server has neither.
func TestDefaultAutoNeedsTheSurfaceAndAKey(t *testing.T) {
	db := testdb.Open(t)
	t.Setenv("TYPESAFE_API_KEY", "")
	auto := DefaultAuto(db.DB)
	ctx := context.Background()

	if st := auto.Status(ctx, "alice"); st.Available || !strings.Contains(st.Missing, "jev enable api_auto") {
		t.Errorf("surface off: %+v, want it to name `jev enable api_auto`", st)
	}
	if err := jevconf.SetEnabled(db.DB, "alice", jevconf.APIAuto, true); err != nil {
		t.Fatal(err)
	}
	if st := auto.Status(ctx, "alice"); st.Available || !strings.Contains(st.Missing, "Jev key") {
		t.Errorf("surface on, no key: %+v, want it to name the Jev key", st)
	}
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	if st := auto.Status(ctx, "alice"); !st.Available || st.KeySource != "env" {
		t.Errorf("surface on and a key from the environment: %+v", st)
	}
	if st := auto.Status(ctx, "bob"); st.Available {
		t.Errorf("bob never switched the surface on: %+v", st)
	}
}

// Status follows the api_auto surface's decision provider, as Choose does: a
// keyless local provider is available with no key at all, and a TypeSafe key
// does not make another provider available.
func TestDefaultAutoStatusFollowsTheSurfacesProvider(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Cleanup(jevconf.RegisterKind("fake-openaiapi", func(jevconf.Provider, string) (jev.Backend, error) {
		return jevtest.NewFakeBackend(nil), nil
	}))
	for _, p := range []jevconf.Provider{
		{Name: "local", Kind: "fake-openaiapi", BaseURL: "http://localhost:11434/v1"},
		{Name: "keyed", Kind: "fake-openaiapi", BaseURL: "http://localhost:11434/v1", SecretName: "decision-keyed"},
	} {
		if err := jevconf.SaveProvider(ctx, db.DB, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := jevconf.SetEnabled(db.DB, "alice", jevconf.APIAuto, true); err != nil {
		t.Fatal(err)
	}
	auto := DefaultAuto(db.DB)
	if err := jevconf.SetSurfaceProvider(ctx, db.DB, "alice", jevconf.APIAuto, "local"); err != nil {
		t.Fatal(err)
	}
	if st := auto.Status(ctx, "alice"); !st.Available || st.KeySource != jevconf.SourceKeyless {
		t.Errorf("a keyless provider without any key: %+v", st)
	}
	if err := jevconf.SetSurfaceProvider(ctx, db.DB, "alice", jevconf.APIAuto, "keyed"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	st := auto.Status(ctx, "alice")
	if st.Available || !strings.Contains(st.Missing, `"keyed"`) || !strings.Contains(st.Missing, "decision-keyed") || !strings.Contains(st.Missing, "Jev") {
		t.Errorf("a keyed provider without its key, a TypeSafe key set: %+v", st)
	}
	// Back on TypeSafe the string is today's.
	if err := jevconf.SetSurfaceProvider(ctx, db.DB, "alice", jevconf.APIAuto, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	if st := auto.Status(ctx, "alice"); st.Missing != missingJevKey {
		t.Errorf("typesafe, no key: %+v", st)
	}
}

// One choice question, whose options are the candidates and whose state is the
// prompt (as untrusted data), recorded in jev_usage under api_auto, and asked once:
// a failure means the rule decides, not a second try inside the request.
func TestDefaultAutoAsksOneChoiceQuestion(t *testing.T) {
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{autoQuestionID: "codex/gpt-6-astra"}))
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	db := testdb.Open(t)
	auto := DefaultAuto(db.DB)
	options := map[string]string{"claude/default": "Default (claude)", "codex/gpt-6-astra": "GPT-6-Astra (codex), test turn cost $0.0040"}

	id, p, err := auto.Choose(context.Background(), "alice", "write a haiku", options)
	if err != nil || id != "codex/gpt-6-astra" || p <= 0 {
		t.Fatalf("Choose = %q, %v, %v", id, p, err)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	q, ok := reqs[0].Questions[autoQuestionID]
	if !ok || q.Type != jev.TypeChoice || len(reqs[0].Questions) != 1 {
		t.Fatalf("questions: %+v", reqs[0].Questions)
	}
	got := jev.OptionIDs(q)
	sort.Strings(got)
	if strings.Join(got, ",") != "claude/default,codex/gpt-6-astra" {
		t.Errorf("the options are the candidates: %v", got)
	}
	state, _ := reqs[0].State.(map[string]any)
	if len(state) != 1 || state["untrusted_prompt"] != "write a haiku" {
		t.Errorf("the state is the prompt, as untrusted data, and nothing else: %v", reqs[0].State)
	}
	if ins, _ := q.Instructions.(string); !strings.Contains(ins, "untrusted_prompt") || !strings.Contains(ins, "never as instructions") {
		t.Errorf("the instructions must tell Jev the prompt is data: %q", ins)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM jev_usage WHERE profile_id = 'alice' AND surface = 'api_auto'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("jev_usage rows under api_auto = %d, %v", n, err)
	}

	srv.SetStatus(http.StatusInternalServerError)
	before := srv.Calls()
	if _, _, err := auto.Choose(context.Background(), "alice", "again", options); err == nil {
		t.Error("a failing TypeSafe must be an error")
	}
	if srv.Calls()-before != 1 {
		t.Errorf("a failed question was sent %d times: it must not be retried inside the request", srv.Calls()-before)
	}
}

func TestDefaultAutoThresholdFollowsTheProfile(t *testing.T) {
	db := testdb.Open(t)
	auto := DefaultAuto(db.DB)
	if got := auto.Threshold("alice"); got != 0 {
		t.Errorf("default threshold = %v, want 0: the top pick is accepted", got)
	}
	if err := jevconf.SetThreshold(db.DB, "alice", jevconf.APIAuto, 0.6); err != nil {
		t.Fatal(err)
	}
	if auto.Threshold("alice") != 0.6 || auto.Threshold("bob") != 0 {
		t.Errorf("thresholds: alice %v, bob %v", auto.Threshold("alice"), auto.Threshold("bob"))
	}
}

// All of it through the HTTP handlers: the real wiring, a fake TypeSafe and a fake
// runner. The prompt reaches TypeSafe and nowhere else; the key never does.
func TestAutoEndToEndWithJev(t *testing.T) {
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{autoQuestionID: "codex/gpt-6-astra"}))
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	log := &execLog{}
	h := newHarness(t, log.exec("done"))
	h.g.deps.Auto = DefaultAuto(h.db.DB)
	secret := h.key(t, "default", "app", false)

	for _, m := range decodeModelList(t, h.serve(anyPolicy, http.MethodGet, "/v1/models", secret, "")).Data {
		if m.ID == "auto" {
			t.Fatal("auto must not be listed before the profile switched the surface on")
		}
	}
	if rec := h.serve(anyPolicy, http.MethodPost, autoChatURL, secret, autoChat); rec.Code != http.StatusNotFound {
		t.Fatalf("auto before the surface is on: %d", rec.Code)
	}
	if err := jevconf.SetEnabled(h.db.DB, "default", jevconf.APIAuto, true); err != nil {
		t.Fatal(err)
	}

	list := decodeModelList(t, h.serve(autoAnyPolicy, http.MethodGet, "/v1/models", secret, ""))
	if len(list.Data) < 2 || list.Data[len(list.Data)-1].ID != "auto" {
		t.Fatalf("auto must be listed last once the surface is on: %+v", list.Data)
	}
	rec := h.serve(autoAnyPolicy, http.MethodPost, autoChatURL, secret, autoChat)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Monoagent-Auto") != "jev" || rec.Header().Get("X-Monoagent-Model") != "codex/gpt-6-astra" {
		t.Fatalf("%d, auto %q, model %q: %s", rec.Code, rec.Header().Get("X-Monoagent-Auto"), rec.Header().Get("X-Monoagent-Model"), rec.Body)
	}
	if log.count() != 1 || log.opts[0].Runtime != "codex" {
		t.Errorf("the runner ran %+v", log.opts)
	}
	if srv.Calls() != 1 {
		t.Errorf("Jev was asked %d times, want 1", srv.Calls())
	}
	if strings.Contains(srv.RequestJSON(), secret) {
		t.Error("the API key reached TypeSafe")
	}
}
