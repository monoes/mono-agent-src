package jevcli

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/providers/decisionsapi" // registers the two kinds, as the binary does
	"github.com/monoes/mono-agent/internal/operator"
)

// The two native decision-API kinds through `jev provider add` (#1740): the
// vendor's host when --base-url is left out, and always a key.
func TestJevProviderAddADecisionsKind(t *testing.T) {
	for kind, host := range map[string]string{
		decisionsapi.KindOpenAI: "https://api.openai.com",
		decisionsapi.KindGemini: "https://generativelanguage.googleapis.com",
	} {
		t.Run(kind, func(t *testing.T) {
			// The tests may run inside an agent; add is the operator's.
			for _, k := range operator.MarkerEnv() {
				t.Setenv(k, "")
			}
			cfg := jevTestCfg(t, true)
			const key = "fake-decisions-credential-01"
			out, errOut, err := runJev(t, cfg, key+"\n", "provider", "add", "vendor", "--kind", kind, "--key-stdin", "--no-probe")
			if err != nil {
				t.Fatalf("%v\n%s", err, errOut)
			}
			if m := jsonMap(t, out); m["base_url"] != host || m["key_set"] != true {
				t.Fatalf("added = %s", out)
			}

			// A key is required, on this computer too, and nothing is saved.
			_, _, err = runJev(t, cfg, "", "provider", "add", "nokey", "--kind", kind, "--base-url", "http://localhost:9", "--no-probe")
			if clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "--key-stdin") {
				t.Fatalf("keyless add: %v", err)
			}
			if _, ok, _ := jevconf.FindProvider(openJevDB(t, cfg), "nokey"); ok {
				t.Fatal("a keyless record was saved")
			}

			// Leaving --base-url out of a replacement moves the provider to the
			// vendor's host, which is asked about like any move.
			if _, _, err = runJev(t, cfg, key+"\n", "provider", "add", "proxied", "--kind", kind,
				"--base-url", "https://proxy.example", "--key-stdin", "--no-probe"); err != nil {
				t.Fatal(err)
			}
			_, notes, err := runJev(t, cfg, "", "provider", "add", "proxied", "--kind", kind,
				"--secret-name", "decision-proxied", "--no-probe")
			if clicore.ExitCodeFor(err) != 3 || !strings.Contains(notes, "proxy.example") {
				t.Fatalf("an unasked move: %v\n%s", err, notes)
			}
			if p, _, _ := jevconf.FindProvider(openJevDB(t, cfg), "proxied"); p.BaseURL != "https://proxy.example" {
				t.Fatalf("moved without --yes: %+v", p)
			}
		})
	}
}
