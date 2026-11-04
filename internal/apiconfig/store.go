package apiconfig

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// Row is the key of the saved settings in the settings table (key TEXT PRIMARY KEY, value
// TEXT NOT NULL). The value is a JSON object, {"v":1, "<setting key>": ...}, every field
// optional. The settings are the machine's, not a profile's: one daemon serves the API.
const Row = "api_gateway_config"

// FormatVersion is the version of the document this binary reads and writes.
const FormatVersion = 1

// ErrTooNew is wrapped by the error of a document written in a newer format. Such a
// document is never rewritten: a binary that cannot read all of it would drop the rest.
var ErrTooNew = errors.New("saved API settings are in a newer format")

// ErrDamaged is wrapped by the error of a row that cannot be decoded: its value is not a JSON
// object, its version is not a whole number from 1, or a field this binary knows has the wrong
// type. Apply with All removes such a row, which nothing else can read. A failing database is
// not this, and a row in a newer format is ErrTooNew, which nothing removes.
var ErrDamaged = errors.New("saved API settings are damaged")

// RemoveRowSQL removes the row by hand: for a row in a newer format, which this version does
// not remove because it would lose what the newer version saved.
const RemoveRowSQL = "delete from settings where key = '" + Row + "'"

// DamagedMessage is how the message of every error of a damaged row starts, whatever is wrong
// with the row. A surface that has only the exit code and the last line of stderr (the desktop
// app) tells damage from any other failure by it, and offers the repair. The rest of the message
// says what is wrong and names the repair with its gate.
const DamagedMessage = "the saved settings are damaged"

// damagedError is the error of a row that cannot be decoded: DamagedMessage, what is wrong with
// the row (never what it held), and the repair with its gate. The CLI maps it to exit 3.
type damagedError struct{ what string }

func (e *damagedError) Error() string {
	return DamagedMessage + " (" + e.what + "); `monoagentcli api config unset --all --yes` removes them"
}
func (e *damagedError) Unwrap() error { return ErrDamaged }

// Load reads the saved settings: empty when nothing is saved. The values are not checked
// (Validate does), so that an invalid one can still be shown and removed. A document that is
// not a JSON object (an empty value included), whose version is not a whole number from 1, or
// one of whose known fields has the wrong type is an ErrDamaged; one in a newer format is an
// ErrTooNew.
func Load(ctx context.Context, db *sql.DB) (Settings, error) {
	var doc string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, Row).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("reading the saved API settings: %w", err)
	}
	return decode(doc)
}

// Update changes the saved settings in one BEGIN IMMEDIATE transaction on a connection of
// its own: it reads the document, hands it to fn, and writes what fn left. Two writers (the
// CLI and the app, two MCP calls) take turns, so neither loses the other's change. fn runs
// holding the database's write lock and must not wait for anything. An error from fn, or a
// document Load would refuse, rolls everything back. Nothing is written when fn changed
// nothing, and a document left with no setting and no field of a newer binary's is deleted.
func Update(ctx context.Context, db *sql.DB, fn func(*Settings) error) error {
	_, err := update(ctx, db, updateOpts{}, func(s *Settings, _ bool) error { return fn(s) })
	return err
}

// updateOpts says how update runs: dry runs fn over the stored document and rolls back, and
// repair lets a row that cannot be decoded (ErrDamaged, never ErrTooNew) be replaced by what fn
// leaves of an empty document, which is how Apply's unset all removes it.
type updateOpts struct{ dry, repair bool }

