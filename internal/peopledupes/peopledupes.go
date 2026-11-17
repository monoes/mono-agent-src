// Package peopledupes finds people stored twice under spellings that differ
// only in case, and merges them. Two kinds exist: the same username under a
// platform spelled "instagram" and "INSTAGRAM" (the CLI import lowercases,
// workflow nodes uppercase), and the same email address written with
// different case (platform "email"). The schema's UNIQUE(platform_username,
// platform, profile_id) is case-sensitive, so both slipped in.
//
// Find only reads. Apply merges each group in one transaction, after writing
// a JSON file with everything it is about to remove or re-point. No schema
// change: it uses UPDATE and DELETE on the existing tables.
package peopledupes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// Why a group is a group.
const (
	ReasonPlatformCase = "platform_case" // same username, platform differs only in case
	ReasonEmailCase    = "email_case"    // same email address, differs only in case
)

const (
	categoryApproved = "approved"
	categoryRejected = "rejected"
)

// Member is one row of a group, with what ranks it.
type Member struct {
	ID        string `json:"id"`
	Platform  string `json:"platform"`
	Username  string `json:"platform_username"`
	Category  string `json:"category,omitempty"`
	Score     int    `json:"score"`    // how many data fields the row fills
	Messages  int    `json:"messages"` // person_messages rows
	Tags      int    `json:"tags"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Group is one person stored more than once. Members is ordered best first;
// the first is the Winner.
type Group struct {
	Reason  string   `json:"reason"`
	Key     string   `json:"key"`
	Winner  string   `json:"winner"`
	Members []Member `json:"members"`
	// Repoint counts, per table, the rows that would really move to the
	// winner. Dropped counts the rows that point at a loser but would be
	// dropped with it instead, because the winner already has a row with the
	// same unique key (for a message: person, source and external id, so two
	// notes with an empty external id collide).
	Repoint map[string]int `json:"repoint"`
	Dropped map[string]int `json:"dropped,omitempty"`
	// Conflict is set when the group is not safe to merge unseen: one row
	// is approved and another rejected. Apply skips it.
	Conflict string `json:"conflict,omitempty"`
}

// Losers are the ids that would be merged into the winner.
func (g Group) Losers() []string {
	var ids []string
	for _, m := range g.Members {
		if m.ID != g.Winner {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// GroupResult is what Apply did with one group.
type GroupResult struct {
	Winner  string         `json:"winner"`
	Merged  int            `json:"merged"`            // people rows merged away
	Skipped string         `json:"skipped,omitempty"` // why the group was left alone
	Err     string         `json:"error,omitempty"`   // the group rolled back
	Moved   map[string]int `json:"moved,omitempty"`
}

// textFields are the people columns a loser can fill in on the winner when
// the winner has them empty.
var textFields = []string{"full_name", "image_url", "contact_details", "website", "follower_count",
	"introduction", "job_title", "profile_url", "headline", "location", "about", "experience",
	"education", "profile_details"}

// numFields are the integer columns; the merge keeps the largest.
var numFields = []string{"content_count", "following_count", "is_verified"}

type row struct {
	Member
	text map[string]string
	num  map[string]int64
}

func (r row) score() int {
	n := 0
	for _, f := range textFields {
		if strings.TrimSpace(r.text[f]) != "" {
			n++
		}
	}
	for _, f := range numFields {
		if r.num[f] > 0 {
			n++
		}
	}
	return n
}

func isEmail(platform string) bool { return strings.EqualFold(platform, "email") }

func groupKey(platform, username string) string {
	if isEmail(platform) {
		username = strings.ToLower(username)
	}
	return strings.ToUpper(platform) + "\x00" + username
}

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func loadRows(ctx context.Context, q queryer, profileID string, ids []string) ([]row, error) {
	cols := "id, platform, platform_username, COALESCE(category,''), COALESCE(CAST(created_at AS TEXT),'')"
	for _, f := range textFields {
		cols += ", COALESCE(CAST(" + f + " AS TEXT),'')"
	}
	for _, f := range numFields {
		cols += ", COALESCE(" + f + ",0)"
	}
	query := "SELECT " + cols + ` FROM people WHERE profile_id = ?`
	args := []any{profileID}
	if ids != nil {
		query += " AND id IN (" + placeholders(len(ids)) + ")"
		args = append(args, strs(ids)...)
	}
	rs, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading people: %w", err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		r := row{text: map[string]string{}, num: map[string]int64{}}
		dest := []any{&r.ID, &r.Platform, &r.Username, &r.Category, &r.CreatedAt}
		ts := make([]string, len(textFields))
		ns := make([]int64, len(numFields))
		for i := range ts {
			dest = append(dest, &ts[i])
		}
		for i := range ns {
			dest = append(dest, &ns[i])
		}
		if err := rs.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading a person: %w", err)
		}
		for i, f := range textFields {
			r.text[f] = ts[i]
		}
		for i, f := range numFields {
			r.num[f] = ns[i]
		}
		r.Score = r.score()
		out = append(out, r)
	}
	return out, rs.Err()
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func strs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// fkTarget is a column that references people(id).
type fkTarget struct{ table, column string }

func referencing(ctx context.Context, q queryer) ([]fkTarget, error) {
	rs, err := q.QueryContext(ctx, `SELECT m.name, f."from" FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" = 'people' ORDER BY m.name, f."from"`)
	if err != nil {
		return nil, fmt.Errorf("listing the tables that point at people: %w", err)
	}
	defer rs.Close()
	var out []fkTarget
	for rs.Next() {
		var t fkTarget
		if err := rs.Scan(&t.table, &t.column); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rs.Err()
}

// Find lists the profile's duplicate groups. It changes nothing.
func Find(ctx context.Context, db *sql.DB, profileID string) ([]Group, error) {
	rows, err := loadRows(ctx, db, profileID, nil)
	if err != nil {
		return nil, err
	}
	byKey := map[string][]row{}
	for _, r := range rows {
		k := groupKey(r.Platform, r.Username)
		byKey[k] = append(byKey[k], r)
	}
	targets, err := referencing(ctx, db)
	if err != nil {
		return nil, err
	}
	var groups []Group
	for k, rs := range byKey {
		if len(rs) < 2 {
			continue
		}
		g := Group{Key: strings.ReplaceAll(k, "\x00", " "), Repoint: map[string]int{}}
		names := map[string]bool{}
		for i := range rs {
			names[rs[i].Username] = true
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_messages WHERE person_id = ?`, rs[i].ID).Scan(&rs[i].Messages); err != nil {
				return nil, err
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM people_tags WHERE person_id = ?`, rs[i].ID).Scan(&rs[i].Tags); err != nil {
				return nil, err
			}
		}
		g.Reason = ReasonPlatformCase
		if isEmail(rs[0].Platform) && len(names) > 1 {
			g.Reason = ReasonEmailCase
		}
		sort.SliceStable(rs, func(i, j int) bool {
			a, b := rs[i], rs[j]
			switch {
			case a.Score != b.Score:
				return a.Score > b.Score
			case a.Messages+a.Tags != b.Messages+b.Tags:
				return a.Messages+a.Tags > b.Messages+b.Tags
			case a.CreatedAt != b.CreatedAt:
				return a.CreatedAt < b.CreatedAt
			}
			return a.ID < b.ID
		})
		approved, rejected := false, false
		for _, r := range rs {
			g.Members = append(g.Members, r.Member)
			approved = approved || r.Category == categoryApproved
			rejected = rejected || r.Category == categoryRejected
		}
		g.Winner = rs[0].ID
		if approved && rejected {
			g.Conflict = "one row is approved and another rejected: decide by hand"
		}
		losers := g.Losers()
		sim, err := simulateRepoint(ctx, db, g, targets)
		if err != nil {
			return nil, err
		}
		for _, t := range targets {
			k := t.table + "." + t.column
			var n int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t.table+" WHERE "+t.column+" IN ("+placeholders(len(losers))+")", strs(losers)...).Scan(&n); err != nil {
				return nil, err
			}
			if n == 0 {
				continue
			}
			left := len(sim.left[k])
			if g.Repoint[k] = n - left; g.Repoint[k] == 0 {
				delete(g.Repoint, k)
			}
			if left > 0 {
				if g.Dropped == nil {
					g.Dropped = map[string]int{}
				}
				g.Dropped[k] = left
			}
		}
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
	return groups, nil
}

