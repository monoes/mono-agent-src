package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	return db.DB
}

func TestResolveKeyOrder(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	t.Setenv("TYPESAFE_API_KEY", "")

	if k, src, err := ResolveKey(ctx, db, "p1", "explicit"); err != nil || k != "explicit" || src != SourceConfig {
		t.Fatalf("explicit: %q %q %v", k, src, err)
	}
	// No vault entry, no env: ErrNoAPIKey (absent, not unreadable); never returns the literal ref.
	k, _, err := ResolveKey(ctx, db, "p1", "@secret:typesafe")
	if !errors.Is(err, jev.ErrNoAPIKey) || errors.Is(err, ErrKeyUnreadable) || k != "" {
		t.Fatalf("missing: %q %v", k, err)
	}
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	for _, explicit := range []string{"", "@secret:typesafe", "@secret:other"} {
		if k, src, err := ResolveKey(ctx, db, "p1", explicit); err != nil || k != "env-key" || src != SourceEnv {
			t.Fatalf("%q: %q %q %v", explicit, k, src, err)
		}
	}
	if k, src, err := ResolveKey(ctx, nil, "p1", ""); err != nil || k != "env-key" || src != SourceEnv {
		t.Fatalf("nil db: %q %q %v", k, src, err)
	}
}

func TestEnabledAndThreshold(t *testing.T) {
	db := newDB(t)
	if Enabled(db, "p1", Capture) || Enabled(nil, "p1", Capture) {
		t.Fatal("surfaces must default to off")
	}
	if err := SetEnabled(db, "p1", Capture, true); err != nil {
		t.Fatal(err)
	}
	if !Enabled(db, "p1", Capture) || Enabled(db, "p2", Capture) || Enabled(db, "p1", Inbox) {
		t.Fatal("enable must be per profile and per surface")
	}
	if err := SetEnabled(db, "p1", Surface("bogus"), true); err == nil {
		t.Fatal("unknown surface accepted")
	}
	if got := Threshold(db, "p1", Capture, 0.75); got != 0.75 {
		t.Fatalf("default threshold = %v", got)
	}
	if err := SetThreshold(db, "p1", Capture, 1.5); err == nil {
		t.Fatal("threshold 1.5 accepted")
	}
	if err := SetThreshold(db, "p1", Capture, 0.6); err != nil || Threshold(db, "p1", Capture, 0.75) != 0.6 {
		t.Fatalf("threshold = %v, %v", Threshold(db, "p1", Capture, 0.75), err)
	}
	for _, s := range Surfaces {
		if len(Egress[s]) == 0 {
			t.Errorf("surface %s has no egress description", s)
		}
		if Describe[s].Title == "" || Describe[s].Description == "" {
			t.Errorf("surface %s has no title/description", s)
		}
		if _, ok := DefaultThreshold[s]; !ok {
			t.Errorf("surface %s has no default threshold", s)
		}
	}
}

// An image request for the `auto` model sends TypeSafe the prompt of the image, so
// the list `jev enable` prints before switching the surface on says so.
func TestAPIAutoEgressSaysImagePromptsToo(t *testing.T) {
	if egress := strings.Join(Egress[APIAuto], " | "); !strings.Contains(egress, "of an image request, its prompt") {
		t.Errorf("the egress list must say that an image request's prompt is sent: %s", egress)
	}
}

// The `auto` model of the OpenAI-compatible API is a surface of its own: switched
// on per profile like the others, and it must say what leaves the machine. Its
// default threshold is 0: the top pick of Jev is accepted unless the profile
// raises it.
func TestAPIAutoSurface(t *testing.T) {
	if !Known(APIAuto) {
		t.Fatal("api_auto is not a surface `jev enable` accepts")
	}
	if string(APIAuto) != "api_auto" {
		t.Errorf("the surface is named %q", APIAuto)
	}
	if got, ok := DefaultThreshold[APIAuto]; !ok || got != 0 {
		t.Errorf("default threshold = %v, %v: want 0, always accept the top pick", got, ok)
	}
	egress := strings.Join(Egress[APIAuto], " | ")
	for _, want := range []string{"first 4,000 characters of the last user message", "validated cost and latency"} {
		if !strings.Contains(egress, want) {
			t.Errorf("the egress list must say %q: %s", want, egress)
		}
	}
	db := newDB(t)
	if Enabled(db, "p1", APIAuto) {
		t.Error("api_auto is on by default")
	}
	if err := SetEnabled(db, "p1", APIAuto, true); err != nil || !Enabled(db, "p1", APIAuto) || Enabled(db, "p2", APIAuto) {
		t.Errorf("enabling api_auto for one profile: %v", err)
	}
}

