package chat

import "database/sql"

// PeercredBindingOn reports whether peer-credential binding is on (stored on, or unset on a machine that can run it), read with the loader every other reader of the
// coder settings uses. A settings object that cannot be read as a whole is an error: whether the setting is on is not known then.
// The open-ended mode asks it to require bound turns (a run does not start, and a turn is not released, without binding).
func PeercredBindingOn(db *sql.DB) (bool, error) {
	s, err := loadCoderSettings(db)
	if err != nil {
		return false, err
	}
	return s.resolveBinding().On, nil
}

// PeercredBindingDefault is the binding answer for a profile with nothing stored: the default on this machine, with why it is off
// when it is (the note is empty when it is on).
func PeercredBindingDefault() (bool, string) {
	r := coderSettings{}.resolveBinding()
	return r.On, r.Note
}

// PeercredBindingNote is PeercredBindingOn plus the reason a default that is off on this machine is off ("" when on or when the
// operator stored the off).
func PeercredBindingNote(db *sql.DB) (bool, string, error) {
	s, err := loadCoderSettings(db)
	if err != nil {
		return false, "", err
	}
	r := s.resolveBinding()
	return r.On, r.Note, nil
}
