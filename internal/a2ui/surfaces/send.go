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
// The text and subject are never cut: past the string cap Send returns
// ErrTooLarge. Recipient, account and detail lines are cut with Truncated.
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
	body := []string{"head"}
	tone := "info"
	if riskyAction(p.Action) {
		tone = "warning"
	}
	b.badge("action", elide(firstNonEmpty(p.Action, "send"), 40), tone)
	b.text("who", elide("To: "+firstNonEmpty(p.Recipient.Name, p.Recipient.PersonName, "(unknown)"), 300), "body")
	b.text("via", elide("Via: "+firstNonEmpty(p.Channel, p.Account.Platform)+accountSuffix(p.Account), 300), "caption")
	head := []string{"action", "who"}
	b.row("head", head...)
	body = append(body, "via")
	if l := p.PreparedBy.Label(); l != "" {
		b.text("by", elide("Prepared by: "+l, 300), "caption")
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
		b.text("subject", elide("Subject: "+p.Subject, textCap), "body")
		body = append(body, "subject")
	}
	if pending && !p.TextReadOnly {
		b.add("text", "TextField", map[string]any{"label": "Message", "value": bind("/send/text"), "variant": "longText"})
		ctx["text"] = bind("/send/text")
	} else {
		b.text("text", elide(p.Text, textCap), "body")
	}
	body = append(body, "text")
	body = append(body, sendExtras(b, p, textCap)...)

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
	return b.create(SendSurfaceID(p.ID))
}

func accountSuffix(a hilsend.Account) string {
	if a.Handle == "" {
		return ""
	}
	return " (" + a.Handle + ")"
}

// sendExtras adds the attachment, detail and host lines, a bounded number
// each, and returns their ids.
func sendExtras(b *builder, p hilsend.Payload, textCap int) []string {
	var ids []string
	line := func(id, s string) { b.text(id, elide(s, min(textCap, 300)), "caption"); ids = append(ids, id) }
	for i, a := range p.Attachments {
		if i == maxSendRows {
			line("att-more", fmt.Sprintf("… and %d more attachments", len(p.Attachments)-maxSendRows))
			break
		}
		line(fmt.Sprintf("att-%d", i), fmt.Sprintf("Attachment: %s %s (%d bytes)", a.Kind, a.Name, a.Size))
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