// backup is the undo file: the rows of every group as they were before.
type backup struct {
	CreatedAt string        `json:"created_at"`
	ProfileID string        `json:"profile_id"`
	Note      string        `json:"note"`
	Groups    []backupGroup `json:"groups"`
}

type backupGroup struct {
	Reason       string                      `json:"reason"`
	Winner       string                      `json:"winner"`
	WinnerBefore map[string]any              `json:"winner_before"`
	Removed      []map[string]any            `json:"removed"`
	Repointed    map[string][]map[string]any `json:"repointed"`
	// DroppedMessages are the messages a merge drops with a loser (the winner
	// has one with the same unique key): the database keeps no other copy.
	// They were scrubbed (storage.EnsureMessagesScrubbed) before they were read.
	DroppedMessages []map[string]any `json:"dropped_messages,omitempty"`
}

const backupNote = "Undo by hand: re-insert each row of 'removed' into people, then for every table in " +
	"'repointed' set the named column back to the removed person's id on the rows (matched by _rowid) " +
	"that were pointing at it; restore 'winner_before' over the winner. A message is listed in " +
	"'repointed' by id and metadata columns only, never its text; one a merge drops as a duplicate " +
	"of the winner's own is listed in 'dropped_messages' with its content (scrubbed)."

func scanRows(rs *sql.Rows) ([]map[string]any, error) {
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rs.Err()
}

