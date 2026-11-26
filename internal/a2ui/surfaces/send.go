package surfaces

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/hilsend"
)

// SendSurfaceID is the surface id of a send approval.
func SendSurfaceID(id string) string { return "send-" + id }

// maxSendRows bounds the attachment and detail lines of a send card.
const maxSendRows = 8

// riskyAction reports whether the action puts something in front of other
// people, which the card marks with a warning badge.
func riskyAction(action string) bool {
	switch action {
	case hilsend.ActionPost, hilsend.ActionComment, hilsend.ActionPublish, hilsend.ActionDM, hilsend.ActionEmail:
		return true
	}
	return false
}

func stateTone(s hilsend.State) string {
	switch s {
	case hilsend.StateSent:
		return "success"
	case hilsend.StateFailed:
		return "danger"
	case hilsend.StateRejected:
		return "neutral"
	}
	return "info"
}

// Send builds the approval surface of one send item. A pending item gets the
// editable text (and subject, when the node has one) and Approve/Reject
// buttons; an item that is not pending, or one the person can only reject
// (Unreviewable), gets no Approve button, and a settled one gets no buttons.
// A card with Approve shows everything: when any line or row would be cut or
// left out, Send returns ErrTooLarge. The editable text and subject of a
// pending item are never cut either. On the other cards, recipient, account
// and detail lines are cut with Truncated.
func Send(p hilsend.Payload) ([][]byte, error) {
	if err := checkID("send", p.ID); err != nil {
		return nil, err
	}
	pending := p.State == hilsend.StatePending
	var text, subject string
	var err error
	if pending {
		if text, err = editable("text", p.Text); err != nil {
			return nil, err
		}
		if subject, err = editable("subject", p.Subject); err != nil {
			return nil, err
		}
	}
	return shrinking(func(textCap int) ([][]byte, error) { return buildSend(p, text, subject, textCap) })
}

func buildSend(p hilsend.Payload, text, subject string, textCap int) ([][]byte, error) {
	pending := p.State == hilsend.StatePending
	canApprove := pending && p.Unreviewable == ""
	b := newBuilder()
	// A card nobody can approve keeps its lines short; one with Approve shows
	// them whole, or is not built (b.whole below).
	limit := func(short int) int {
		if canApprove {
			return textCap
		}
		return min(textCap, short)
	}
	body := []string{"head"}
	tone := "info"
	if riskyAction(p.Action) {
		tone = "warning"
	}
	b.badge("action", b.fit(firstNonEmpty(p.Action, "send"), limit(40)), tone)
	b.text("who", b.fit("To: "+firstNonEmpty(p.Recipient.Name, p.Recipient.PersonName, "(unknown)"), limit(300)), "body")
	if u := p.Recipient.ProfileURL; u != "" && u != p.Recipient.Name {
		b.text("profile", b.fit("Profile: "+u, limit(300)), "caption")
		body = append(body, "profile")
	}
	b.text("via", b.fit("Via: "+firstNonEmpty(p.Channel, p.Account.Platform)+accountSuffix(p.Account), limit(300)), "caption")
	b.row("head", "action", "who")
	body = append(body, "via")
	if l := p.PreparedBy.Label(); l != "" {
		b.text("by", b.fit("Prepared by: "+l, limit(300)), "caption")
		body = append(body, "by")
	}
	if !pending {
		b.badge("state", string(p.State), stateTone(p.State))
		body = append(body, "state")
		if p.Error != "" {
			b.text("err", elide(p.Error, textCap), "caption")
			body = append(body, "err")
		}
	}
	if p.Unreviewable != "" {
		b.text("unrev", elide("Cannot be approved here: "+p.Unreviewable, textCap), "caption")
		body = append(body, "unrev")
	}

	ctx := map[string]any{"id": p.ID}
	b.data["send"] = map[string]any{"text": text, "subject": subject}
	if pending && subject != "" && !p.SubjectReadOnly {
		b.add("subject", "TextField", map[string]any{"label": "Subject", "value": bind("/send/subject"), "variant": "shortText"})
		ctx["subject"] = bind("/send/subject")
		body = append(body, "subject")
	} else if p.Subject != "" {
		b.text("subject", b.fit("Subject: "+p.Subject, textCap), "body")
		body = append(body, "subject")
	}
	if pending && !p.TextReadOnly {
		b.add("text", "TextField", map[string]any{"label": "Message", "value": bind("/send/text"), "variant": "longText"})
		ctx["text"] = bind("/send/text")
	} else {
		b.text("text", b.fit(p.Text, textCap), "body")
	}
	body = append(body, "text")
	if canApprove && (len(p.Attachments) > maxSendRows || len(p.Details) > maxSendRows) {
		return nil, fmt.Errorf("%w: %d attachments and %d details, at most %d of each fit on a card",
			ErrTooLarge, len(p.Attachments), len(p.Details), maxSendRows)
	}
	body = append(body, sendExtras(b, p, limit(300))...)

	if canApprove {
		b.button(btnApprove, "Approve", "primary", "hil.approve", ctx)
	}
	if pending {
		b.button(btnReject, "Reject", "danger", "hil.reject", map[string]any{"id": p.ID})
		if canApprove {
			b.row("actions", btnApprove, btnReject)
		} else {
			b.row("actions", btnReject)
		}
		body = append(body, "actions")
	}
	b.column("body", body...)
	b.add("root", "Card", map[string]any{"child": "body"})
	if canApprove {
		if err := b.whole(); err != nil {
			return nil, err
		}
	}
	return b.create(SendSurfaceID(p.ID))
}

func accountSuffix(a hilsend.Account) string {
	if a.Handle == "" {
		return ""
	}
	return " (" + a.Handle + ")"
}

// sendExtras adds the attachment, detail and host lines, a bounded number
// each and each at most lineCap bytes, and returns their ids.
func sendExtras(b *builder, p hilsend.Payload, lineCap int) []string {
	var ids []string
	line := func(id, s string) { b.text(id, b.fit(s, lineCap), "caption"); ids = append(ids, id) }
	for i, a := range p.Attachments {
		if i == maxSendRows {
			line("att-more", fmt.Sprintf("… and %d more attachments", len(p.Attachments)-maxSendRows))
			break
		}
		s := fmt.Sprintf("Attachment: %s %s (%d bytes)", a.Kind, a.Name, a.Size)
		// The name is only the last part of where a link or a file is.
		if from := firstNonEmpty(a.URL, a.Path); from != "" && from != a.Name {
			s += " " + from
		}
		line(fmt.Sprintf("att-%d", i), s)
	}
	for i, d := range p.Details {
		if i == maxSendRows {
			line("det-more", fmt.Sprintf("… and %d more details", len(p.Details)-maxSendRows))
			break
		}
		line(fmt.Sprintf("det-%d", i), d.Key+": "+d.Value)
	}
	if p.Host != "" {
		line("host", "Through: "+p.Host)
	}
	if p.FetchURL != "" {
		line("fetch", "Fetched from: "+p.FetchURL)
	}
	return ids
}
