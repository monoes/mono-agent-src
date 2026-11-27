package surfaces

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/hilsend"
)

// A card cannot show what an attachment holds, so a send that carries one is
// never approved from a card: it can be rejected there, and says why.
func TestASendWithAttachmentsCannotBeApprovedFromACard(t *testing.T) {
	for _, a := range []hilsend.Attachment{
		{Kind: "image", Name: "cover.png", Path: "/srv/cover.png", Size: 12034},
		{Kind: "file", Name: "offer.pdf", Path: "/srv/offer.pdf"},
		{Kind: "link", Name: "x", URL: "https://files.example.test/x"},
	} {
		p := samplePayload()
		p.Attachments = []hilsend.Attachment{a}
		msgs := mustSend(t, p)
		s := surfaceOf(t, msgs, "send-7c1e")
		for id, c := range s.Components {
			if s.DeclaresAction(id, "hil.approve") || c["component"] == "TextField" {
				t.Fatalf("%s: %s offers to approve or edit: %v", a.Kind, id, c)
			}
		}
		if !s.DeclaresAction(btnReject, "hil.reject") {
			t.Fatalf("%s: cannot be rejected", a.Kind)
		}
		why, _ := s.Components["unrev"]["text"].(string)
		if !strings.Contains(why, "attachments") || !strings.Contains(why, "Communications") {
			t.Fatalf("%s: reason %q", a.Kind, why)
		}
		if got, _ := s.Components["att-0"]["text"].(string); !strings.Contains(got, a.Name) {
			t.Fatalf("%s: not listed: %q", a.Kind, got)
		}
	}
	// the text of such a send is not cut into an error: it can still be rejected
	p := samplePayload()
	p.Attachments, p.Text = []hilsend.Attachment{{Kind: "file", Name: "a"}}, strings.Repeat("x", 30<<10)
	if s := surfaceOf(t, mustSend(t, p), "send-7c1e"); !s.DeclaresAction(btnReject, "hil.reject") {
		t.Fatal("no card to reject from")
	}
}
