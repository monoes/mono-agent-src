package surfaces

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/hilsend"
)

// A card that offers Approve shows everything the person approves: a record
// whose card would have to cut or leave out a value builds no card at all.
func TestHILApprovalNeverCutsOrOmits(t *testing.T) {
	for name, mut := range map[string]func(*HILItem){
		"long value":    func(i *HILItem) { i.ReadonlyData = map[string]any{"long": strings.Repeat("ü", 2001)} },
		"long nested":   func(i *HILItem) { i.ReadonlyData = map[string]any{"n": []any{strings.Repeat("x", 4000)}} },
		"long key":      func(i *HILItem) { i.ReadonlyData = map[string]any{strings.Repeat("k", 4001): "v"} },
		"long node":     func(i *HILItem) { i.NodeName = strings.Repeat("n", 4001) },
		"long workflow": func(i *HILItem) { i.WorkflowName = strings.Repeat("w", 4001) },
		"too many rows": func(i *HILItem) {
			i.ReadonlyData = map[string]any{}
			for n := 0; n < 40; n++ {
				i.ReadonlyData[fmt.Sprintf("field%02d", n)] = n
			}
		},
		// each value fits, the envelope does not: shorter text would be a cut
		"envelope size": func(i *HILItem) {
			i.ReadonlyData = map[string]any{}
			for n := 0; n < 17; n++ {
				i.ReadonlyData[fmt.Sprintf("k%02d", n)] = strings.Repeat("a", 4000)
			}
		},
	} {
		it := sampleHIL()
		mut(&it)
		if msgs, err := HIL(it); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: want ErrTooLarge, got err %v and %d messages", name, err, len(msgs))
		}
	}
}

// Everything at its longest that still fits is shown whole, past the shorter
// caps a title, a key or a detail line has on a card nobody can approve.
func TestHILApprovalShowsLongValuesWhole(t *testing.T) {
	it := sampleHIL()
	it.NodeName, it.WorkflowName = strings.Repeat("n", 900), strings.Repeat("w", 900)
	key, val := strings.Repeat("k", 300), strings.Repeat("v", 4000)
	it.ReadonlyData = map[string]any{key: val}
	msgs, err := HIL(it)
	if err != nil {
		t.Fatal(err)
	}
	s := surfaceOf(t, msgs, "hil-482")
	for id, want := range map[string]string{"title": it.NodeName, "sub": "Workflow: " + it.WorkflowName, "ro-0-k": key, "ro-0-v": val} {
		if got := s.Components[id]["text"]; got != want {
			t.Errorf("%s is not whole: %d bytes, want %d", id, len(fmt.Sprint(got)), len(want))
		}
	}
}

// A text value wraps in the card: a one-line field would hide its end.
func TestHILEditableStringsAreLongText(t *testing.T) {
	it := sampleHIL()
	it.EditableData = map[string]any{"a": "short", "b": strings.Repeat("x", 151), "n": float64(5), "t": true}
	msgs, err := HIL(it)
	if err != nil {
		t.Fatal(err)
	}
	s := surfaceOf(t, msgs, "hil-482")
	for id, want := range map[string]string{"ef-0": "longText", "ef-1": "longText", "ef-2": "shortText", "ef-3": "shortText"} {
		if got := s.Components[id]["variant"]; got != want {
			t.Errorf("%s: variant %v, want %s", id, got, want)
		}
	}
}

