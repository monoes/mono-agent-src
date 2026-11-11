package hilsend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/secrets"
)

// A held send keeps a reference to a granted secret it carries, never the
// value (#1594): the guard replaces what the run read by a token bound to the
// card, and the approval turns the tokens back into values if the grant still
// holds (secrets.ResolveHeld). Nothing else resolves: a literal @secret: in a
// card is refused at approval.

func heldMAC(key, id, name string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(id + "|" + name))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func heldVerifier(key, id string) func(name, mac string) bool {
	return func(name, mac string) bool {
		return key != "" && hmac.Equal([]byte(mac), []byte(heldMAC(key, id, name)))
	}
}

// mapStrings returns v with f applied to every string in it, map keys too.
func mapStrings(v interface{}, f func(string) (string, error)) (interface{}, error) {
	switch t := v.(type) {
	case string:
		return f(t)
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, e := range t {
			r, err := mapStrings(e, f)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, e := range t {
			nk, err := f(k)
			if err != nil {
				return nil, err
			}
			r, err := mapStrings(e, f)
			if err != nil {
				return nil, err
			}
			out[nk] = r
		}
		return out, nil
	}
	return v, nil
}

// viaJSON applies f to every string of *ptr, which round-trips through JSON.
func viaJSON(ptr interface{}, f func(string) (string, error)) error {
	raw, err := json.Marshal(ptr)
	if err != nil {
		return err
	}
	var generic interface{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return err
	}
	out, err := mapStrings(generic, f)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(out)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, ptr)
}

// heldify replaces every secret value the run read by a token bound to card
// id, in the payload and the replay. A value that cannot be a token (an
// encoded form of it, a name outside the token charset) is refused: such a
// card is not queued.
func heldify(ctx context.Context, id, key string, p *Payload, rp *replay) (bool, error) {
	minted := false
	f := func(s string) (string, error) {
		out, ok := secrets.UnresolveTracked(ctx, s, func(name string) string {
			minted = true
			return secrets.HeldToken(name, heldMAC(key, id, name))
		})
		if !ok {
			return s, fmt.Errorf("it carries a secret the run read in a form that cannot be held as a reference (encoded, or a name outside letters, digits and . _ -)")
		}
		return out, nil
	}
	if err := viaJSON(p, f); err != nil {
		return false, err
	}
	if err := viaJSON(rp, f); err != nil {
		return false, err
	}
	return minted, nil
}

// createHeld queues the card with references in place of granted secrets.
func (s Store) createHeld(ctx context.Context, p Payload, rp replay) (Payload, error) {
	p.ID = uuid.NewString()
	if len(secrets.ResolvedValues(ctx)) > 0 {
		kb := make([]byte, 32)
		if _, err := rand.Read(kb); err != nil {
			return p, err
		}
		key := hex.EncodeToString(kb)
		minted, err := heldify(ctx, p.ID, key, &p, &rp)
		if err != nil {
			return p, fmt.Errorf("this send is not queued for approval: %w", err)
		}
		if minted {
			rp.RefKey = key
		}
	}
	return s.create(ctx, p, rp)
}

// resolveHeldReplay is the config and input an approval sends, with the
// references of the card turned into values (or only checked, dry). The text
// and subject edits are in cfg already.
func (s Store) resolveHeldReplay(ctx context.Context, p Payload, rp replay, cfg map[string]interface{}, dry bool) (map[string]interface{}, []map[string]interface{}, error) {
	o := secrets.HeldOptions{Verify: heldVerifier(rp.RefKey, p.ID), NodeType: rp.NodeType, Host: p.Host, Card: p.ID, Dry: dry}
	f := func(str string) (string, error) { return secrets.ResolveHeld(ctx, s.DB, s.Profile, str, o) }
	var cfgOut map[string]interface{}
	holder := struct {
		C map[string]interface{}   `json:"c"`
		I []map[string]interface{} `json:"i"`
	}{cfg, rp.Input}
	if err := viaJSON(&holder, f); err != nil {
		return nil, nil, fmt.Errorf("send item %s: %w", p.ID, err)
	}
	cfgOut = holder.C
	return cfgOut, holder.I, nil
}

var scrubbed sync.Map // *sql.DB + profile -> struct{}