func TestNewClientRecordsUsage(t *testing.T) {
	db := newDB(t)
	srv := jevtest.NewServer(t, nil)
	c, err := NewClient(context.Background(), db, "p1", "k", "", Capture)
	if err != nil {
		t.Fatal(err)
	}
	q := map[string]jev.Question{"q": {Type: jev.TypeChoice, Criteria: map[string]any{"a": nil, "b": nil}}}
	if _, err := c.Ask(context.Background(), "s", q); err != nil {
		t.Fatal(err)
	}
	srv.SetStatus(400)
	_, _ = c.Ask(context.Background(), "s", q)
	u, err := UsageSince(db, "p1", time.Now().Add(-time.Hour))
	if err != nil || len(u) != 1 {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	if u[0].Surface != "capture" || u[0].Calls != 2 || u[0].Failures != 1 || u[0].InputTokens != 100 || u[0].EstimatedUSD <= 0 {
		t.Fatalf("usage = %+v", u[0])
	}
}

func TestRecorderCountsTokensOfRejectedAnswers(t *testing.T) {
	db := newDB(t)
	// A billed 200 whose noul answer has no value: the client rejects it,
	// but its input tokens were still charged and must be recorded.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-test","usage":{"input_tokens":123},"answers":{"q":{"type":"noul"}}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	jevtest.DefaultHost(t, srv.URL)
	c, err := NewClient(context.Background(), db, "p1", "k", "", Capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ask(context.Background(), "s", map[string]jev.Question{"q": {Type: jev.TypeNoul}}); !errors.Is(err, jev.ErrInvalidAnswer) {
		t.Fatalf("err = %v, want ErrInvalidAnswer", err)
	}
	var tokens, ok int
	if err := db.QueryRow(`SELECT input_tokens, ok FROM jev_usage WHERE profile_id = 'p1'`).Scan(&tokens, &ok); err != nil {
		t.Fatal(err)
	}
	if tokens != 123 || ok != 0 {
		t.Fatalf("recorded input_tokens=%d ok=%d, want 123 and 0", tokens, ok)
	}
}

func TestSuggestionCache(t *testing.T) {
	db := newDB(t)
	var got map[string]any
	if ok, err := LoadSuggestion(db, "p1", PeopleReview, "person-1", &got); ok || err != nil {
		t.Fatalf("empty cache: %v %v", ok, err)
	}
	for _, v := range []string{"approve", "reject"} {
		if err := SaveSuggestion(db, "p1", PeopleReview, "person-1", map[string]any{"choice": v}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := LoadSuggestion(db, "p1", PeopleReview, "person-1", &got); !ok || err != nil || got["choice"] != "reject" {
		t.Fatalf("cache = %v %v %v", got, ok, err)
	}
}

func TestKeySourceNeverNeedsTheVaultKey(t *testing.T) {
	db := newDB(t)
	t.Setenv("TYPESAFE_API_KEY", "")
	if _, err := KeySource(context.Background(), db, "p1"); !errors.Is(err, jev.ErrNoAPIKey) {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	if src, err := KeySource(context.Background(), nil, "p1"); err != nil || src != SourceEnv {
		t.Fatalf("src = %q %v", src, err)
	}
}

func TestKeyNameAliases(t *testing.T) {
	for in, want := range map[string]bool{
		"Jev Api key": true, "jev_api_key": true, "TypeSafe API Key": true, "typesafe": true,
		"JEV": true, "openai": false, "jevons notes": false, "": false,
	} {
		if got := keyNameAliases[normaliseKeyName(in)]; got != want {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
}
