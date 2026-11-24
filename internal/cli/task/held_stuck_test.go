package task

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// R16-D5: the list says when a claim cannot be renewed any more.
func TestHeldNoteShowsAStuckClaim(t *testing.T) {
	tk := tasks.Task{Claim: &tasks.Claim{By: "bot", Until: time.Now().Add(-time.Hour), Stale: true}, ClaimStuck: true}
	if got := heldNote(tk); !strings.Contains(got, "stuck") || !strings.Contains(got, "bot") {
		t.Fatalf("heldNote = %q", got)
	}
}
