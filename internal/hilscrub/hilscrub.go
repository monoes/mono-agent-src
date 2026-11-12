// Package hilscrub removes, from the rows older versions wrote, the value of
// a secret agent access covers (#1594): a held send's card and its
// Communications row used to keep the resolved text. Every reader of those
// tables calls Ensure first, so a row is clean before anyone sees it.
package hilscrub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

func init() { storage.ReadHook = Ensure }

const (
	cutoffKey = "hilscrub.cutoff"
	// rowidKey is the highest hil_pending row id that existed when the cutoff
	// was set. A row is older than the cutoff only if its timestamp AND its
	// row id say so: a clock stepped backwards gives a new card an old
	// timestamp, never a low row id.
	rowidKey = "hilscrub.cutoff.rowid"
)

// noBound is the row id bound of a database that has none (no card table yet).
const noBound = int64(1<<63 - 1)

// eligible is what makes a card one the scrub rewrites (and Mask masks); the
// arguments are the profile, the cutoff and the row id bound.
const eligible = `profile_id = ? AND created_at < ? AND rowid <= ? AND execution_id NOT LIKE 'browser:%' AND execution_id NOT LIKE 'extpair:%'`

const (
	budget      = 4 * time.Second
	writeBudget = 500 * time.Millisecond // one card's transaction
	backoff     = 30 * time.Second       // no new attempt after a pass that failed
)

// state is one profile's: a pass holds mu (single flight: concurrent first
// reads wait for it and then find the marker), failedAt is the back-off and
// vals what the readers mask in memory while the rows are not clean.
type state struct {
	mu       sync.Mutex
	failedAt time.Time
	vals     map[string]string
	cutoff   string // what vals are masked against
	bound    int64
}

var passes atomic.Int64 // passes that read the vault (tests)

var states sync.Map // db pointer + profile -> *state

func stateOf(db *sql.DB, profile string) *state {
	v, _ := states.LoadOrStore(fmt.Sprintf("%p|%s", db, profile), &state{})
	return v.(*state)
}

// MarkCutoff stores, once, the moment from which cards no longer hold a value
// and the highest card row id that existed then. Every writer of a card calls
// it before its INSERT. Best effort. A database that has only the moment (set
// by an earlier version) gets the bound from the cards older than it.
func MarkCutoff(ctx context.Context, db *sql.DB) {
	if db == nil {
		return
	}
	var have int
	if db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key IN (?, ?)`, cutoffKey, rowidKey).Scan(&have) == nil && have == 2 {
		return // no write, so no wait for the lock
	}
	wctx, cancel := context.WithTimeout(ctx, writeBudget)
	defer cancel()
	tx, err := db.BeginTx(wctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(wctx, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, datetime('now'))`, cutoffKey); err != nil {
		return
	}
	// Without the card table the bound stays unset (noBound).
	_, _ = tx.ExecContext(wctx, `INSERT OR IGNORE INTO settings (key, value)
		SELECT ?, CAST(COALESCE(MAX(rowid), 0) AS TEXT) FROM hil_pending WHERE created_at < (SELECT value FROM settings WHERE key = ?)`, rowidKey, cutoffKey)
	_ = tx.Commit()
}

// cutoffs reads the moment and the row id bound.
func cutoffs(ctx context.Context, db *sql.DB) (string, int64, bool) {
	var cutoff, raw string
	if db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, cutoffKey).Scan(&cutoff) != nil || cutoff == "" {
		return "", 0, false
	}
	bound := noBound
	if db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, rowidKey).Scan(&raw) == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && n >= 0 {
			bound = n
		}
	}
	return cutoff, bound, true
}

// fingerprint is a hash of what decides which values are covered: the grant
// and the vault's entries, not their contents.
func fingerprint(ctx context.Context, db *sql.DB, profile string) string {
	var grant, vault string
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(CAST(enabled_at AS TEXT),'')||'|'||COALESCE(secret_names,'')||'|'||COALESCE(hosts,'')||'|'||COALESCE(any_host,'')||'|'||COALESCE(CAST(expires_at AS TEXT),'')||'|'||level
		FROM profile_agent_secret_access WHERE profile_id = ?`, profile).Scan(&grant)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*)||'|'||COALESCE(MAX(updated_at),'')||'|'||COALESCE(MAX(seq),0) FROM vault_secrets WHERE profile_id = ?`, profile).Scan(&vault)
	h := sha256.Sum256([]byte(grant + "#" + vault))
	return hex.EncodeToString(h[:])
}