// update is Update with its options. fn is told whether it is repairing: the stored row cannot
// be decoded, and the settings it is handed are an empty document in its place. update reports
// whether it found a row it had to repair (with dry set, whether it would), and fn runs before
// anything is written, so it can refuse the repair.
func update(ctx context.Context, db *sql.DB, opts updateOpts, fn func(s *Settings, repairing bool) error) (repaired bool, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("saved API settings: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return false, fmt.Errorf("saved API settings: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			// The connection may still be inside the transaction: it must not go back to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	var doc string
	found := true
	switch err := conn.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, Row).Scan(&doc); {
	case errors.Is(err, sql.ErrNoRows):
		found = false
	case err != nil:
		return false, fmt.Errorf("reading the saved API settings: %w", err)
	}
	var s Settings
	if found { // a row whose value is empty is a row, and is not JSON
		var derr error
		if s, derr = decode(doc); derr != nil {
			if !opts.repair || !errors.Is(derr, ErrDamaged) {
				return false, derr
			}
			s, repaired = Settings{}, true
		}
	}
	before := s
	if err := fn(&s, repaired); err != nil {
		return false, err
	}
	if opts.dry {
		return repaired, nil
	}
	if repaired || !sameValues(before, s) {
		text, empty := encode(s)
		if empty {
			_, err = conn.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, Row)
		} else {
			_, err = conn.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, Row, text)
		}
		if err != nil {
			return false, fmt.Errorf("saving the API settings: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, fmt.Errorf("saving the API settings: %w", err)
	}
	committed = true
	return repaired, nil
}

func sameValues(a, b Settings) bool {
	for _, k := range Keys() {
		if a.Get(k) != b.Get(k) {
			return false
		}
	}
	return true
}

// decode reads a stored document. Fields it does not know are kept in Settings.extra.
func decode(doc string) (Settings, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &fields); err != nil || fields == nil {
		return Settings{}, &damagedError{fmt.Sprintf("settings table, key %s: not a JSON object", Row)}
	}
	if raw, ok := fields["v"]; ok {
		v, ok := wholeNumber(raw)
		if !ok || v < 1 {
			return Settings{}, &damagedError{fmt.Sprintf("settings table, key %s: its version is not a whole number from 1", Row)}
		}
		if v > FormatVersion {
			return Settings{}, fmt.Errorf("%w: the row %s is in format %d and this monoagentcli reads format %d; use the monoagentcli that wrote it, "+
				"or remove the row by hand with `sqlite3 <database> \"%s\"` (the database this command opened, ~/.monoagent/monoagent.db unless --db-path names another); nothing was changed",
				ErrTooNew, Row, v, FormatVersion, RemoveRowSQL)
		}
		delete(fields, "v")
	}
	var s Settings
	for _, key := range Keys() {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		delete(fields, key)
		text, err := textOf(key, raw)
		if err != nil {
			return Settings{}, &damagedError{fmt.Sprintf("settings table, key %s: %v", Row, err)}
		}
		_ = s.Set(key, text)
	}
	if len(fields) > 0 {
		s.extra = fields
	}
	return s, nil
}

// wholeNumber reads a JSON number that is an integer.
func wholeNumber(raw json.RawMessage) (int, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(n.String())
	return i, err == nil
}

// textOf reads the value of a setting: a string, or for max_concurrent a number; null is
// not saved.
func textOf(key string, raw json.RawMessage) (string, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return "", fmt.Errorf("%s is not valid JSON", key)
	}
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case json.Number:
		if key == KeyMaxConcurrent {
			return x.String(), nil
		}
	}
	if key == KeyMaxConcurrent {
		return "", fmt.Errorf("%s must be a number or a string", key)
	}
	return "", fmt.Errorf("%s must be a string", key)
}

// encode writes the document: the fields of a newer binary as they were, v, and each saved
// setting, max_concurrent as a number when it is a whole number. It reports true when the
// document would hold nothing.
func encode(s Settings) (string, bool) {
	m := make(map[string]json.RawMessage, len(s.extra)+len(Keys())+1)
	for k, v := range s.extra {
		m[k] = v
	}
	m["v"] = json.RawMessage(strconv.Itoa(FormatVersion))
	for _, key := range Keys() {
		text := s.Get(key)
		if text == "" {
			continue
		}
		if n, err := strconv.Atoi(text); key == KeyMaxConcurrent && err == nil && n >= 0 && strconv.Itoa(n) == text {
			m[key] = json.RawMessage(text)
			continue
		}
		b, _ := json.Marshal(text)
		m[key] = b
	}
	if len(m) == 1 {
		return "", true
	}
	b, _ := json.Marshal(m) // a map's keys are written in order
	return string(b), false
}
