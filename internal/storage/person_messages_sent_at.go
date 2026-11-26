package storage

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// sentAtValue scans a person_messages.sent_at of any storage class the column
// has held: time (the driver's reading of a DATETIME), unix seconds (INTEGER or
// REAL), RFC3339 text, Go time text ("2006-01-02 15:04:05 +0000 UTC") and the
// empty string, which reads as NULL. A value it cannot read is also NULL, so
// one odd row never makes a person's messages unlistable.
type sentAtValue struct {
	Time  time.Time
	Valid bool
}

func (v *sentAtValue) Scan(src any) error {
	v.Time, v.Valid = time.Time{}, false
	switch s := src.(type) {
	case nil:
	case time.Time:
		v.Time, v.Valid = s, true
	case int64:
		v.Time, v.Valid = time.Unix(s, 0).UTC(), true
	case float64:
		sec := int64(s)
		v.Time, v.Valid = time.Unix(sec, int64((s-float64(sec))*1e9)).UTC(), true
	case []byte:
		v.parse(string(s))
	case string:
		v.parse(s)
	default:
		return fmt.Errorf("unsupported sent_at type %T", src)
	}
	return nil
}

func (v *sentAtValue) parse(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		v.Time, v.Valid = t.UTC(), true
		return
	}
	// Go time text: date, time, numeric offset, then a zone name and maybe a
	// monotonic reading; only the first three fields carry the instant.
	if f := strings.Fields(s); len(f) >= 3 {
		if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700", strings.Join(f[:3], " ")); err == nil {
			v.Time, v.Valid = t.UTC(), true
			return
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			v.Time, v.Valid = t.UTC(), true
			return
		}
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		_ = v.Scan(n)
	}
}

// sentAtInstant is an SQL expression for col as one comparable instant (a
// julian day), whatever form it is stored in, or NULL when it is empty or
// unreadable. It lets ORDER BY see RFC3339 and Go time text as the same scale.
// Text that does not start with a digit is unreadable too, which keeps 'now'
// out of julianday: the expression is indexed (migration 171, which must
// change with it), and SQLite refuses a non-deterministic one there.
func sentAtInstant(col string) string {
	c := "NULLIF(" + col + ", '')"
	return "(CASE typeof(" + col + ") WHEN 'integer' THEN " + col + "/86400.0+2440587.5" +
		" WHEN 'real' THEN " + col + "/86400.0+2440587.5" +
		" WHEN 'text' THEN julianday(CASE WHEN " + col + " NOT GLOB '[0-9]*' THEN NULL WHEN instr(substr(" + c + ",12),' ')>0 THEN substr(" + c + ",1,10+instr(substr(" + c + ",12),' ')) ELSE " + c + " END)" +
		" END)"
}
