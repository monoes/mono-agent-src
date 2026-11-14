package hilsend

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/hilscrub"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Store keeps send items in hil_pending (so `hil list` and the HIL badge see
// them) and the per-destination allow list in send_allow.
//
// A row's columns: node_name "send:<action>"; node_id the node type;
// execution_id "send:<id>" (no workflow waits on it); readonly_data the
// Payload without its current text; editable_data {"text","subject"} (the
// current values, edits overwrite them); node_config the replay.
// status is pending, sending (State approved), rejected, sent or failed.
type Store struct {
	DB      *sql.DB
	Profile string
}

var (
	// ErrNotFound: no such send item in this profile.
	ErrNotFound = errors.New("send item not found")
	// ErrNotSend: the HIL item exists but is not a send item.
	ErrNotSend = errors.New("not a send item")
	// ErrNotFinished: the card could not be updated after its send ran (the row
	// kept changing under the update); it stays "sending" and is not run again.
	ErrNotFinished = errors.New("the send was handled but the card could not be updated, so it still shows as sending; it is not sent again. Check the destination before preparing it again")
	// ErrAlwaysLabel: always allow asked for a send whose recipient is a
	// label, which would let any later send with that label through.
	ErrAlwaysLabel = errors.New("always allow needs an address or handle, and this send's recipient is a description that fits any send of its kind; approve it without always allow")
	// ErrNoText: a text edit of a card whose op names no text field (its
	// text says what the step sends): the approved run would send its
	// config, not the edit.
	ErrNoText = errors.New("this card's text says what the step sends, not a message: there is no text to edit; approve it as it is or reject it")
	// ErrNoSubject: a subject edit of a card whose op names no subject
	// field: the approved run would send none, while the record kept it.
	ErrNoSubject = errors.New("this send has no subject field: there is no subject to edit; approve it as it is or reject it")
	// ErrUnreviewable: an approve (always-allow included) of a card that
	// can't show all the step would send (too large, or a draft's). It can
	// only be rejected, or the step changed and run again.
	ErrUnreviewable = errors.New("this send can't be reviewed on its card, so it can't be approved: reject it, or change the step and run it again")
)

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// IsSend reports whether a hil_pending node_config is a send item's.
func IsSend(nodeConfig string) bool {
	var r struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal([]byte(nodeConfig), &r) == nil && r.Kind == "send"
}

// create stores a pending item.
func (s Store) create(ctx context.Context, p Payload, rp replay) (Payload, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	p.Kind, p.State, p.CreatedAt = "send", StatePending, now()
	if p.Attachments == nil {
		p.Attachments = []Attachment{}
	}
	rp.Kind = "send"
	hilscrub.MarkCutoff(ctx, s.DB)
	ro := p
	ro.Text, ro.Subject = "", ""
	roRaw, err := json.Marshal(ro)
	if err != nil {
		return p, err
	}
	edRaw, _ := json.Marshal(editable{Text: p.Text, Subject: p.Subject})
	rpRaw, err := json.Marshal(rp)
	if err != nil {
		return p, err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		 VALUES (?, ?, '', ?, ?, 'pending', ?, ?, ?, ?)`,
		p.ID, "send:"+p.ID, p.NodeType, "send:"+p.Action, string(roRaw), string(edRaw), string(rpRaw), s.Profile)
	if err != nil {
		return p, fmt.Errorf("storing send item: %w", err)
	}
	s.commsQueue(ctx, p, rp)
	return p, nil
}

type row struct {
	id, status, ro, ed, nc string
}

// find resolves an exact id or a unique id prefix.
func (s Store) find(ctx context.Context, id string) (row, error) {
	hilscrub.Ensure(ctx, s.DB, s.Profile)
	id = strings.TrimSpace(id)
	if id == "" {
		return row{}, ErrNotFound
	}
	// An exact id is a primary-key lookup. The prefix scan below reads every
	// pending item of the profile (an OR with LIKE cannot use the key), so it
	// runs only when the id is not exact.
	var exact row
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, status, readonly_data, editable_data, node_config FROM hil_pending
		 WHERE profile_id = ? AND id = ?`, s.Profile, id).
		Scan(&exact.id, &exact.status, &exact.ro, &exact.ed, &exact.nc)
	if err == nil {
		return exact, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return row{}, err
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, status, readonly_data, editable_data, node_config FROM hil_pending
		 WHERE profile_id = ? AND id LIKE ? ESCAPE '\' LIMIT 3`,
		s.Profile, escapeLike(id)+"%")
	if err != nil {
		return row{}, err
	}
	defer rows.Close()
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.status, &r.ro, &r.ed, &r.nc); err != nil {
			return row{}, err
		}
		found = append(found, r)
	}
	if len(found) != 1 {
		return row{}, ErrNotFound
	}
	return found[0], nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (r row) payload() (Payload, replay, error) {
	var rp replay
	if err := json.Unmarshal([]byte(r.nc), &rp); err != nil || rp.Kind != "send" {
		return Payload{}, rp, ErrNotSend
	}
	var p Payload
	if err := json.Unmarshal([]byte(r.ro), &p); err != nil {
		return Payload{}, rp, fmt.Errorf("reading send item: %w", err)
	}
	var ed editable
	_ = json.Unmarshal([]byte(r.ed), &ed)
	p.Text, p.Subject = ed.Text, ed.Subject
	p.ID = r.id
	p.State = stateOf(r.status)
	p.Error, p.ResolvedAt = rp.Error, rp.ResolvedAt
	if len(rp.Result) > 0 {
		_ = json.Unmarshal(rp.Result, &p.Result)
	}
	if p.Attachments == nil {
		p.Attachments = []Attachment{}
	}
	p.TextReadOnly = rp.TextField == ""
	p.SubjectReadOnly = rp.SubjectKey == ""
	return p, rp, nil
}

