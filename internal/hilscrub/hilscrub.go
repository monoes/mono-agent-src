// Package hilscrub removes, from the rows older versions wrote, the value of
// a secret agent access covers (#1594): a held send's card and its
// Communications row used to keep the resolved text. Every reader of those
// tables calls Ensure first, so a row is clean before anyone sees it.
package hilscrub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

func init() { storage.ReadHook = Ensure }

var done sync.Map // *sql.DB + profile -> struct{}

const budget = 4 * time.Second

func Ensure(ctx context.Context, db *sql.DB, profile string) {
	if db == nil || profile == "" {
		return
	}
	k := fmt.Sprintf("%p|%s", db, profile)
	if _, ok := done.Load(k); ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	vals := map[string]string{}
	for n, v := range secrets.GrantedValues(ctx, db, profile) {
		if scrubbable(v) {
			vals[n] = v
		}
	}
	if len(vals) == 0 {
		return
	}
	clean := true
	hit, err := findCards(ctx, db, profile, vals)
	if err != nil {
		note(err)
		return
	}
	for _, id := range hit {
		if err := scrubCard(ctx, db, profile, id, vals); err != nil {
			note(err)
			clean = false
		}
	}
	if err := scrubOrphanMessages(ctx, db, profile, vals); err != nil {
		note(err)
		clean = false
	}
	if clean {
		done.Store(k, struct{}{})
	}
}

// note logs a failed pass without any value; a read-only handle is silent.
func note(err error) {
	if strings.Contains(strings.ToLower(err.Error()), "readonly") || strings.Contains(strings.ToLower(err.Error()), "read-only") {
		return
	}
	log.Warn().Err(err).Msg("hilscrub: a pass did not finish; it is tried again at the next read")
}

// occurrences are the strings a value is written as in stored JSON.
func occurrences(vals map[string]string) []string {
	var out []string
	for _, v := range vals {
		out = append(out, v)
		if e := escaped(v); e != v {
			out = append(out, e)
		}
	}
	return out
}

func findCards(ctx context.Context, db *sql.DB, profile string, vals map[string]string) ([]string, error) {
	q := `SELECT id FROM hil_pending WHERE profile_id = ? AND (0`
	args := []interface{}{profile}
	for _, o := range occurrences(vals) {
		q += ` OR instr(readonly_data, ?) > 0 OR instr(editable_data, ?) > 0 OR instr(edited_data, ?) > 0 OR instr(node_config, ?) > 0`
		args = append(args, o, o, o, o)
	}
	rows, err := db.QueryContext(ctx, q+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

type row struct{ id, status, exec, ro, ed, edited, nc string }

func scrubbable(v string) bool {
	if len(v) < secrets.MinTrackedLen {
		return false
	}
	if len(v) < 16 && strings.IndexFunc(v, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') }) < 0 {
		return false
	}
	return true
}

func escaped(v string) string {
	j, _ := json.Marshal(v)
	return string(j[1 : len(j)-1])
}

func alnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// replaceBounded replaces v in s where it is not part of a longer word.
func replaceBounded(s, v, to string) string {
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(s[i:], v)
		if j < 0 {
			break
		}
		start, end := i+j, i+j+len(v)
		b.WriteString(s[i:start])
		if (start > 0 && alnum(s[start-1])) || (end < len(s) && alnum(s[end])) {
			b.WriteString(v)
		} else {
			b.WriteString(to)
		}
		i = end
	}
	b.WriteString(s[i:])
	return b.String()
}

// byLength is the names of vals, the longest value first, so a value that
// holds another is replaced whole.
func byLength(vals map[string]string) []string {
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(vals[names[i]]) != len(vals[names[j]]) {
			return len(vals[names[i]]) > len(vals[names[j]])
		}
		return names[i] < names[j]
	})
	return names
}

