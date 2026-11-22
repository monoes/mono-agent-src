package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

// The mission tools are wired only for a Dynamic org turn: `send --mission` on a solo chat used to be accepted and
// do nothing but cost a prose plan (#1750). It is refused, and the reason names Dynamic org.
func TestSendMissionOnASoloChatIsRefusedAndNamesDynamicOrg(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	if err := openTestChatStore(t, dbPath).SetConversationOrgMode(conv, "default", ai.OrgModeSolo); err != nil {
		t.Fatal(err)
	}
	out, code := runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "t-solo", "--mission", conv, "plan it")
	if code == 0 || !strings.Contains(out, "Dynamic org") || !strings.Contains(out, "--org dynamic") {
		t.Fatalf("a solo chat confirmed a mission: exit %d %s", code, out)
	}
	cs, closeCS := confirmStore(t, dbPath)
	defer closeCS()
	if n := cs.PendingFor("default", conv); n != 0 {
		t.Fatalf("the refused send was queued: %d", n)
	}
	if err := openTestChatStore(t, dbPath).SetConversationOrgMode(conv, "default", ai.OrgModeDynamic); err != nil {
		t.Fatal(err)
	}
	if out, code := runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "t-dyn", "--mission", conv, "plan it"); code != 0 {
		t.Fatalf("a Dynamic org chat was refused: exit %d %s", code, out)
	}
	// A send without --mission is not affected by the org mode.
	if out, code := runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "t-plain", conv, "hello"); code != 0 {
		t.Fatalf("plain send: exit %d %s", code, out)
	}
}
