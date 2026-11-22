package commsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/personitem"
)

// Kinds of a people row (people.kind).
const (
	KindPerson  = "person"
	KindCompany = "company"
)

// Account is the account the item goes out from.
type Account struct {
	Platform string `json:"platform"`
	Handle   string `json:"handle,omitempty"`
}

// Recipient is who the item is written to. Everything is optional: an item
// with none (a post) is linked to our own account instead.
type Recipient struct {
	PersonID   string `json:"personId,omitempty"` // an existing People row
	Name       string `json:"name,omitempty"`
	Handle     string `json:"handle,omitempty"` // username, or the address for an email
	ProfileURL string `json:"profileUrl,omitempty"`
	Kind       string `json:"kind,omitempty"` // person (default) | company
}

// Item is one outbound item waiting for approval.
type Item struct {
	HilRef  string // hil_pending.id
	Profile string // profile id; required
	// Source is the message source: the channel the item goes out on
	// (linkedin, x, instagram, email, outlook, gmail, ...).
	Source string
	// Platform is the People platform of the recipient and of our own account;
	// Source when empty.
	Platform    string
	Action      string // dm | comment | post | email | connect | publish | ...
	Account     Account
	Recipient   Recipient
	Subject     string
	Text        string
	Attachments interface{}
	// Meta lands in the message's metadata beside the fixed keys. The
	// workflow path puts {"workflow": {...target_nodes...}} here.
	Meta map[string]interface{}
	// TextField and SubjectField name the item field that holds the text and
	// subject, so an operator's edit of it can update the row.
	TextField, SubjectField string
}

// Result is what Queue created or found.
type Result struct {
	PersonID  string
	MessageID string
	Own       bool // the person is our own account (the item had no recipient)
}

// Queue saves the item's person (the recipient, or our own account when it
// has none) in People and its message in Communications as pending_approval.
// Queuing the same HilRef again returns the same row; an edit made meanwhile
// is kept.
func Queue(ctx context.Context, db *sql.DB, it Item) (Result, error) {
	var res Result
	if db == nil {
		return res, errors.New("commsync: no database")
	}
	if it.HilRef == "" {
		return res, errors.New("commsync: an item needs its hil ref")
	}
	profile := it.Profile
	if profile == "" {
		return res, errors.New("commsync: an item needs its profile")
	}
	platform := strings.ToLower(firstNonEmpty(it.Platform, it.Source, it.Account.Platform))
	source := strings.ToLower(firstNonEmpty(it.Source, platform, "unknown"))

	// An existing external id wins: the item was queued before.
	var existing, existingPerson string
	err := db.QueryRowContext(ctx, `SELECT id, person_id FROM person_messages WHERE hil_ref = ? LIMIT 1`, it.HilRef).Scan(&existing, &existingPerson)
	if err == nil {
		return Result{PersonID: existingPerson, MessageID: existing}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}

	personID, own, err := resolvePerson(ctx, db, profile, platform, it)
	if err != nil {
		return res, err
	}
	res.PersonID, res.Own = personID, own

	meta := map[string]interface{}{}
	for k, v := range it.Meta {
		meta[k] = v
	}
	meta["kind"] = "approval"
	meta["action"] = it.Action
	meta["channel"] = source
	meta["account"] = it.Account
	if it.Recipient != (Recipient{}) {
		meta["recipient"] = it.Recipient
	}
	if it.Attachments != nil {
		meta["attachments_pending"] = it.Attachments
	}
	if it.TextField != "" {
		meta["text_field"] = it.TextField
	}
	if it.SubjectField != "" {
		meta["subject_field"] = it.SubjectField
	}
	meta["own_account_item"] = own
	rawMeta, _ := json.Marshal(meta)

	res.MessageID = uuid.NewString()
	_, err = db.ExecContext(ctx, `
		INSERT INTO person_messages (id, person_id, source, external_id, direction, sender, subject, body,
		                             metadata, status, sent_at, profile_id, created_at, hil_ref)
		VALUES (?, ?, ?, ?, 'outbound', ?, ?, ?, ?, 'pending_approval', NULL, ?, ?, ?)
		ON CONFLICT(person_id, source, external_id) DO UPDATE SET
			subject = excluded.subject, body = excluded.body, metadata = excluded.metadata
			WHERE person_messages.status = 'pending_approval'`,
		res.MessageID, personID, source, "hil:"+it.HilRef, nullStr(it.Account.Handle), nullStr(it.Subject), nullStr(it.Text),
		string(rawMeta), profile, time.Now().UTC(), it.HilRef)
	if err != nil {
		return res, fmt.Errorf("commsync: saving message of %s: %w", it.HilRef, err)
	}
	// The conflict path keeps the old row id.
	_ = db.QueryRowContext(ctx, `SELECT id FROM person_messages WHERE person_id = ? AND source = ? AND external_id = ?`,
		personID, source, "hil:"+it.HilRef).Scan(&res.MessageID)
	return res, nil
}

