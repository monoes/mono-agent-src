package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// ---------------------------------------------------------------------------
// Person Messages
// ---------------------------------------------------------------------------

// UpsertPersonMessage inserts a message/interaction for a person, or updates
// it if the same (person_id, source, external_id) already exists — making
// re-imports from the same source idempotent. When external_id is not
// supplied (e.g. manually logged notes), it defaults to the message's own
// generated ID so unrelated messages never collide on the unique constraint.
// profileID scopes the write to the person's profile; the message is
// rejected if personID does not belong to that profile.
func (d *Database) UpsertPersonMessage(msg *PersonMessage, profileID string) error {
	if err := requireProfile(profileID); err != nil {
		return err
	}
	if msg.ID == "" {
		msg.ID = NewID()
	}
	if msg.Direction == "" {
		msg.Direction = "inbound"
	}
	if msg.ExternalID == "" {
		msg.ExternalID = msg.ID
	}
	if msg.Status == "" {
		msg.Status = "sent"
	}
	// sent_at is stored in UTC. A time in another zone (an RFC3339 --sent-at
	// or import with an offset) is stored by the driver as Go's time text with
	// a numeric zone name, "… +0330 +0330", which it cannot read back, and
	// would order by its local time.

	var exists int
	err := d.DB.QueryRow(`SELECT 1 FROM people WHERE id = ? AND profile_id = ?`, msg.PersonID, profileID).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("person %s not found", msg.PersonID)
	}
	if err != nil {
		return fmt.Errorf("checking person %s: %w", msg.PersonID, err)
	}

	_, err = d.DB.Exec(`
		INSERT INTO person_messages (
			id, person_id, source, external_id, direction, sender, subject,
			body, metadata, status, sent_at, profile_id, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(person_id, source, external_id)
		DO UPDATE SET
			direction = excluded.direction,
			sender    = excluded.sender,
			subject   = excluded.subject,
			body      = excluded.body,
			metadata  = excluded.metadata,
			status    = excluded.status,
			sent_at   = excluded.sent_at`,
		msg.ID, msg.PersonID, msg.Source, msg.ExternalID, msg.Direction,
		nullStr(msg.Sender), nullStr(msg.Subject), nullStr(msg.Body),
		nullStr(msg.Metadata), msg.Status, nullTime(msg.SentAt.UTC()), profileID, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("upserting message for person %s: %w", msg.PersonID, err)
	}

	d.kg.start(func() {
		label := msg.Subject
		if label == "" {
			label = msg.ID
		}
		desc := fmt.Sprintf("%s message (%s) via %s", msg.Direction, msg.Status, msg.Source)
		if msg.Sender != "" {
			desc += fmt.Sprintf(", from %s", msg.Sender)
		}
		if msg.Body != "" {
			desc += ": " + msg.Body
		}
		node := monomind.KGNode{Name: label, Type: "message", Description: desc}
		// Target is the person's row ID, not display name: PersonMessage
		// only carries PersonID, and fetching the display name here would
		// add an extra query for a best-effort sync.
		edge := monomind.KGEdge{Source: label, Target: msg.PersonID, Relation: "sent_to", Description: msg.Source}
		_ = monomind.SyncToKnowledgeGraph(context.Background(), d.DB, profileID, []monomind.KGNode{node}, []monomind.KGEdge{edge}, "message:"+msg.ID)
	})

	return nil
}

