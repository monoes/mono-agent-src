package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// resolve-charge is the person's verb: no agent process (a coder session token, a mission binding) can end an
// unresolved child, whatever the operator check says; a flag mix is refused; a child nobody holds unresolved is refused.
func TestNoAgentProcessResolvesAChildCharge(t *testing.T) {
	m := newMasterTurn(t) // holds the session token
	m.cfg.RequireOperator = func(string) error { return nil }
	if out, code := runSessionsCLI(t, m.cfg, "mission-children", "resolve-charge", "some-child", "--usd", "1"); code != 3 {
		t.Errorf("with a session token: exit %d %s, want the operator refusal (3)", code, out)
	}
	t.Setenv(coderhost.SessionTokenEnv, "")
	t.Setenv(agenttokens.EnvMissionToken, "some-token")
	if out, code := runSessionsCLI(t, m.cfg, "mission-children", "resolve-charge", "some-child", "--usd", "1"); code != 3 {
		t.Errorf("with a mission binding: exit %d %s", code, out)
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	if out, code := runSessionsCLI(t, m.cfg, "mission-children", "resolve-charge", "some-child"); code == 0 {
		t.Errorf("neither --usd nor --skip was accepted: %s", out)
	}
	out, code := runSessionsCLI(t, m.cfg, "mission-children", "resolve-charge", "some-child", "--usd", "1")
	if code == 0 || !strings.Contains(out, "no unresolved spend") {
		t.Errorf("a child that is not unresolved: exit %d %s", code, out)
	}
}
