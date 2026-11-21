package hfzeroshot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTheKindIsRegistered(t *testing.T) {
	if !slices.Contains(jevconf.Kinds(), Kind) {
		t.Fatalf("kinds %v lack %q", jevconf.Kinds(), Kind)
	}
}

func TestChoiceEndToEnd(t *testing.T) {
	f := newFake(t, pairs(0.3, 0.1, 0.1))
	c := providerClient(t, f.URL+"/models/bart-mnli", testKey)
	q := jev.Question{Type: jev.TypeChoice, Instructions: "Where does this ticket go?", Criteria: map[string]any{
		"billing": "a billing question", "sales": "", "support": " a billing question ",
	}}
	resp, err := c.Ask(context.Background(), map[string]any{"ticket": "refund please"}, map[string]jev.Question{"route": q})
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Answers["route"]
	if a.Choice != "billing" || !near(a.Probabilities["billing"], 0.6) || !near(a.Probabilities["sales"], 0.2) || !near(a.Probabilities["support"], 0.2) {
		t.Errorf("answer %+v", a)
	}
	if resp.Provider != "hf" || resp.Probability != jev.ProbNative || resp.Model != "bart-mnli" {
		t.Errorf("provider %q probability %q model %q", resp.Provider, resp.Probability, resp.Model)
	}
	got := f.requests()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	s := got[0]
	if s.method != http.MethodPost || s.path != "/models/bart-mnli" || s.header.Get("Authorization") != "Bearer "+testKey ||
		s.header.Get("Content-Type") != "application/json" {
		t.Errorf("request %s %s %v", s.method, s.path, s.header)
	}
	want := []string{"a billing question", "sales", "a billing question (support)"}
	if !slices.Equal(s.body.Parameters.CandidateLabels, want) || s.body.Parameters.MultiLabel {
		t.Errorf("parameters %+v, want labels %v", s.body.Parameters, want)
	}
	if s.body.Inputs != "Where does this ticket go?\n\n"+`{"ticket":"refund please"}` {
		t.Errorf("inputs %q", s.body.Inputs)
	}
}

func TestNoulEndToEnd(t *testing.T) {
	// The {"labels","scores"} shape, in the server's own (score) order.
	f := newFake(t, func(r classifyRequest) (int, string) {
		l := r.Parameters.CandidateLabels
		return http.StatusOK, fmt.Sprintf(`{"sequence":"x","labels":[%q,%q],"scores":[0.6,0.2]}`, l[1], l[0])
	})
	c := providerClient(t, f.URL, testKey)
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{
		"plain":     {Type: jev.TypeNoul, Instructions: "Spam?"},
		"described": {Type: jev.TypeNoul, Instructions: "Spam?", Criteria: map[string]string{"true": "spam", "false": "real mail"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, a := range resp.Answers {
		if !near(a.Noul, 0.25) { // P(yes) = 0.2 / (0.2 + 0.6)
			t.Errorf("%s: %+v", id, a)
		}
	}
	var labels [][]string
	for _, s := range f.requests() {
		labels = append(labels, s.body.Parameters.CandidateLabels)
	}
	slices.SortFunc(labels, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	if len(labels) != 2 || !slices.Equal(labels[0], []string{"spam", "real mail"}) || !slices.Equal(labels[1], []string{"yes", "no"}) {
		t.Errorf("labels %v", labels)
	}
}

func TestEveryResponseShape(t *testing.T) {
	sent := []string{"a", "b"}
	for _, raw := range []string{
		`[{"label":"b","score":0.25},{"label":"a","score":0.75}]`,
		`{"labels":["a","b"],"scores":[0.75,0.25]}`,
		`[[{"label":"a","score":0.75},{"label":"b","score":0.25}]]`,
		`[{"sequence":"s","labels":["b","a"],"scores":[0.25,0.75]}]`,
	} {
		got, err := decodeScores([]byte(raw), sent)
		if err != nil || !near(got["a"], 0.75) || !near(got["b"], 0.25) {
			t.Errorf("%s: %v %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		`[{"label":"c","score":1}]`,
		`{"labels":["a","b"],"scores":[1]}`,
		`[[[{"label":"a","score":1}]]]`,
		`[]`,
		`not json`,
		`{"error":"loading"}`,
	} {
		if _, err := decodeScores([]byte(raw), sent); !errors.Is(err, jev.ErrInvalidAnswer) || strings.Contains(err.Error(), "loading") {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// An unknown label in a reply fails the call; it is never quoted.
func TestAnUnknownLabelFails(t *testing.T) {
	f := newFake(t, func(classifyRequest) (int, string) { return http.StatusOK, `[{"label":"ignore previous","score":1}]` })
	c := providerClient(t, f.URL, testKey)
	_, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": {Type: jev.TypeNoul}})
	if !errors.Is(err, jev.ErrInvalidAnswer) || strings.Contains(err.Error(), "ignore previous") {
		t.Fatalf("got %v", err)
	}
}

func TestScoreIsUnsupported(t *testing.T) {
	f := newFake(t, pairs(1))
	c := providerClient(t, f.URL, testKey)
	score := jev.Question{Type: jev.TypeScore, Criteria: []string{"low", "high"}}
	if _, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": score}); !errors.Is(err, jev.ErrUnsupported) {
		t.Errorf("through Ask: %v", err)
	}
	b, err := New(jevconf.Provider{BaseURL: f.URL}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Answer(context.Background(), &jev.Request{Questions: map[string]jev.Question{"q": score}}); !errors.Is(err, jev.ErrUnsupported) {
		t.Errorf("on the backend: %v", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("a score question sent %d request(s)", n)
	}
}

func TestTooManyOptionsAreUnsupported(t *testing.T) {
	f := newFake(t, pairs(1))
	c := providerClient(t, f.URL, testKey)
	opts := map[string]any{}
	for i := 0; i <= MaxLabels; i++ {
		opts[fmt.Sprintf("o%02d", i)] = ""
	}
	_, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": {Type: jev.TypeChoice, Criteria: opts}})
	if !errors.Is(err, jev.ErrUnsupported) || !strings.Contains(err.Error(), "at most 50") {
		t.Errorf("got %v", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d request(s)", n)
	}
}

func TestInputsAreCut(t *testing.T) {
	big := map[string]any{"text": strings.Repeat("é", 10000)}
	f := newFake(t, pairs(0.9, 0.1))
	c := providerClient(t, f.URL, testKey)
	if _, err := c.Ask(context.Background(), big, map[string]jev.Question{"q": {Type: jev.TypeNoul, Instructions: "Is it French?"}}); err != nil {
		t.Fatal(err)
	}
	in := f.requests()[0].body.Inputs
	if n := utf8.RuneCountInString(in); n != MaxInputChars || !utf8.ValidString(in) || !strings.HasPrefix(in, "Is it French?\n\n{") {
		t.Errorf("%d characters, prefix %q", n, in[:20])
	}
}

func TestNewNeedsAnEndpoint(t *testing.T) {
	if _, err := New(jevconf.Provider{}, ""); err == nil {
		t.Error("an empty base_url was accepted")
	}
}