func snapshot(ctx context.Context, db *sql.DB, profileID string, g Group, targets []fkTarget) (backupGroup, error) {
	bg := backupGroup{Reason: g.Reason, Winner: g.Winner, Repointed: map[string][]map[string]any{}}
	all := append([]string{g.Winner}, g.Losers()...)
	rs, err := db.QueryContext(ctx, `SELECT * FROM people WHERE profile_id = ? AND id IN (`+placeholders(len(all))+`)`, append([]any{profileID}, strs(all)...)...)
	if err != nil {
		return bg, err
	}
	people, err := scanRows(rs)
	if err != nil {
		return bg, err
	}
	for _, p := range people {
		if p["id"] == g.Winner {
			bg.WinnerBefore = p
		} else {
			bg.Removed = append(bg.Removed, p)
		}
	}
	losers := g.Losers()
	sim, err := simulateRepoint(ctx, db, g, targets)
	if err != nil {
		return bg, err
	}
	for _, t := range targets {
		cols := "*"
		if t.table == "person_messages" {
			cols = messageKeyColumns // never the text: see messageKeyColumns
		}
		rs, err := db.QueryContext(ctx, "SELECT rowid AS _rowid, "+cols+" FROM "+t.table+" WHERE "+t.column+" IN ("+placeholders(len(losers))+")", strs(losers)...)
		if err != nil {
			return bg, err
		}
		rows, err := scanRows(rs)
		if err != nil {
			return bg, err
		}
		if len(rows) > 0 {
			bg.Repointed[t.table+"."+t.column] = rows
		}
		if ids := sim.left[t.table+"."+t.column]; t.table == "person_messages" && len(ids) > 0 {
			rs, err := db.QueryContext(ctx, "SELECT rowid AS _rowid, * FROM person_messages WHERE rowid IN ("+placeholders(len(ids))+")", int64s(ids)...)
			if err != nil {
				return bg, err
			}
			if bg.DroppedMessages, err = scanRows(rs); err != nil {
				return bg, err
			}
		}
	}
	return bg, nil
}

// messageKeyColumns are the columns of person_messages the undo file keeps for
// a message that is only re-pointed: what identifies it and where it was, none
// of its text (sender, subject, body, metadata). Message text is private and
// older rows may still carry a secret the scrub of #1594 has not reached.
const messageKeyColumns = "id, person_id, source, external_id, direction, status, sent_at, read_at, hil_ref, profile_id, created_at"

func int64s(in []int64) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// simulated is what re-pointing a group would do: per "table.column", how many
// rows would move and the rowids of the rows that would stay behind (to be
// dropped with the loser).
type simulated struct {
	moved map[string]int
	left  map[string][]int64
}

// simulateRepoint runs the re-pointing of mergeGroup inside a transaction it
// always rolls back, so a dry run and the undo file know exactly which rows
// the unique keys would drop. It changes nothing.
func simulateRepoint(ctx context.Context, db *sql.DB, g Group, targets []fkTarget) (simulated, error) {
	sim := simulated{moved: map[string]int{}, left: map[string][]int64{}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return sim, err
	}
	defer tx.Rollback()
	if sim.moved, err = repointTables(ctx, tx, g, targets); err != nil {
		return sim, err
	}
	losers := g.Losers()
	for _, t := range targets {
		if t.table == "person_links" {
			continue
		}
		rs, err := tx.QueryContext(ctx, "SELECT rowid FROM "+t.table+" WHERE "+t.column+" IN ("+placeholders(len(losers))+")", strs(losers)...)
		if err != nil {
			return sim, err
		}
		for rs.Next() {
			var id int64
			if err := rs.Scan(&id); err != nil {
				rs.Close()
				return sim, err
			}
			k := t.table + "." + t.column
			sim.left[k] = append(sim.left[k], id)
		}
		rs.Close()
	}
	return sim, nil
}

