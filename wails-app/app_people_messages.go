package main

import (
	"strconv"
)

// PersonMessage is one entry of `people messages … --json`. The app only
// forwards what the CLI printed, so times stay the CLI's RFC 3339 strings.
type PersonMessage struct {
	ID         string  `json:"id"`
	PersonID   string  `json:"person_id"`
	Source     string  `json:"source"`
	ExternalID string  `json:"external_id,omitempty"`
	Direction  string  `json:"direction"`
	Sender     string  `json:"sender,omitempty"`
	Subject    string  `json:"subject,omitempty"`
	Body       string  `json:"body,omitempty"`
	Metadata   string  `json:"metadata,omitempty"`
	Status     string  `json:"status,omitempty"` // draft | sent | failed
	SentAt     string  `json:"sent_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
	ReadAt     *string `json:"read_at,omitempty"` // nil = unread inbound
}

// PersonMessageWithPerson is a message of the cross-person feeds
// (`people messages all|drafts`) with whom it belongs to.
type PersonMessageWithPerson struct {
	PersonMessage
	PersonFullName         string `json:"person_full_name,omitempty"`
	PersonPlatformUsername string `json:"person_platform_username"`
	PersonPlatform         string `json:"person_platform"`
}

// PersonStatusUpdate is one entry of `people status … --json`.
type PersonStatusUpdate struct {
	ID        string `json:"id"`
	PersonID  string `json:"person_id"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// GetPersonMessages lists a person's messages, newest first
// (`people messages list <id>`). Each carries read_at.
func (a *App) GetPersonMessages(personID string) []*PersonMessage {
	if a.signedOut() != nil {
		return nil
	}
	out := []*PersonMessage{}
	if err := a.runMonoCLI("", &out, "people", "messages", "list", personID, "--limit", "100"); err != nil {
		return nil
	}
	return out
}

// GetAllPersonMessages is the cross-person feed (`people messages all`).
func (a *App) GetAllPersonMessages(limit int) []*PersonMessageWithPerson {
	if a.signedOut() != nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	out := []*PersonMessageWithPerson{}
	if err := a.runMonoCLI("", &out, "people", "messages", "all", "--limit", strconv.Itoa(limit)); err != nil {
		return nil
	}
	return out
}

// MarkPersonMessagesRead marks inbound messages read — the given ids, or
// every message of personID when ids is empty (`people messages read`).
func (a *App) MarkPersonMessagesRead(personID string, ids []string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	args := []string{"people", "messages", "read"}
	if personID != "" {
		args = append(args, "--person", personID)
	}
	return a.runMonoCLI("", nil, append(args, ids...)...)
}

// MarkPersonMessageUnread marks one inbound message unread again.
func (a *App) MarkPersonMessageUnread(id string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return a.runMonoCLI("", nil, "people", "messages", "unread", id)
}

// ComposePersonMessage sends (or drafts, when asDraft is true) an email to a
// person via the given Outlook connection and records it on their message
// history — `people messages compose`.
func (a *App) ComposePersonMessage(personID, connectionID, subject, body string, asDraft bool) (*PersonMessage, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	args := []string{"people", "messages", "compose",
		"--connection=" + connectionID, "--subject=" + subject, "--body=" + body}
	if asDraft {
		args = append(args, "--draft")
	}
	var msg PersonMessage
	if err := a.runMonoCLI("", &msg, append(args, "--", personID)...); err != nil {
		return nil, err
	}
	return &msg, nil
}

// GetDraftPersonMessages returns all draft (unsent) outbound messages for the
// active profile, for review in the Human in Loop section —
// `people messages drafts`.
func (a *App) GetDraftPersonMessages() ([]*PersonMessageWithPerson, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var drafts []*PersonMessageWithPerson
	if err := a.runMonoCLI("", &drafts, "people", "messages", "drafts"); err != nil {
		return nil, err
	}
	return drafts, nil
}

// SendDraftPersonMessage sends a previously-created draft and marks it as
// sent — `people messages send-draft`.
func (a *App) SendDraftPersonMessage(personMessageID string) (*PersonMessage, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var msg PersonMessage
	if err := a.runMonoCLI("", &msg, "people", "messages", "send-draft", personMessageID); err != nil {
		return nil, err
	}
	return &msg, nil
}

// RejectDraftPersonMessage discards a draft, best-effort from the Outlook
// Drafts folder too — `people messages reject-draft`.
func (a *App) RejectDraftPersonMessage(personMessageID string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return a.runMonoCLI("", nil, "people", "messages", "reject-draft", personMessageID)
}