func stateOf(status string) State {
	if status == "sending" {
		return StateApproved
	}
	return State(status)
}

// Get returns one send item.
func (s Store) Get(ctx context.Context, id string) (Payload, error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return Payload{}, err
	}
	r.ro, r.ed = hilscrub.Mask(s.DB, s.Profile, r.id, r.ro), hilscrub.Mask(s.DB, s.Profile, r.id, r.ed)
	p, _, err := r.payload()
	return p, err
}

// List returns this profile's send items in states (all when none), oldest first.
func (s Store) List(ctx context.Context, states ...State) ([]Payload, error) {
	hilscrub.Ensure(ctx, s.DB, s.Profile)
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, status, readonly_data, editable_data, node_config FROM hil_pending
		 WHERE profile_id = ? AND execution_id LIKE 'send:%' ORDER BY created_at ASC, rowid ASC`, s.Profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payload{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.status, &r.ro, &r.ed, &r.nc); err != nil {
			return nil, err
		}
		r.ro, r.ed = hilscrub.Mask(s.DB, s.Profile, r.id, r.ro), hilscrub.Mask(s.DB, s.Profile, r.id, r.ed)
		p, _, err := r.payload()
		if err != nil {
			continue
		}
		if len(states) > 0 {
			keep := false
			for _, st := range states {
				keep = keep || st == p.State
			}
			if !keep {
				continue
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Allow is one always-allow rule.
type Allow struct {
	ID        string `json:"id,omitempty"`
	Action    string `json:"action"`
	Platform  string `json:"platform"`
	Account   string `json:"account"`
	Recipient string `json:"recipient"`
}

// Allowed reports whether the operator allowed this destination for good.
func (s Store) Allowed(ctx context.Context, action, platform, account, recipient string) bool {
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM send_allow WHERE profile_id = ? AND action = ? AND platform = ? AND account = ? AND recipient = ?`,
		s.Profile, action, platform, strings.ToLower(account), recipientKey(recipient)).Scan(&n)
	return err == nil && n > 0
}

// Allow records a destination as always allowed. Only the CLI's operator
// path calls it.
func (s Store) Allow(ctx context.Context, a Allow) error {
	if a.Recipient == "" || a.Action == "" {
		return errors.New("an always-allow rule needs an action and a recipient")
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT OR IGNORE INTO send_allow (id, profile_id, action, platform, account, recipient) VALUES (?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), s.Profile, a.Action, a.Platform, strings.ToLower(a.Account), recipientKey(a.Recipient))
	return err
}

// Disallow removes a rule by id; false when there was none.
func (s Store) Disallow(ctx context.Context, id string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM send_allow WHERE id = ? AND profile_id = ?`, id, s.Profile)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Allows lists the rules.
func (s Store) Allows(ctx context.Context) ([]Allow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, action, platform, account, recipient FROM send_allow WHERE profile_id = ? ORDER BY created_at, rowid`, s.Profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Allow{}
	for rows.Next() {
		var a Allow
		if err := rows.Scan(&a.ID, &a.Action, &a.Platform, &a.Account, &a.Recipient); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// recipientKey is the identity an always-allow rule matches on.
func recipientKey(r string) string { return strings.ToLower(strings.TrimSpace(r)) }

// keyOf is the best identity of a recipient: profile URL, person id, name.
func keyOf(r Recipient) string {
	for _, v := range []string{r.ProfileURL, r.PersonID, r.Name} {
		if v != "" {
			return v
		}
	}
	return ""
}

// VerifyApproval reports whether the process with this id and proof was
// started by an approved send of nodeType (see Resolve): the item must be
// mid-send and the proof must hash to the one stored at claim time.
func (s Store) VerifyApproval(ctx context.Context, id, proof, nodeType string) bool {
	if id == "" || proof == "" {
		return false
	}
	var status, nc string
	err := s.DB.QueryRowContext(ctx,
		`SELECT status, node_config FROM hil_pending WHERE id = ? AND profile_id = ?`, id, s.Profile).Scan(&status, &nc)
	if err != nil || status != "sending" {
		return false
	}
	var rp replay
	// An approval queued under a legacy name is the canonical type's.
	if json.Unmarshal([]byte(nc), &rp) != nil || rp.Kind != "send" ||
		workflow.CanonicalNodeType(rp.NodeType) != workflow.CanonicalNodeType(nodeType) || rp.ProofHash == "" {
		return false
	}
	sum := sha256.Sum256([]byte(proof))
	return hex.EncodeToString(sum[:]) == rp.ProofHash
}

// FromRow decodes the card of a hil_pending row already read (so a caller
// iterating rows need not query again); nil for an ordinary HIL item.
func FromRow(id, status, readonlyData, editableData, nodeConfig string) *Payload {
	p, _, err := (row{id: id, status: status, ro: readonlyData, ed: editableData, nc: nodeConfig}).payload()
	if err != nil {
		return nil
	}
	return &p
}
