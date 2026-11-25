package decisionsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// A record added with no base_url is completed with the vendor's host before
// its key is bound to a host; one that names a host keeps it.
func TestTheKindsFillTheVendorsHost(t *testing.T) {
	for kind, host := range map[string]string{
		KindOpenAI: "https://api.openai.com",
		KindGemini: "https://generativelanguage.googleapis.com",
	} {
		if got := jevconf.WithKindDefaults(jevconf.Provider{Kind: " " + kind + " "}).BaseURL; got != host {
			t.Errorf("%s: base_url %q, want %q", kind, got, host)
		}
		named := jevconf.Provider{Kind: kind, BaseURL: "https://proxy.example/"}
		if got := jevconf.WithKindDefaults(named).BaseURL; got != "https://proxy.example" {
			t.Errorf("%s: a named base_url became %q", kind, got)
		}
	}
}

// A base_url that already ends in the API version (as an "openai" provider's
// does) does not get the version twice.
func TestAVersionedBaseURLIsNotDoubled(t *testing.T) {
	for _, c := range []struct{ kind, suffix, want string }{
		{KindOpenAI, "/v1", "/v1/decisions"},
		{KindOpenAI, "/proxy/v1/", "/proxy/v1/decisions"},
		{KindOpenAI, "/proxy", "/proxy/v1/decisions"},
		{KindGemini, "/v1beta", "/v1beta/decisions"},
		{KindGemini, "", "/v1beta/decisions"},
	} {
		b, err := New(c.kind, jevconf.Provider{BaseURL: "http://127.0.0.1:1" + c.suffix}, "k")
		if err != nil {
			t.Fatal(err)
		}
		if want := "http://127.0.0.1:1" + c.want; b.url != want {
			t.Errorf("%s %q: url %q, want %q", c.kind, c.suffix, b.url, want)
		}
	}
}

// The model a call names is the model sent; a call that names none uses the
// record's.
func TestACallsModelIsSent(t *testing.T) {
	for _, kind := range []string{KindOpenAI, KindGemini} {
		var sent []string
		b := backend(t, kind, func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Model string `json:"model"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &in)
			sent = append(sent, in.Model)
			if kind == KindGemini {
				io.WriteString(w, `{"answers":{"a":{"type":"noul","noul":0.6}}}`)
				return
			}
			io.WriteString(w, `{"answers":[{"type":"predicate","name":"a","probability":0.6}]}`)
		})
		for _, model := range []string{"named-by-the-call", ""} {
			resp, err := b.Answer(context.Background(), &jev.Request{Model: model,
				Questions: map[string]jev.Question{"a": {Type: jev.TypeNoul}}})
			if err != nil {
				t.Fatal(err)
			}
			want := model
			if want == "" {
				want = b.model
			}
			if got := sent[len(sent)-1]; got != want || resp.Model != want {
				t.Errorf("%s: sent %q, reported %q, want %q", kind, got, resp.Model, want)
			}
		}
	}
}
