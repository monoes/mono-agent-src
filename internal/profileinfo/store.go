package profileinfo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/secrets"
)

// Where a value came from.
const (
	SourceUser = "user"
	SourceAI   = "ai"
)

const (
	maxLine     = 300
	maxText     = 4000
	maxPhotoMB  = 10
	photoFolder = "info"
)

// Errors a caller can tell apart.
var (
	ErrUnknownField = errors.New("unknown field")
	ErrUserOwned    = errors.New("the person typed this value; the AI does not replace it")
)

// Entry is one stored value. Masked values (Sensitive and Card fields, unless
// revealed) show only their last four characters.
type Entry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Masked    bool   `json:"masked,omitempty"`
	Source    string `json:"source"`
	Origin    string `json:"origin,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// Now supplies the time; tests replace it.
var Now = time.Now

func mask(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return "••••"
	}
	return "•••• " + string(r[len(r)-4:])
}

// Get returns the profile's values in catalog order. Encrypted fields are
// masked unless reveal is set.
func Get(ctx context.Context, db *sql.DB, profile string, reveal bool) ([]Entry, error) {
	return GetWhere(ctx, db, profile, reveal, nil)
}

// GetWhere is Get for the fields keep accepts (nil keeps all); a field keep
// rejects is neither decrypted nor sealed.
func GetWhere(ctx context.Context, db *sql.DB, profile string, reveal bool, keep func(Field) bool) ([]Entry, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value, source, origin, updated_at FROM profile_info WHERE profile_id = ?`, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Key, &e.Value, &e.Source, &e.Origin, &e.UpdatedAt); err != nil {
			return nil, err
		}
		have[e.Key] = e
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(have))
	for _, fd := range Catalog {
		e, ok := have[fd.Key]
		if !ok || (keep != nil && !keep(fd)) {
			continue
		}
		if fd.Encrypted() {
			var plain []byte
			if strings.HasPrefix(e.Value, encPrefix) {
				plain, err = secrets.DecryptBlob(ctx, db, profile, e.Value)
				if err != nil {
					return nil, fmt.Errorf("decrypting %s: %w", fd.Key, err)
				}
			} else {
				// A value an earlier build stored in plain text: seal it, but a
				// failure (read-only handle, locked keyring) must not fail the read.
				if plain, err = sealStored(ctx, db, profile, fd.Key, e.Value); err != nil {
					fmt.Fprintf(os.Stderr, "warning: profileinfo: could not encrypt %s in place: %v\n", fd.Key, err)
					plain = []byte(e.Value)
				}
			}
			e.Value = string(plain)
			if !reveal {
				e.Value, e.Masked = mask(e.Value), true
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// TypeOf is what the profile is: ScopeBusiness when its profile type is
// Business, else ScopePerson.
func TypeOf(ctx context.Context, db *sql.DB, profile string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM profile_info WHERE profile_id = ? AND key = ?`, profile, TypeKey).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if strings.EqualFold(v, "Business") {
		return ScopeBusiness, nil
	}
	return ScopePerson, nil
}

// encPrefix starts every value secrets.EncryptBlob stores.
const encPrefix = "vaultenc:v1:"

// sealStored encrypts, in place, a value of an encrypted field that an earlier
// build stored as plain text (it only started encrypting some fields later),
// and returns the plain value. The row's source, origin and time are kept.
func sealStored(ctx context.Context, db *sql.DB, profile, key, plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	sealed, err := secrets.EncryptBlob(ctx, db, profile, []byte(plain))
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE profile_info SET value = ? WHERE profile_id = ? AND key = ? AND value = ?`, sealed, profile, key, plain); err != nil {
		return nil, err
	}
	return []byte(plain), nil
}

// Values returns the plain values of the fields keep accepts, for the AI step
// and the extension; an encrypted field is decrypted only if keep takes it.
func Values(ctx context.Context, db *sql.DB, profile string, keep func(Field) bool) (map[string]string, error) {
	all, err := GetWhere(ctx, db, profile, true, keep)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range all {
		out[e.Key] = e.Value
	}
	return out, nil
}

// clean trims a value and drops control characters other than newline.
func clean(s string, multiline bool) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	return strings.Map(func(r rune) rune {
		if r == '\n' && multiline {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

var (
	emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	phoneRe = regexp.MustCompile(`^[+()\d\s.\-/]+$`)
)

func digitsOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// check validates value for fd and returns it in the form to store.
func check(fd *Field, value string) (string, error) {
	v := clean(value, fd.Kind == Textarea)
	limit := maxLine
	if fd.Kind == Textarea {
		limit = maxText
	}
	if utf8.RuneCountInString(v) > limit {
		return "", fmt.Errorf("%s is longer than %d characters", fd.Label, limit)
	}
	if v == "" {
		return "", nil
	}
	switch fd.Kind {
	case Select:
		for _, o := range fd.Options {
			if strings.EqualFold(o, v) {
				return o, nil
			}
		}
		return "", fmt.Errorf("%s must be one of: %s", fd.Label, strings.Join(fd.Options, ", "))
	case Date:
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return "", fmt.Errorf("%s must be a date like 2026-10-15", fd.Label)
		}
	case Email:
		if !emailRe.MatchString(v) {
			return "", fmt.Errorf("%s must be an email address like name@example.com", fd.Label)
		}
	case Phone:
		if !phoneRe.MatchString(v) || len(digitsOf(v)) < 5 {
			return "", fmt.Errorf("%s must be a phone number like +49 170 1234567", fd.Label)
		}
	}
	return v, nil
}

// Set stores value for key. An empty value removes it. A value from the AI
// never replaces one the person typed (ErrUserOwned).
func Set(ctx context.Context, db *sql.DB, profile, key, value, source, origin string) error {
	fd := Lookup(key)
	if fd == nil {
		return fmt.Errorf("%w %q", ErrUnknownField, key)
	}
	if fd.Kind == Photo {
		return fmt.Errorf("%s is a photo: use SetPhoto", fd.Label)
	}
	if source != SourceUser && source != SourceAI {
		return fmt.Errorf("source must be %q or %q", SourceUser, SourceAI)
	}
	if source == SourceAI && fd.Card {
		return errors.New("the AI does not fill card details")
	}
	v, err := check(fd, value)
	if err != nil {
		return err
	}
	if v == "" {
		return Remove(ctx, db, profile, key)
	}
	if source == SourceAI {
		var existing string
		err := db.QueryRowContext(ctx, `SELECT source FROM profile_info WHERE profile_id = ? AND key = ?`, profile, key).Scan(&existing)
		if err == nil && existing == SourceUser {
			return ErrUserOwned
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	stored := v
	if fd.Encrypted() {
		if stored, err = secrets.EncryptBlob(ctx, db, profile, []byte(v)); err != nil {
			return err
		}
	}
	return put(ctx, db, profile, key, stored, source, origin)
}

func put(ctx context.Context, db *sql.DB, profile, key, stored, source, origin string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO profile_info (profile_id, key, value, source, origin, updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(profile_id, key) DO UPDATE SET value = excluded.value, source = excluded.source, origin = excluded.origin, updated_at = excluded.updated_at`,
		profile, key, stored, source, origin, Now().UTC().Format(time.RFC3339))
	return err
}

// Remove clears a value (and a photo's file). Clearing nothing is not an error.
func Remove(ctx context.Context, db *sql.DB, profile, key string) error {
	fd := Lookup(key)
	if fd == nil {
		return fmt.Errorf("%w %q", ErrUnknownField, key)
	}
	var rel string
	if fd.Kind == Photo {
		_ = db.QueryRowContext(ctx, `SELECT value FROM profile_info WHERE profile_id = ? AND key = ?`, profile, key).Scan(&rel)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM profile_info WHERE profile_id = ? AND key = ?`, profile, key); err != nil {
		return err
	}
	if photoRel(rel) {
		_ = os.Remove(filepath.Join(profiledir.Root(db, profile), filepath.FromSlash(rel)))
	}
	return nil
}

// photoRel reports a stored photo path that stays inside the profile's info folder; a value from
// a hand-edited or imported database that climbs out of it is never read or removed.
func photoRel(rel string) bool {
	return strings.HasPrefix(rel, photoFolder+"/") && filepath.IsLocal(filepath.FromSlash(rel))
}

var photoExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".pdf": true}

// SetPhoto copies the file at src into the profile's folder as the photo for key.
func SetPhoto(ctx context.Context, db *sql.DB, profile, key, src string) error {
	fd := Lookup(key)
	if fd == nil {
		return fmt.Errorf("%w %q", ErrUnknownField, key)
	}
	if fd.Kind != Photo {
		return fmt.Errorf("%s is not a photo field", fd.Label)
	}
	ext := strings.ToLower(filepath.Ext(src))
	if !photoExts[ext] {
		return errors.New("a photo must be a png, jpg, webp, gif or pdf file")
	}
	// Lstat first: a link to another file, or a FIFO or device that would block the open, is refused.
	l, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if l.Mode()&os.ModeSymlink != 0 {
		return errors.New("the photo must be the file itself, not a link to one")
	}
	if !l.Mode().IsRegular() {
		return errors.New("the photo must be a regular file")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat() // the opened file, not whatever the name points at now
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("the photo must be a regular file")
	}
	if st.Size() > maxPhotoMB<<20 {
		return fmt.Errorf("the photo is larger than %d MB", maxPhotoMB)
	}
	root := profiledir.Root(db, profile)
	if root == "" {
		return errors.New("this profile has no folder")
	}
	dir := filepath.Join(root, photoFolder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	rel := photoFolder + "/" + key + ext
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(in, maxPhotoMB<<20+1)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		return err
	}
	// A photo replaced by one of another type leaves the old file behind.
	var old string
	_ = db.QueryRowContext(ctx, `SELECT value FROM profile_info WHERE profile_id = ? AND key = ?`, profile, key).Scan(&old)
	if err := put(ctx, db, profile, key, rel, SourceUser, ""); err != nil {
		return err
	}
	if photoRel(old) && old != rel {
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(old)))
	}
	return nil
}

// PhotoPath is the absolute path of key's photo, or "" if there is none.
func PhotoPath(ctx context.Context, db *sql.DB, profile, key string) string {
	if fd := Lookup(key); fd == nil || fd.Kind != Photo {
		return ""
	}
	var rel string
	if err := db.QueryRowContext(ctx, `SELECT value FROM profile_info WHERE profile_id = ? AND key = ?`, profile, key).Scan(&rel); err != nil || !photoRel(rel) {
		return ""
	}
	return filepath.Join(profiledir.Root(db, profile), filepath.FromSlash(rel))
}
