package chat

import (
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// internal/ai copies dynorg.ConversationOwner (it cannot import it); the delete revokes the v2 mission tokens by
// that owner, so the two must agree.
func TestTheDeleteRevokesMissionTokensByTheOwnerDynorgMints(t *testing.T) {
	for _, c := range [][2]string{{"default", "c1"}, {"p", "9f1c"}, {"", ""}} {
		if got, want := ai.ConversationOwner(c[0], c[1]), dynorg.ConversationOwner(c[0], c[1]); got != want {
			t.Errorf("ai.ConversationOwner(%q, %q) = %q, dynorg says %q", c[0], c[1], got, want)
		}
	}
}