// scrubOnce replaces, in the rows of this profile that still hold the value
// of a secret agent access covers, that value by a reference (a row made
// before #1594). A pending row gets a key of its own and stays approvable; a
// finished row's tokens are inert. A pending row that cannot take a token is
// rejected. Without a grant the values are not known and nothing is touched.
func (s Store) scrubOnce(ctx context.Context) {
	k := fmt.Sprintf("%p|%s", s.DB, s.Profile)
	if _, done := scrubbed.Load(k); done {
		return
	}
	vals := secrets.GrantedValues(ctx, s.DB, s.Profile)
	if len(vals) == 0 {
		return
	}
	scrubbed.Store(k, struct{}{})
	rows, err := s.DB.QueryContext(ctx, `SELECT id, status, readonly_data, editable_data, node_config FROM hil_pending WHERE profile_id = ?`, s.Profile)
	if err != nil {
		return
	}
	var hit []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.status, &r.ro, &r.ed, &r.nc) != nil {
			continue
		}
		all := r.ro + r.ed + r.nc
		for _, v := range vals {
			j, _ := json.Marshal(v)
			if strings.Contains(all, v) || strings.Contains(all, string(j[1:len(j)-1])) {
				hit = append(hit, r)
				break
			}
		}
	}
	rows.Close()
	for _, r := range hit {
		s.scrubRow(ctx, r, vals)
	}
}

func (s Store) scrubRow(ctx context.Context, r row, vals map[string]string) {
	kb := make([]byte, 32)
	if _, err := rand.Read(kb); err != nil {
		return
	}
	key := hex.EncodeToString(kb)
	ok := true
	send := IsSend(r.nc)
	swap := func(raw string) string {
		for name, v := range vals {
			j, _ := json.Marshal(v)
			esc := string(j[1 : len(j)-1])
			if !strings.Contains(raw, v) && !strings.Contains(raw, esc) {
				continue
			}
			if !secrets.HeldNameOK(name) {
				ok = false
				continue
			}
			tok := secrets.HeldToken(name, heldMAC(key, r.id, name))
			if !send {
				tok = "***" // a workflow's card: tokens would flow on into its run
			}
			raw = strings.ReplaceAll(strings.ReplaceAll(raw, esc, tok), v, tok)
		}
		return raw
	}
	ro, ed, nc := swap(r.ro), swap(r.ed), swap(r.nc)
	if !ok {
		if r.status == "pending" {
			s.rejectScrubbed(ctx, r.id)
		}
		return
	}
	var rp replay
	if json.Unmarshal([]byte(nc), &rp) == nil && rp.Kind == "send" {
		rp.RefKey = key
		if b, err := json.Marshal(rp); err == nil {
			nc = string(b)
		}
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE hil_pending SET readonly_data = ?, editable_data = ?, node_config = ? WHERE id = ? AND profile_id = ?`, ro, ed, nc, r.id, s.Profile)
	s.scrubComms(ctx, r.id, vals, key)
}

func (s Store) rejectScrubbed(ctx context.Context, id string) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE hil_pending SET status = 'rejected', editable_data = '{}', readonly_data = '{}',
		node_config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND profile_id = ? AND status = 'pending'`,
		`{"kind":"send","error":"held send contained a secret that cannot be held as a reference: resubmit it"}`, id, s.Profile)
	_, _ = s.DB.ExecContext(ctx, `UPDATE person_messages SET body = '', subject = '' WHERE hil_ref = ?`, id)
}

// scrubComms does the same for the Communications row of the card.
func (s Store) scrubComms(ctx context.Context, id string, vals map[string]string, key string) {
	var body, subject sql.NullString
	if s.DB.QueryRowContext(ctx, `SELECT body, subject FROM person_messages WHERE hil_ref = ?`, id).Scan(&body, &subject) != nil {
		return
	}
	swap := func(t string) string {
		for name, v := range vals {
			if secrets.HeldNameOK(name) {
				t = strings.ReplaceAll(t, v, secrets.HeldToken(name, heldMAC(key, id, name)))
			}
		}
		return t
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE person_messages SET body = ?, subject = ? WHERE hil_ref = ?`, swap(body.String), swap(subject.String), id)
}
