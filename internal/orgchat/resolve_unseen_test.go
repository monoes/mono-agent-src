package orgchat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

func approve(t *testing.T, f *fakeClient, ref string) (*Result, error) {
	t.Helper()
	return Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: ref, Approve: true})
}

// Without a request id monomind releases every pending call of the role and
// action, so the chat approves none, however the call is addressed.
func TestApproveWithoutARequestIDIsRefused(t *testing.T) {
	for _, ref := range []string{"dev:Bash:5", "dev:Bash"} {
		f := newFake()
		// The card showed the call of ts 5; another was queued after it was drawn.
		f.approvals = []Approval{bashCall("", 5), bashCall("", 8)}
		_, err := approve(t, f, ref)
		var noID *monomind.NoRequestIDError
		if !errors.As(err, &noID) || noID.Org != "acme" || noID.Role != "dev" || noID.Action != "Bash" {
			t.Fatalf("approve %s = %v, want NoRequestIDError", ref, err)
		}
		for _, want := range []string{"nothing was sent", "update monomind", "org approve acme dev Bash", "operator only"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not say %q", err, want)
			}
		}
		if len(f.sent) != 0 || f.approvals[0].Approved != nil || f.approvals[1].Approved != nil {
			t.Fatalf("approve %s released something: %q", ref, f.sent)
		}
	}
}

// A call queued after the card was drawn is never released.
func TestApproveNeverReleasesACallQueuedAfterTheCard(t *testing.T) {
	f := newFake()
	f.approvals = []Approval{bashCall("apr-seen", 5), bashCall("apr-later", 8)}
	if _, err := approve(t, f, "apr-seen"); err != nil {
		t.Fatal(err)
	}
	// A second click releases nothing more.
	if res, err := approve(t, f, "apr-seen"); err != nil || !res.Already {
		t.Fatalf("second click = %+v, %v", res, err)
	}
	if len(f.sent) != 1 || f.sent[0] != "approve dev Bash apr-seen" || f.approvals[1].Approved != nil {
		t.Fatalf("sent %q, later = %v", f.sent, f.approvals[1].Approved)
	}
}

// A role:action ref picks whichever call is pending now, which need not be the
// one the approver saw: it approves nothing, and names the request instead.
func TestApproveByRoleAndActionIsRefusedAlsoWhenTheCallHasAnID(t *testing.T) {
	f := newFake()
	seen := bashCall("apr-seen", 5)
	done := false
	seen.Approved = &done // resolved elsewhere after the card was drawn
	f.approvals = []Approval{seen, bashCall("apr-later", 8)}
	_, err := approve(t, f, "dev:Bash")
	var byRef *UnnamedRefError
	if !errors.As(err, &byRef) || byRef.RequestID != "apr-later" {
		t.Fatalf("err = %v, want UnnamedRefError naming apr-later", err)
	}
	if !strings.Contains(err.Error(), "nothing was sent") || !strings.Contains(err.Error(), "apr-later") {
		t.Errorf("refusal %q", err)
	}
	if len(f.sent) != 0 || f.approvals[1].Approved != nil {
		t.Fatalf("released: %q", f.sent)
	}
}

// Denying is the safe direction and stays as it was.
func TestDenyWithoutARequestIDStillWorks(t *testing.T) {
	f := newFake()
	f.approvals = []Approval{bashCall("", 5), bashCall("", 8)}
	res, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "dev:Bash"})
	if err != nil || res.State != StateDenied {
		t.Fatalf("deny = %+v, %v", res, err)
	}
	if len(f.sent) != 1 || f.sent[0] != "deny dev Bash " {
		t.Fatalf("sent %q", f.sent)
	}
}

// The operator gate is asked first, as before: its refusal is the one a
// process that is not the operator gets.
func TestApproveAsksAllowBeforeTheRequestIDRule(t *testing.T) {
	f := newFake()
	f.approvals = []Approval{bashCall("", 5)}
	gate := errors.New("operator only")
	_, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "dev:Bash:5", Approve: true,
		Allow: func(string) error { return gate }})
	if !errors.Is(err, gate) || len(f.sent) != 0 {
		t.Fatalf("err = %v, sent %q", err, f.sent)
	}
}

// A role or action monomind names oddly is not written into the command a
// person might paste.
func TestTheRefusalNeverPutsOddNamesInTheCommand(t *testing.T) {
	err := &monomind.NoRequestIDError{Org: "acme", Role: "dev; echo pasted", Action: "Ba‮sh"}
	msg := err.Error()
	if !strings.Contains(msg, "org approve acme <role> <action>") || strings.Contains(msg, "‮") {
		t.Fatalf("refusal %q", msg)
	}
}