// scrubCard scrubs one card and its Communications row in one transaction,
// reading the card inside it: a claim or a finish that came first is simply
// what is scrubbed.
func scrubCard(ctx context.Context, db *sql.DB, profile, id string, vals map[string]string) error {
	return dbconn.WithWriteTx(ctx, db, func(conn *sql.Conn) error {
		var r row
		err := conn.QueryRowContext(ctx, `SELECT id, status, execution_id, readonly_data, editable_data, edited_data, node_config FROM hil_pending WHERE id = ? AND profile_id = ?`, id, profile).
			Scan(&r.id, &r.status, &r.exec, &r.ro, &r.ed, &r.edited, &r.nc)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var cfg struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal([]byte(r.nc), &cfg)
		send := cfg.Kind == "send" && strings.HasPrefix(r.exec, "send:")
		macs := map[string]string{}
		bad := false
		token := func(name string) string {
			if !send {
				return "***"
			}
			if !secrets.HeldNameOK(name) {
				bad = true
				return "***"
			}
			if macs[name] == "" {
				macs[name] = secrets.HeldNewMAC()
			}
			return secrets.HeldToken(name, macs[name])
		}
		swap := func(raw string) string {
			for _, name := range byLength(vals) {
				v := vals[name]
				if replaceBounded(raw, v, "\x00") == raw && replaceBounded(raw, escaped(v), "\x00") == raw {
					continue
				}
				t := token(name)
				raw = replaceBounded(replaceBounded(raw, escaped(v), t), v, t)
			}
			return raw
		}
		ro, ed, edited, nc := swap(r.ro), swap(r.ed), swap(r.edited), swap(r.nc)
		if bad && send && r.status == "pending" {
			if _, err := conn.ExecContext(ctx, `UPDATE hil_pending SET status = 'rejected', editable_data = '{}', readonly_data = '{}', edited_data = '{}',
				node_config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND profile_id = ?`,
				`{"kind":"send","error":"held send contained a secret that cannot be held as a reference: reject it and have the agent prepare it again"}`, r.id, profile); err != nil {
				return err
			}
			_, err := conn.ExecContext(ctx, `UPDATE person_messages SET body = '', subject = '' WHERE hil_ref = ?`, r.id)
			return err
		}
		if len(macs) > 0 {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(nc), &m) == nil {
				hashes := map[string]string{}
				if old, ok := m["refHashes"]; ok {
					_ = json.Unmarshal(old, &hashes)
				}
				for n, mac := range macs {
					hashes[n] = secrets.HeldMACHash(mac)
				}
				m["refHashes"], _ = json.Marshal(hashes)
				if b, err := json.Marshal(m); err == nil {
					nc = string(b)
				}
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE hil_pending SET readonly_data = ?, editable_data = ?, edited_data = ?, node_config = ? WHERE id = ? AND profile_id = ?`,
			ro, ed, edited, nc, r.id, profile); err != nil {
			return err
		}
		var body, subject sql.NullString
		err = conn.QueryRowContext(ctx, `SELECT body, subject FROM person_messages WHERE hil_ref = ?`, r.id).Scan(&body, &subject)
		if err == sql.ErrNoRows || strings.Contains(fmt.Sprint(err), "no such table") {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `UPDATE person_messages SET body = ?, subject = ? WHERE hil_ref = ?`, swap(body.String), swap(subject.String), r.id)
		return err
	})
}

// scrubOrphanMessages finds Communications rows that still hold a value
// although their card is clean (a pass that stopped half way, an older
// version) and masks it.
func scrubOrphanMessages(ctx context.Context, db *sql.DB, profile string, vals map[string]string) error {
	q := `SELECT id FROM person_messages WHERE profile_id = ? AND (0`
	args := []interface{}{profile}
	for _, o := range occurrences(vals) {
		q += ` OR instr(body, ?) > 0 OR instr(subject, ?) > 0`
		args = append(args, o, o)
	}
	rows, err := db.QueryContext(ctx, q+`)`, args...)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		err := dbconn.WithWriteTx(ctx, db, func(conn *sql.Conn) error {
			var body, subject sql.NullString
			if err := conn.QueryRowContext(ctx, `SELECT body, subject FROM person_messages WHERE id = ?`, id).Scan(&body, &subject); err != nil {
				return err
			}
			mask := func(t string) string {
				for _, name := range byLength(vals) {
					t = replaceBounded(replaceBounded(t, escaped(vals[name]), "***"), vals[name], "***")
				}
				return t
			}
			_, err := conn.ExecContext(ctx, `UPDATE person_messages SET body = ?, subject = ? WHERE id = ?`, mask(body.String), mask(subject.String), id)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
