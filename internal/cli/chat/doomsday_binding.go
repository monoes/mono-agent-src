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

// BindingState is what an open-ended run asks of the binding setting: whether turns are bound, and, when they are not, whether
// the run may start anyway. UnboundReason is non-empty only when binding is off AND this machine cannot run the binding service
// (then the run starts unbound with a warning; an explicit off on a machine that could run it still refuses a run). Note is why a
// default that is on elsewhere is off here.
type BindingState struct {
	On            bool
	UnboundReason string
	Note          string
}

// PeercredBindingState reads the stored setting and resolves it for this machine.
func PeercredBindingState(db *sql.DB) (BindingState, error) {
	s, err := loadCoderSettings(db)
	if err != nil {
		return BindingState{}, err
	}
	r := s.resolveBinding()
	st := BindingState{On: r.On, Note: r.Note}
	if !r.On {
		st.UnboundReason = bindingReadiness()
	}
	return st, nil
}

// UnboundWarning is the one line shown wherever a run goes without binding.
func UnboundWarning(reason string) string {
	if reason == "" {
		return ""
	}
	return "running without peer-credential binding: " + reason
}
