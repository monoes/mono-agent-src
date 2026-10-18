package hilsend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/commsync"
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
	// ErrAlwaysLabel: always allow asked for a send whose recipient is a
	// label, which would let any later send with that label through.
	ErrAlwaysLabel = errors.New("always allow needs an address or handle, and this send's recipient is a description that fits any send of its kind; approve it without always allow")
	// ErrNoText: a text edit of a card whose op names no text field (its
	// text says what the step sends): the approved run would send its
	// config, not the edit.
	ErrNoText = errors.New("this card's text says what the step sends, not a message: there is no text to edit; approve it as it is or reject it")
)

// replay is what approving needs to run the node again.
type replay struct {
	Kind       string                   `json:"kind"`
	NodeType   string                   `json:"nodeType"`
	Config     map[string]interface{}   `json:"config"`
	Input      []map[string]interface{} `json:"input"`
	TextField  string                   `json:"textField"`
	SubjectKey string                   `json:"subjectField,omitempty"`
	ProofHash  string                   `json:"proofHash,omitempty"`
	Error      string                   `json:"error,omitempty"`
	Result     json.RawMessage          `json:"result,omitempty"`
	ResolvedAt string                   `json:"resolvedAt,omitempty"`
	// Publication is set once a registration claimed this send's approval
	// (ClaimPublicationApproval): one approval registers one publication.
	Publication string `json:"publication,omitempty"`
}

type editable struct {
	Text    string `json:"text"`
	Subject string `json:"subject,omitempty"`
}

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
	p.Kind, p.ID, p.State, p.CreatedAt = "send", uuid.NewString(), StatePending, now()
	if p.Attachments == nil {
		p.Attachments = []Attachment{}
	}
	rp.Kind = "send"
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
	id = strings.TrimSpace(id)
	if id == "" {
		return row{}, ErrNotFound
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, status, readonly_data, editable_data, node_config FROM hil_pending
		 WHERE profile_id = ? AND (id = ? OR id LIKE ? ESCAPE '\') LIMIT 3`,
		s.Profile, id, escapeLike(id)+"%")
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
		if r.id == id {
			return r, nil
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
	p, _, err := r.payload()
	return p, err
}

// List returns this profile's send items in states (all when none), oldest first.
func (s Store) List(ctx context.Context, states ...State) ([]Payload, error) {
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

// Edit changes a pending item's text and/or subject (nil leaves one alone).
// The item stays pending, so the card re-previews the edit.
func (s Store) Edit(ctx context.Context, id string, text, subject *string) (Payload, error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return Payload{}, err
	}
	p, _, err := r.payload()
	if err != nil {
		return Payload{}, err
	}
	if p.State != StatePending {
		return p, fmt.Errorf("send item %s is %s, only a pending one can be edited", p.ID, p.State)
	}
	if text != nil && p.TextReadOnly {
		return p, fmt.Errorf("send item %s: %w", p.ID, ErrNoText)
	}
	if text != nil {
		p.Text = *text
	}
	if subject != nil {
		p.Subject = *subject
	}
	raw, _ := json.Marshal(editable{Text: p.Text, Subject: p.Subject})
	res, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET editable_data = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'pending' AND profile_id = ?`,
		string(raw), p.ID, s.Profile)
	if err != nil {
		return p, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.Get(ctx, p.ID)
	}
	warn(commsync.SetText(ctx, s.DB, p.ID, &p.Text, &p.Subject), "edit")
	return p, nil
}

// claim moves a pending item to sending, once. It returns the payload and
// replay as claimed and the proof that lets the performing process through
// Guard. claimed is false when the item was not pending (already resolved,
// or someone else claimed it first). always (an always-allow approve) is
// refused for a label recipient (ErrAlwaysLabel), leaving it pending.
func (s Store) claim(ctx context.Context, id string, always bool) (p Payload, rp replay, proof string, claimed bool, err error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return p, rp, "", false, err
	}
	p, rp, err = r.payload()
	if err != nil {
		return p, rp, "", false, err
	}
	if p.State != StatePending {
		return p, rp, "", false, nil
	}
	if always && p.Recipient.Label {
		// What is claimed decides, not an earlier read: it stays pending.
		return p, rp, "", false, ErrAlwaysLabel
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return p, rp, "", false, err
	}
	proof = hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(proof))
	rp.ProofHash = hex.EncodeToString(sum[:])
	rp.ResolvedAt = now()
	raw, _ := json.Marshal(rp)
	res, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status = 'sending', node_config = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'pending' AND profile_id = ?`, string(raw), p.ID, s.Profile)
	if err != nil {
		return p, rp, "", false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := s.Get(ctx, p.ID)
		return cur, rp, "", false, gerr
	}
	p.State, p.ResolvedAt = StateApproved, rp.ResolvedAt
	s.commsStatus(ctx, p.ID, commsync.Approved, commsync.Detail{Text: &p.Text, Subject: &p.Subject})
	s.commsStatus(ctx, p.ID, commsync.Sending, commsync.Detail{})
	return p, rp, proof, true, nil
}

// reject marks a pending item rejected; false when it was not pending.
func (s Store) reject(ctx context.Context, id string) (Payload, bool, error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return Payload{}, false, err
	}
	p, rp, err := r.payload()
	if err != nil {
		return p, false, err
	}
	if p.State != StatePending {
		return p, false, nil
	}
	rp.ResolvedAt = now()
	raw, _ := json.Marshal(rp)
	res, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status = 'rejected', node_config = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'pending' AND profile_id = ?`, string(raw), p.ID, s.Profile)
	if err != nil {
		return p, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := s.Get(ctx, p.ID)
		return cur, false, gerr
	}
	p.State, p.ResolvedAt = StateRejected, rp.ResolvedAt
	s.commsStatus(ctx, p.ID, commsync.Rejected, commsync.Detail{})
	return p, true, nil
}

// finish records how a claimed send ended.
func (s Store) finish(ctx context.Context, id string, result json.RawMessage, runErr error) (Payload, error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return Payload{}, err
	}
	_, rp, err := r.payload()
	if err != nil {
		return Payload{}, err
	}
	status := "sent"
	rp.ProofHash = ""
	if runErr != nil {
		status, rp.Error = "failed", runErr.Error()
	} else {
		rp.Result = result
	}
	raw, _ := json.Marshal(rp)
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status = ?, node_config = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'sending' AND profile_id = ?`, status, string(raw), r.id, s.Profile); err != nil {
		return Payload{}, err
	}
	if runErr != nil {
		s.commsStatus(ctx, r.id, commsync.Failed, commsync.Detail{Error: runErr.Error()})
	} else {
		s.commsStatus(ctx, r.id, commsync.Sent, commsync.Detail{})
	}
	return s.Get(ctx, r.id)
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
	if json.Unmarshal([]byte(nc), &rp) != nil || rp.Kind != "send" || rp.NodeType != nodeType || rp.ProofHash == "" {
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
