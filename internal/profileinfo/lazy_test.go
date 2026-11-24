package profileinfo

import "testing"

// A field keep rejects must not be decrypted: a broken sealed card number
// cannot fail a read that only wants the plain fields.
func TestValuesDoesNotDecryptFieldsItDoesNotKeep(t *testing.T) {
	db, _ := newDB(t)
	if _, err := db.Exec(`INSERT INTO profile_info (profile_id, key, value, source, origin, updated_at) VALUES ('p1','card_number',?,'user','','2026-01-01')`, encPrefix+"garbage"); err != nil {
		t.Fatal(err)
	}
	if err := Set(bg, db, "p1", "first_name", "Ada", "user", ""); err != nil {
		t.Fatal(err)
	}
	got, err := Values(bg, db, "p1", func(f Field) bool { return !f.Encrypted() })
	if err != nil {
		t.Fatalf("a field that was not kept was decrypted: %v", err)
	}
	if got["first_name"] != "Ada" || len(got) != 1 {
		t.Errorf("values %v", got)
	}
}
