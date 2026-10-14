package jevcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/secrets"
)

func jevKeyTestCfg(t *testing.T) *testConfig {
	t.Helper()
	keyring.MockInit()
	return jevTestCfg(t, true)
}

func TestJevKeySetStatusTestRemove(t *testing.T) {
	cfg := jevKeyTestCfg(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-typesafe-123" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"jev-latest"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)

	// No key yet: status says none, test reports ok=false with exit 0.
	out, _, err := runJev(t, cfg, "", "key", "test")
	if err != nil || !strings.Contains(out, `"ok": false`) {
		t.Fatalf("test without key: %v %s", err, out)
	}

	// Set creates "typesafe" from stdin.
	out, _, err = runJev(t, cfg, "sk-typesafe-123\n", "key", "set")
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		KeyEntry string `json:"key_entry"`
		Replaced bool   `json:"replaced"`
	}
	_ = json.Unmarshal([]byte(out), &set)
	if set.KeyEntry != "typesafe" || set.Replaced {
		t.Fatalf("set = %s", out)
	}

	out, _, err = runJev(t, cfg, "", "status")
	var st struct {
		KeySource string `json:"key_source"`
		KeyEntry  string `json:"key_entry"`
		Surfaces  []struct {
			Title  string   `json:"title"`
			Egress []string `json:"egress"`
		} `json:"surfaces"`
	}
	if err != nil || json.Unmarshal([]byte(out), &st) != nil || st.KeySource != "vault" || st.KeyEntry != "typesafe" {
		t.Fatalf("status = %v %s", err, out)
	}
	if len(st.Surfaces) == 0 || st.Surfaces[0].Title == "" || len(st.Surfaces[0].Egress) == 0 {
		t.Fatalf("surfaces lack title/egress: %s", out)
	}

	out, _, err = runJev(t, cfg, "", "key", "test")
	if err != nil || !strings.Contains(out, `"ok": true`) || !strings.Contains(out, "jev-latest") {
		t.Fatalf("test with key: %v %s", err, out)
	}

	// Setting again replaces the same entry (no second entry).
	if out, _, err = runJev(t, cfg, "sk-typesafe-123", "key", "set"); err != nil || !strings.Contains(out, `"replaced": true`) {
		t.Fatalf("second set: %v %s", err, out)
	}
	db := openJevDB(t, cfg)
	entries, _ := secrets.List(t.Context(), db, "default")
	n := 0
	for _, e := range entries {
		if e.Name == "typesafe" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("typesafe entries = %d", n)
	}

	if out, _, err = runJev(t, cfg, "", "key", "remove"); err != nil || !strings.Contains(out, `"removed": "typesafe"`) {
		t.Fatalf("remove: %v %s", err, out)
	}
	if _, _, err = runJev(t, cfg, "", "key", "remove"); clicore.ExitCodeFor(err) == 0 {
		t.Fatal("second remove should fail")
	}
}

// A key already stored under a natural name is updated in place.
func TestJevKeySetUpdatesNaturallyNamedEntry(t *testing.T) {
	cfg := jevKeyTestCfg(t)
	db := openJevDB(t, cfg)
	if _, err := secrets.Add(t.Context(), db, "default", "secret", "Jev Api key", map[string]string{"value": "old"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	out, _, err := runJev(t, cfg, "new-key", "key", "set")
	if err != nil || !strings.Contains(out, `"key_entry": "Jev Api key"`) || !strings.Contains(out, `"replaced": true`) {
		t.Fatalf("set: %v %s", err, out)
	}
	key, src, err := jevconf.ResolveKey(t.Context(), db, "default", "")
	if err != nil || key != "new-key" || src != "vault" {
		t.Fatalf("resolve = %q %q %v", key, src, err)
	}
}

func TestJevKeySetRejectsEmpty(t *testing.T) {
	cfg := jevKeyTestCfg(t)
	if _, _, err := runJev(t, cfg, "\n", "key", "set"); clicore.ExitCodeFor(err) == 0 {
		t.Fatal("empty key accepted")
	}
}
