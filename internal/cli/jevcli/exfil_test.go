package jevcli

import (
	"testing"

	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
)

// `jev ask`, `jev models` and `jev key test` send the stored key, so the
// TYPESAFE_BASE_URL of a marked process, a chat, a workflow's child or a coder
// turn that cleared its markers does not steer it: it goes to the TypeSafe host.
func TestJevCommandsSendTheStoredKeyOnlyToTheTypeSafeHost(t *testing.T) {
	const key = "ts-STORED-KEY-jevcli-0123"
	req := `{"state":{"text":"x"},"questions":{"route":{"type":"choice","criteria":{"billing":"money","tech":"bugs"}}}}`
	for _, args := range [][]string{{"ask", "--request", "-"}, {"models"}, {"key", "test"}} {
		for _, c := range callers.All() {
			if c.Refused || c.Granted {
				continue // a command is no step: there is no unapproved ctx here
			}
			t.Run(args[0]+"/"+c.Name, func(t *testing.T) {
				cfg := jevTestCfg(t, true)
				if _, _, err := runJev(t, cfg, key+"\n", "key", "set"); err != nil {
					t.Fatal(err)
				}
				legit, envHost := jevtest.TwoHosts(t)
				c.Prepare(t)
				_, _, _ = runJev(t, cfg, req, args...)
				c.StoredKeyReach(t, legit, envHost, key, "", "")
			})
		}
	}
}