// ListPersonMessages returns messages for a person within the given profile,
// optionally filtered by source, newest first. Pass an empty source to skip
// that filter.
func (d *Database) ListPersonMessages(personID, source, profileID string, limit, offset int) ([]*PersonMessage, error) {
	if err := requireProfile(profileID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT pm.id, pm.person_id, pm.source, pm.external_id, pm.direction,
		       COALESCE(pm.sender,''), COALESCE(pm.subject,''), COALESCE(pm.body,''),
		       COALESCE(pm.metadata,''), pm.status, pm.sent_at, pm.created_at, pm.read_at, COALESCE(pm.hil_ref,'')
		FROM person_messages pm
		JOIN people p ON p.id = pm.person_id
		WHERE pm.person_id = ? AND p.profile_id = ?`
	args := []interface{}{personID, profileID}

	if source != "" {
		query += " AND pm.source = ?"
		args = append(args, source)
	}
	query += " ORDER BY COALESCE(pm.sent_at, pm.created_at) DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing messages for person %s: %w", personID, err)
	}
	defer rows.Close()

	var messages []*PersonMessage
	for rows.Next() {
		m := &PersonMessage{}
		var sentAt, readAt sql.NullTime
		if err := rows.Scan(
			&m.ID, &m.PersonID, &m.Source, &m.ExternalID, &m.Direction,
			&m.Sender, &m.Subject, &m.Body, &m.Metadata, &m.Status, &sentAt, &m.CreatedAt, &readAt, &m.HilRef,
		); err != nil {
			return nil, fmt.Errorf("scanning person message row: %w", err)
		}
		if sentAt.Valid {
			m.SentAt = sentAt.Time
		}
		m.ReadAt = nullTimePtr(readAt)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// GetPersonMessage returns a single message by ID, or nil if not found.
func (d *Database) GetPersonMessage(id string) (*PersonMessage, error) {
	m := &PersonMessage{}
	var sentAt, readAt sql.NullTime
	err := d.DB.QueryRow(`
		SELECT id, person_id, source, external_id, direction,
		       COALESCE(sender,''), COALESCE(subject,''), COALESCE(body,''),
		       COALESCE(metadata,''), status, sent_at, created_at, read_at, COALESCE(hil_ref,'')
		FROM person_messages WHERE id = ?`, id,
	).Scan(
		&m.ID, &m.PersonID, &m.Source, &m.ExternalID, &m.Direction,
		&m.Sender, &m.Subject, &m.Body, &m.Metadata, &m.Status, &sentAt, &m.CreatedAt, &readAt, &m.HilRef,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting person message %s: %w", id, err)
	}
	if sentAt.Valid {
		m.SentAt = sentAt.Time
	}
	m.ReadAt = nullTimePtr(readAt)
	return m, nil
}

// UpdatePersonMessageStatus transitions a message's status (e.g. "draft" -> "sent").
// UpdatePersonMessageStatus is not synced to the knowledge graph: it only
// has the message ID and new status in scope, and re-fetching the full
// message/profile just to sync a status change isn't worth the extra query
// for a best-effort update.
func (d *Database) UpdatePersonMessageStatus(id, status string) error {
	result, err := d.DB.Exec(`UPDATE person_messages SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("updating status for message %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("message %s not found", id)
	}
	return nil
}

// UpdatePersonMessageExternalID updates the source-native id for a message,
// e.g. after send_draft resolves the new id Graph assigns when moving a
// draft into Sent Items.
func (d *Database) UpdatePersonMessageExternalID(id, externalID string) error {
	result, err := d.DB.Exec(`UPDATE person_messages SET external_id = ? WHERE id = ?`, externalID, id)
	if err != nil {
		return fmt.Errorf("updating external id for message %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("message %s not found", id)
	}
	return nil
}

// ListPersonMessagesByStatus returns messages across every person in the
// given profile filtered by status (e.g. "draft"), newest first.
func (d *Database) ListPersonMessagesByStatus(profileID, status string) ([]*PersonMessageWithPerson, error) {
	if err := requireProfile(profileID); err != nil {
		return nil, err
	}
	rows, err := d.DB.Query(`
		SELECT pm.id, pm.person_id, pm.source, pm.external_id, pm.direction,
		       COALESCE(pm.sender,''), COALESCE(pm.subject,''), COALESCE(pm.body,''),
		       COALESCE(pm.metadata,''), pm.status, pm.sent_at, pm.created_at,
		       COALESCE(p.full_name,''), p.platform_username, p.platform, pm.read_at, COALESCE(pm.hil_ref,'')
		FROM person_messages pm
		JOIN people p ON p.id = pm.person_id
		WHERE p.profile_id = ? AND pm.status = ?
		ORDER BY pm.created_at DESC`, profileID, status,
	)
	if err != nil {
		return nil, fmt.Errorf("listing person messages by status %q: %w", status, err)
	}
	defer rows.Close()

	var messages []*PersonMessageWithPerson
	for rows.Next() {
		m := &PersonMessageWithPerson{}
		var sentAt, readAt sql.NullTime
		if err := rows.Scan(
			&m.ID, &m.PersonID, &m.Source, &m.ExternalID, &m.Direction,
			&m.Sender, &m.Subject, &m.Body, &m.Metadata, &m.Status, &sentAt, &m.CreatedAt,
			&m.PersonFullName, &m.PersonPlatformUsername, &m.PersonPlatform, &readAt, &m.HilRef,
		); err != nil {
			return nil, fmt.Errorf("scanning person message row: %w", err)
		}
		if sentAt.Valid {
			m.SentAt = sentAt.Time
		}
		m.ReadAt = nullTimePtr(readAt)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// PersonMessageWithPerson pairs a message with the person it belongs to, for
// a unified cross-person view (e.g. "show me everything synced").
type PersonMessageWithPerson struct {
	PersonMessage
	PersonFullName         string `json:"person_full_name,omitempty"`
	PersonPlatformUsername string `json:"person_platform_username"`
	PersonPlatform         string `json:"person_platform"`
}

// ListAllPersonMessages returns messages across every person in the given
// profile, newest first, optionally filtered by source. Pass an empty
// source to skip that filter.
func (d *Database) ListAllPersonMessages(profileID, source string, limit, offset int) ([]*PersonMessageWithPerson, error) {
	return d.ListAllPersonMessagesFiltered(profileID, source, false, limit, offset)
}

// ListAllPersonMessagesFiltered is ListAllPersonMessages, optionally only
// the unread inbound messages.
func (d *Database) ListAllPersonMessagesFiltered(profileID, source string, unreadOnly bool, limit, offset int) ([]*PersonMessageWithPerson, error) {
	return d.ListAllPersonMessagesByStatus(profileID, source, "", unreadOnly, limit, offset)
}

// ListAllPersonMessagesByStatus is ListAllPersonMessagesFiltered, optionally
// only the messages in one status (e.g. "pending_approval"; "" = any).
func (d *Database) ListAllPersonMessagesByStatus(profileID, source, status string, unreadOnly bool, limit, offset int) ([]*PersonMessageWithPerson, error) {
	if err := requireProfile(profileID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT pm.id, pm.person_id, pm.source, pm.external_id, pm.direction,
		       COALESCE(pm.sender,''), COALESCE(pm.subject,''), COALESCE(pm.body,''),
		       COALESCE(pm.metadata,''), pm.status, pm.sent_at, pm.created_at,
		       COALESCE(p.full_name,''), p.platform_username, p.platform, pm.read_at, COALESCE(pm.hil_ref,'')
		FROM person_messages pm
		JOIN people p ON p.id = pm.person_id
		WHERE p.profile_id = ?`
	args := []interface{}{profileID}

	if source != "" {
		query += " AND pm.source = ?"
		args = append(args, source)
	}
	if status != "" {
		query += " AND pm.status = ?"
		args = append(args, status)
	}
	if unreadOnly {
		query += " AND pm.direction = 'inbound' AND pm.read_at IS NULL"
	}
	query += " ORDER BY COALESCE(pm.sent_at, pm.created_at) DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing all person messages: %w", err)
	}
	defer rows.Close()

	var messages []*PersonMessageWithPerson
	for rows.Next() {
		m := &PersonMessageWithPerson{}
		var sentAt, readAt sql.NullTime
		if err := rows.Scan(
			&m.ID, &m.PersonID, &m.Source, &m.ExternalID, &m.Direction,
			&m.Sender, &m.Subject, &m.Body, &m.Metadata, &m.Status, &sentAt, &m.CreatedAt,
			&m.PersonFullName, &m.PersonPlatformUsername, &m.PersonPlatform, &readAt, &m.HilRef,
		); err != nil {
			return nil, fmt.Errorf("scanning person message row: %w", err)
		}
		if sentAt.Valid {
			m.SentAt = sentAt.Time
		}
		m.ReadAt = nullTimePtr(readAt)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// DeletePersonMessage removes a single message by ID.
func (d *Database) DeletePersonMessage(id string) error {
	result, err := d.DB.Exec("DELETE FROM person_messages WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting person message %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("message %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Person Status Updates
// ---------------------------------------------------------------------------

// AddPersonStatusUpdate appends a new status-update entry for a person — a
// manually-written personal note, unrelated to PersonMessage.Status.
// profileID scopes the write; the update is rejected if personID does not
// belong to that profile.
func (d *Database) AddPersonStatusUpdate(personID, profileID, text string) (*PersonStatusUpdate, error) {
	if err := requireProfile(profileID); err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("status text must not be empty")
	}

	var exists int
	err := d.DB.QueryRow(`SELECT 1 FROM people WHERE id = ? AND profile_id = ?`, personID, profileID).Scan(&exists)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("person %s not found", personID)
	}
	if err != nil {
		return nil, fmt.Errorf("checking person %s: %w", personID, err)
	}

	u := &PersonStatusUpdate{
		ID:        NewID(),
		PersonID:  personID,
		Text:      text,
		CreatedAt: time.Now().UTC(),
	}
	_, err = d.DB.Exec(
		`INSERT INTO person_status_updates (id, person_id, profile_id, text, created_at) VALUES (?,?,?,?,?)`,
		u.ID, u.PersonID, profileID, u.Text, u.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("saving status update for person %s: %w", personID, err)
	}

	// Not synced to the knowledge graph: the existing person KG node is
	// keyed by display name, which isn't in scope here (only personID is) —
	// re-fetching the person just to sync this status text isn't worth the
	// extra query for a best-effort update.
	return u, nil
}

// GetLatestPersonStatusUpdate returns the most recent status update for a
// person within profileID, or (nil, nil) if none exists yet.
func (d *Database) GetLatestPersonStatusUpdate(personID, profileID string) (*PersonStatusUpdate, error) {
	if err := requireProfile(profileID); err != nil {
		return nil, err
	}
	u := &PersonStatusUpdate{}
	err := d.DB.QueryRow(`
		SELECT id, person_id, text, created_at
		FROM person_status_updates
		WHERE person_id = ? AND profile_id = ?
		ORDER BY created_at DESC LIMIT 1`, personID, profileID,
	).Scan(&u.ID, &u.PersonID, &u.Text, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest status update for person %s: %w", personID, err)
	}
	return u, nil
}

// ListPersonStatusUpdates returns every status update for a person within
// profileID, newest first. limit <= 0 means no cap.
func (d *Database) ListPersonStatusUpdates(personID, profileID string, limit int) ([]*PersonStatusUpdate, error) {
	if err := requireProfile(profileID); err != nil {
		return nil, err
	}
	query := `
		SELECT id, person_id, text, created_at
		FROM person_status_updates
		WHERE person_id = ? AND profile_id = ?
		ORDER BY created_at DESC`
	args := []interface{}{personID, profileID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing status updates for person %s: %w", personID, err)
	}
	defer rows.Close()

	var updates []*PersonStatusUpdate
	for rows.Next() {
		u := &PersonStatusUpdate{}
		if err := rows.Scan(&u.ID, &u.PersonID, &u.Text, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning status update row: %w", err)
		}
		updates = append(updates, u)
	}
	return updates, rows.Err()
}