// repointTables moves, in every referencing table but person_links (see
// moveLinks), the rows of the losers to the winner. A row that would duplicate
// one the winner already has is ignored and stays on its loser.
func repointTables(ctx context.Context, tx *sql.Tx, g Group, targets []fkTarget) (map[string]int, error) {
	losers := g.Losers()
	moved := map[string]int{}
	for _, t := range targets {
		if t.table == "person_links" {
			continue
		}
		res, err := tx.ExecContext(ctx, "UPDATE OR IGNORE "+t.table+" SET "+t.column+" = ? WHERE "+t.column+" IN ("+placeholders(len(losers))+")",
			append([]any{g.Winner}, strs(losers)...)...)
		if err != nil {
			return nil, fmt.Errorf("re-pointing %s.%s: %w", t.table, t.column, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			moved[t.table+"."+t.column] = int(n)
		}
	}
	return moved, nil
}

// Apply merges every group that is safe to merge, each in its own
// transaction. Before it changes anything it writes backupPath (mode 0600,
// refusing to overwrite): without that file nothing is merged. A group whose
// transaction fails is rolled back whole and reported in its result; the
// other groups still go through.
func Apply(ctx context.Context, db *sql.DB, profileID string, groups []Group, backupPath string) ([]GroupResult, error) {
	targets, err := referencing(ctx, db)
	if err != nil {
		return nil, err
	}
	// A raw read of person_messages must follow the scrub of #1594, or the
	// undo file would write back a value it removed (hilscrub guard test).
	storage.EnsureMessagesScrubbed(db, profileID)
	results := make([]GroupResult, len(groups))
	doc := backup{CreatedAt: time.Now().UTC().Format(time.RFC3339), ProfileID: profileID, Note: backupNote}
	todo := make([]int, 0, len(groups))
	for i, g := range groups {
		results[i].Winner = g.Winner
		switch {
		case g.Conflict != "":
			results[i].Skipped = g.Conflict
		case len(g.Losers()) == 0:
			results[i].Skipped = "nothing to merge"
		default:
			bg, err := snapshot(ctx, db, profileID, g, targets)
			if err != nil {
				return nil, fmt.Errorf("saving the undo file: %w", err)
			}
			doc.Groups = append(doc.Groups, bg)
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		return results, nil
	}
	if err := writeBackup(backupPath, doc); err != nil {
		return nil, err
	}
	for _, i := range todo {
		moved, err := mergeGroup(ctx, db, profileID, groups[i], targets)
		if err != nil {
			results[i].Err = err.Error()
			continue
		}
		results[i].Merged = len(groups[i].Losers())
		results[i].Moved = moved
	}
	return results, nil
}

func writeBackup(path string, doc backup) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the undo file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing the undo file (nothing was merged): %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("writing the undo file (nothing was merged): %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("writing the undo file (nothing was merged): %w", err)
	}
	return f.Close()
}

func mergeGroup(ctx context.Context, db *sql.DB, profileID string, g Group, targets []fkTarget) (map[string]int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	losers := g.Losers()
	cur, err := loadRows(ctx, tx, profileID, append([]string{g.Winner}, losers...))
	if err != nil {
		return nil, err
	}
	if len(cur) != len(losers)+1 {
		return nil, fmt.Errorf("the group changed since it was listed; run people dedupe again")
	}
	byID := map[string]row{}
	for _, r := range cur {
		byID[r.ID] = r
	}
	win := byID[g.Winner]
	var ordered []row
	for _, m := range g.Members {
		if m.ID != g.Winner {
			ordered = append(ordered, byID[m.ID])
		}
	}

	moved, err := repointTables(ctx, tx, g, targets)
	if err != nil {
		return nil, err
	}
	if n, err := moveLinks(ctx, tx, profileID, g.Winner, losers); err != nil {
		return nil, err
	} else if n > 0 {
		moved["person_links"] = n
	}

	// The losers go first: the winner may take over their spelling. A row
	// that still points at a loser (a duplicate of one the winner has) goes
	// with it by ON DELETE CASCADE; one without a cascade fails the delete
	// and so rolls the group back.
	for _, id := range losers {
		if _, err := tx.ExecContext(ctx, `DELETE FROM people WHERE id = ? AND profile_id = ?`, id, profileID); err != nil {
			return nil, fmt.Errorf("removing %s: %w", id, err)
		}
	}

	var sets []string
	var args []any
	for _, f := range textFields {
		if strings.TrimSpace(win.text[f]) != "" {
			continue
		}
		for _, l := range ordered {
			if v := l.text[f]; strings.TrimSpace(v) != "" {
				sets, args = append(sets, f+" = ?"), append(args, v)
				break
			}
		}
	}
	for _, f := range numFields {
		best := win.num[f]
		for _, l := range ordered {
			if l.num[f] > best {
				best = l.num[f]
			}
		}
		if best != win.num[f] {
			sets, args = append(sets, f+" = ?"), append(args, best)
		}
	}
	// Review state: an approved person is never demoted, and keeps the
	// introduction that was approved.
	var approved *row
	for i := range ordered {
		if ordered[i].Category == categoryApproved {
			approved = &ordered[i]
			break
		}
	}
	switch {
	case win.Category == categoryApproved:
	case approved != nil:
		sets, args = append(sets, "category = ?"), append(args, categoryApproved)
		if v := approved.text["introduction"]; strings.TrimSpace(v) != "" {
			// replaces any introduction set above from another loser
			sets, args = dropSet(sets, args, "introduction = ?")
			sets, args = append(sets, "introduction = ?"), append(args, v)
		}
	case win.Category == "":
		for _, l := range ordered {
			if l.Category != "" {
				sets, args = append(sets, "category = ?"), append(args, l.Category)
				break
			}
		}
	}
	if isEmail(win.Platform) && win.Username != strings.ToLower(win.Username) {
		sets, args = append(sets, "platform_username = ?"), append(args, strings.ToLower(win.Username))
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
		args = append(args, g.Winner, profileID)
		if _, err := tx.ExecContext(ctx, "UPDATE people SET "+strings.Join(sets, ", ")+" WHERE id = ? AND profile_id = ?", args...); err != nil {
			return nil, fmt.Errorf("updating the winner: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return moved, nil
}

// dropSet removes the assignment set (and its argument) when present.
func dropSet(sets []string, args []any, set string) ([]string, []any) {
	for i, s := range sets {
		if s == set {
			return append(sets[:i:i], sets[i+1:]...), append(args[:i:i], args[i+1:]...)
		}
	}
	return sets, args
}

// moveLinks re-points person_links, which order their two ends (person_a <
// person_b) and so cannot be moved with a plain UPDATE. A link between the
// merged rows themselves disappears; one the winner already has with the
// same person is kept once.
func moveLinks(ctx context.Context, tx *sql.Tx, profileID, winner string, losers []string) (int, error) {
	ph := placeholders(len(losers))
	args := append(append([]any{profileID}, strs(losers)...), strs(losers)...)
	rs, err := tx.QueryContext(ctx, `SELECT id, person_a, person_b, relation, status, confidence, source, model, created_at, updated_at
		FROM person_links WHERE profile_id = ? AND (person_a IN (`+ph+`) OR person_b IN (`+ph+`))`, args...)
	if err != nil {
		return 0, fmt.Errorf("reading person_links: %w", err)
	}
	type link struct {
		id, a, b, relation, status, source, model, created, updated string
		confidence                                                  sql.NullFloat64
	}
	var links []link
	for rs.Next() {
		var l link
		if err := rs.Scan(&l.id, &l.a, &l.b, &l.relation, &l.status, &l.confidence, &l.source, &l.model, &l.created, &l.updated); err != nil {
			rs.Close()
			return 0, err
		}
		links = append(links, l)
	}
	rs.Close()
	isMerged := map[string]bool{winner: true}
	for _, id := range losers {
		isMerged[id] = true
	}
	moved := 0
	for _, l := range links {
		if _, err := tx.ExecContext(ctx, `DELETE FROM person_links WHERE id = ?`, l.id); err != nil {
			return 0, err
		}
		other := l.a
		if isMerged[l.a] {
			other = l.b
		}
		if isMerged[other] {
			continue
		}
		a, b := winner, other
		if b < a {
			a, b = b, a
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO person_links (id, profile_id, person_a, person_b, relation, status, confidence, source, model, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, l.id, profileID, a, b, l.relation, l.status, l.confidence, l.source, l.model, l.created, l.updated); err != nil {
			return 0, fmt.Errorf("re-pointing person_links: %w", err)
		}
		moved++
	}
	return moved, nil
}
