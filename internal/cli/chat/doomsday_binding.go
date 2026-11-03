package chat

import "database/sql"

// PeercredBindingOn reports whether security.peercred_binding is on in the database, read with the loader every other reader of the
// coder settings uses. A settings object that cannot be read as a whole is an error: whether the setting is on is not known then.
// The open-ended mode asks it to require bound turns (a run does not start, and a turn is not released, without binding).
func PeercredBindingOn(db *sql.DB) (bool, error) {
	s, err := loadCoderSettings(db)
	if err != nil {
		return false, err
	}
	return bool(s.PeercredBinding), nil
}