func TestSendApprovalNeverCutsOrOmits(t *testing.T) {
	long := strings.Repeat("x", 4001)
	rows := func(n int) []hilsend.Detail {
		out := make([]hilsend.Detail, n)
		for i := range out {
			out[i] = hilsend.Detail{Key: fmt.Sprintf("k%d", i), Value: "v"}
		}
		return out
	}
	for name, mut := range map[string]func(*hilsend.Payload){
		"read-only text":    func(p *hilsend.Payload) { p.TextReadOnly, p.Text = true, long },
		"read-only subject": func(p *hilsend.Payload) { p.SubjectReadOnly, p.Subject = true, long },
		"recipient":         func(p *hilsend.Payload) { p.Recipient.Name = long },
		"channel":           func(p *hilsend.Payload) { p.Channel = long },
		"account":           func(p *hilsend.Payload) { p.Account.Handle = long },
		"prepared by":       func(p *hilsend.Payload) { p.PreparedBy = hilsend.PreparedBy{System: true, Role: long} },
		"action":            func(p *hilsend.Payload) { p.Action = long },
		"detail":            func(p *hilsend.Payload) { p.Details = []hilsend.Detail{{Key: "cc", Value: long}} },
		"attachment":        func(p *hilsend.Payload) { p.Attachments = []hilsend.Attachment{{Kind: "file", Name: long}} },
		"host":              func(p *hilsend.Payload) { p.Host = long },
		"fetch url":         func(p *hilsend.Payload) { p.FetchURL = long },
		"profile url":       func(p *hilsend.Payload) { p.Recipient.ProfileURL = "https://example.test/" + long },
		"too many details":  func(p *hilsend.Payload) { p.Details = rows(maxSendRows + 1) },
		"too many attachments": func(p *hilsend.Payload) {
			p.Attachments = make([]hilsend.Attachment, maxSendRows+1)
		},
		"envelope size": func(p *hilsend.Payload) {
			p.Details = rows(maxSendRows)
			for i := range p.Details {
				p.Details[i].Value = strings.Repeat("\x01", 3900)
			}
		},
	} {
		p := samplePayload()
		mut(&p)
		if msgs, err := Send(p, "d1"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: want ErrTooLarge, got err %v and %d messages", name, err, len(msgs))
		}
		// The same record is still shown, cut, where nobody can approve it.
		for state, change := range map[string]func(*hilsend.Payload){
			"rejected":    func(p *hilsend.Payload) { p.State = hilsend.StateRejected },
			"reject only": func(p *hilsend.Payload) { p.Unreviewable = "not reviewable" },
		} {
			q := samplePayload()
			mut(&q)
			change(&q)
			msgs, err := Send(q, "d1")
			if err != nil {
				t.Errorf("%s, %s: %v", name, state, err)
				continue
			}
			if surfaceOf(t, msgs, "send-7c1e").DeclaresAction(btnApprove, "hil.approve") {
				t.Errorf("%s, %s: offers Approve", name, state)
			}
		}
	}
}

// An approvable card shows long lines whole, and where a link or a profile
// the send uses really points.
func TestSendApprovalShowsEverythingWhole(t *testing.T) {
	p := samplePayload()
	cc := strings.Repeat("someone@example.test, ", 60)
	p.Recipient = hilsend.Recipient{Name: strings.Repeat("r", 700), ProfileURL: "https://social.example.test/ada"}
	p.Details = []hilsend.Detail{{Key: "cc", Value: cc}}
	p.Attachments = []hilsend.Attachment{
		{Kind: "link", Name: "photo.jpg", URL: "https://files.example.test/a/photo.jpg"},
		{Kind: "file", Name: "offer.pdf", Path: "/srv/out/offer.pdf", Size: 10},
	}
	s := surfaceOf(t, mustSend(t, p), "send-7c1e")
	for id, want := range map[string]string{
		"who":     "To: " + p.Recipient.Name,
		"profile": "Profile: https://social.example.test/ada",
		"det-0":   "cc: " + cc,
		"att-0":   "Attachment: link photo.jpg (0 bytes) https://files.example.test/a/photo.jpg",
		"att-1":   "Attachment: file offer.pdf (10 bytes) /srv/out/offer.pdf",
	} {
		if got := s.Components[id]["text"]; got != want {
			t.Errorf("%s: %.120v, want %.120s", id, got, want)
		}
	}
	if !s.DeclaresAction(btnApprove, "hil.approve") {
		t.Fatal("no Approve")
	}
}