// Mask is s, the text of card id, with the values of a pass that did not
// finish replaced by "***": a reader calls it on what it is about to show, so a
// read never shows a value just because the rows could not be written. Only a
// card the scrub would rewrite (older than the cutoff) is masked: a newer one
// is returned as it is, so Mask cannot be used to test a guess against a card.
// s itself when the rows are clean.
func Mask(db *sql.DB, profile, id, s string) string {
	if db == nil {
		return s
	}
	st := stateOf(db, profile)
	st.mu.Lock()
	vals, cutoff, bound := st.vals, st.cutoff, st.bound
	st.mu.Unlock()
	if len(vals) == 0 {
		return s
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeBudget)
	defer cancel()
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hil_pending WHERE id = ? AND `+eligible, id, profile, cutoff, bound).Scan(&n)
	if err == nil && n == 0 {
		return s
	}
	// An old card, or a row that cannot be checked: masked.
	for _, name := range byLength(vals) {
		v := vals[name]
		s = replaceBounded(replaceBounded(s, escaped(v), "***"), v, "***")
	}
	return s
}

func markerKey(profile string) string { return "hilscrub.done:" + profile }

func Ensure(ctx context.Context, db *sql.DB, profile string) {
	if db == nil || profile == "" {
		return
	}
	st := stateOf(db, profile)
	st.mu.Lock()
	defer st.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	MarkCutoff(ctx, db)
	fp := fingerprint(ctx, db, profile)
	var last string
	if db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, markerKey(profile)).Scan(&last) == nil && last == fp {
		st.vals = nil
		return
	}
	if !st.failedAt.IsZero() && time.Since(st.failedAt) < backoff {
		return
	}
	cutoff, bound, ok := cutoffs(ctx, db)
	if !ok {
		return
	}
	passes.Add(1)
	got, readable := secrets.GrantedValues(ctx, db, profile)
	if !readable {
		st.failedAt = time.Now() // not marked done: tried again after the back-off
		return
	}
	vals := map[string]string{}
	for n, v := range got {
		if scrubbable(v) {
			vals[n] = v
		}
	}
	finish := func(clean bool) {
		if clean {
			st.vals, st.failedAt = nil, time.Time{}
			wctx, cancelW := context.WithTimeout(ctx, writeBudget)
			defer cancelW()
			_, _ = db.ExecContext(wctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, markerKey(profile), fp)
			return
		}
		st.vals, st.cutoff, st.bound, st.failedAt = vals, cutoff, bound, time.Now()
	}
	if len(vals) == 0 {
		finish(true)
		return
	}
	hit, err := findCards(ctx, db, profile, cutoff, bound, vals)
	if err != nil {
		note(err)
		finish(false)
		return
	}
	clean := true
	for _, id := range hit {
		wctx, cancelW := context.WithTimeout(ctx, writeBudget)
		err := scrubCard(wctx, db, profile, id, vals)
		cancelW()
		if err != nil {
			note(err)
			clean = false
		}
	}
	finish(clean)
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

func findCards(ctx context.Context, db *sql.DB, profile, cutoff string, bound int64, vals map[string]string) ([]string, error) {
	q := `SELECT id FROM hil_pending WHERE ` + eligible + ` AND (0`
	args := []interface{}{profile, cutoff, bound}
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

// mapJSON applies f to every string (and key) of the JSON document raw, so a
// value after an escape (\n, \t, <...>) is seen as the text it is. A document
// that is not JSON is taken as one string.
func mapJSON(raw string, f func(string) string) string {
	var v interface{}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return f(raw)
	}
	var walk func(interface{}) interface{}
	walk = func(x interface{}) interface{} {
		switch t := x.(type) {
		case string:
			return f(t)
		case []interface{}:
			for i := range t {
				t[i] = walk(t[i])
			}
			return t
		case map[string]interface{}:
			out := make(map[string]interface{}, len(t))
			for k, e := range t {
				out[f(k)] = walk(e)
			}
			return out
		}
		return x
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(walk(v)) != nil {
		return raw
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

var tokenRE = regexp.MustCompile(`\[\[held-secret:([A-Za-z0-9._-]+):([0-9a-f]{32})\]\]`)

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
			Kind      string            `json:"kind"`
			RefHashes map[string]string `json:"refHashes"`
		}
		_ = json.Unmarshal([]byte(r.nc), &cfg)
		send := cfg.Kind == "send" && strings.HasPrefix(r.exec, "send:")
		macs := map[string]string{}
		// A card that already holds a reference to a name keeps its tag: the
		// value an operator typed beside it joins that reference.
		for _, m := range tokenRE.FindAllStringSubmatch(r.ed+r.edited+r.nc+r.ro, -1) {
			if _, covered := vals[m[1]]; covered && secrets.HeldMACMatches(cfg.RefHashes[m[1]], m[2]) {
				macs[m[1]] = m[2]
			}
		}
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
		swapText := func(t string) string {
			for _, name := range byLength(vals) {
				if v := vals[name]; strings.Contains(t, v) {
					t = replaceBounded(t, v, token(name))
				}
			}
			return t
		}
		swap := func(raw string) string { return mapJSON(raw, swapText) }
		ro, ed, edited, nc := swap(r.ro), swap(r.ed), swap(r.edited), swap(r.nc)
		if bad && send && r.status == "pending" {
			if _, err := conn.ExecContext(ctx, `UPDATE hil_pending SET status = 'rejected', editable_data = '{}', readonly_data = '{}', edited_data = '{}',
				node_config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND profile_id = ?`,
				`{"kind":"send","error":"held send contained a secret that cannot be held as a reference: reject it and have the agent prepare it again"}`, r.id, profile); err != nil {
				return err
			}
			_, err := conn.ExecContext(ctx, `UPDATE person_messages SET body = '', subject = '' WHERE hil_ref = ? AND profile_id = ?`, r.id, profile)
			return err
		}
		if len(macs) > 0 {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(nc), &m) == nil {
				hashes := map[string]string{}
				if old, ok := m["refHashes"]; ok {
					_ = json.Unmarshal(old, &hashes)
					if hashes == nil {
						hashes = map[string]string{}
					}
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
		err = conn.QueryRowContext(ctx, `SELECT body, subject FROM person_messages WHERE hil_ref = ? AND profile_id = ?`, r.id, profile).Scan(&body, &subject)
		if err == sql.ErrNoRows || strings.Contains(fmt.Sprint(err), "no such table") {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `UPDATE person_messages SET body = ?, subject = ? WHERE hil_ref = ? AND profile_id = ?`, swapText(body.String), swapText(subject.String), r.id, profile)
		return err
	})
}