// resolvePerson returns the People row the message belongs to: the recipient,
// or our own account when the item has none.
func resolvePerson(ctx context.Context, db *sql.DB, profile, platform string, it Item) (id string, own bool, err error) {
	r := it.Recipient
	if r.PersonID != "" {
		var n int
		if db.QueryRowContext(ctx, `SELECT 1 FROM people WHERE id = ? AND profile_id = ?`, r.PersonID, profile).Scan(&n) == nil {
			return r.PersonID, false, nil
		}
	}
	handle := firstNonEmpty(r.Handle, handleFor(platform, r.ProfileURL))
	if handle == "" && strings.TrimSpace(r.Name) != "" {
		// A free-text name names nobody; saving it as the username would
		// make a row no other writer can ever match.
		return "", false, fmt.Errorf("commsync: recipient %q has no handle or profile URL", r.Name)
	}
	if handle != "" {
		kind := r.Kind
		if kind != KindCompany && isCompanyURL(r.ProfileURL) {
			kind = KindCompany
		}
		if kind != KindCompany {
			kind = KindPerson
		}
		id, err = upsertPerson(ctx, db, personSpec{
			profile: profile, platform: platform, username: handle, name: r.Name, profileURL: r.ProfileURL, kind: kind,
		})
		return id, false, err
	}
	account := firstNonEmpty(it.Account.Handle, "me")
	id, err = upsertPerson(ctx, db, personSpec{
		profile: profile, platform: platform, username: account, name: it.Account.Handle, kind: KindPerson, own: true,
	})
	return id, true, err
}

type personSpec struct {
	profile, platform, username, name, profileURL, kind string
	own                                                 bool
}

// upsertPerson finds the People row of (platform, username) in the profile,
// ignoring case, or creates it (with the platform upper-cased, as the node
// writers do). Two concurrent creators of one row share it: the loser of the
// unique key reads the winner's. Known facts fill gaps; nothing already saved
// is overwritten, and neither category (the review state) nor introduction
// (the DM draft) is touched.
func upsertPerson(ctx context.Context, db *sql.DB, p personSpec) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM people
		WHERE profile_id = ? AND LOWER(platform) = LOWER(?) AND LOWER(platform_username) = LOWER(?) LIMIT 1`,
		p.profile, p.platform, p.username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = uuid.NewString()
		now := time.Now().UTC()
		_, err = db.ExecContext(ctx, `INSERT INTO people (id, platform_username, platform, full_name, profile_url, kind, profile_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(platform_username, platform, profile_id) DO NOTHING`,
			id, p.username, strings.ToUpper(p.platform), nullStr(p.name), nullStr(p.profileURL), p.kind, p.profile, now, now)
		if err == nil {
			err = db.QueryRowContext(ctx, `SELECT id FROM people
				WHERE profile_id = ? AND LOWER(platform) = LOWER(?) AND LOWER(platform_username) = LOWER(?) LIMIT 1`,
				p.profile, p.platform, p.username).Scan(&id)
		}
	} else if err == nil {
		_, err = db.ExecContext(ctx, `UPDATE people SET
			full_name = COALESCE(NULLIF(full_name,''), ?),
			profile_url = COALESCE(NULLIF(profile_url,''), ?),
			kind = CASE WHEN ? = 'company' THEN 'company' ELSE kind END,
			updated_at = ? WHERE id = ?`,
			nullStr(p.name), nullStr(p.profileURL), p.kind, time.Now().UTC(), id)
	}
	if err != nil {
		return "", fmt.Errorf("commsync: saving %s/%s in People: %w", p.platform, p.username, err)
	}
	if p.own {
		// A flag in profile_details; the review state and the DM draft keep their meaning.
		if _, err := db.ExecContext(ctx, `UPDATE people SET profile_details = json_set(COALESCE(NULLIF(profile_details,''),'{}'), '$.own_account', json('true')) WHERE id = ?`, id); err != nil {
			return "", fmt.Errorf("commsync: marking own account: %w", err)
		}
	}
	return id, nil
}

// handleFor is the username a profile URL names on platform, the way the
// platform's own adapter reads it, or the address of a mailto: link.
func handleFor(platform, raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "mailto:") {
		addr := raw[len("mailto:"):]
		if i := strings.IndexAny(addr, "?#"); i >= 0 {
			addr = addr[:i]
		}
		return strings.ToLower(strings.TrimSpace(addr))
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		// LinkedIn: the member is the segment after /in/, whatever follows it
		// (/in/alice/details/experience/ is alice).
		if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) >= 2 && parts[0] == "in" {
			return strings.TrimPrefix(parts[1], "@")
		}
		if h := personitem.Username(platform, raw); h != "" {
			return h
		}
	}
	return handleOf(raw)
}

// handleOf is the username in a profile URL or the address of a mailto: link.
func handleOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(raw, "@")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return strings.TrimPrefix(parts[len(parts)-1], "@")
}

// isCompanyURL: a LinkedIn company/school page.
func isCompanyURL(raw string) bool {
	return strings.Contains(raw, "/company/") || strings.Contains(raw, "/showcase/")
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
