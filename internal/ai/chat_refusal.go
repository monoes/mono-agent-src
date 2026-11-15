package ai

import (
	"errors"
	"strings"
	"unicode"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// MaxRefusalReasonRunes caps a refusal's reason, stored and shown.
const MaxRefusalReasonRunes = 500

// MaxRefusalNotices bounds the refusal notices one conversation keeps.
const MaxRefusalNotices = 20

// ErrTooManyRefusals is returned when the conversation already holds MaxRefusalNotices refusals.
var ErrTooManyRefusals = errors.New("chat: this conversation already holds the most refusal notices it keeps")

// ErrUnknownRefusalCode is returned for a code that is not a known failure code.
var ErrUnknownRefusalCode = errors.New("chat: unknown refusal code")

// refusalCodes are the failure codes a refused turn may carry. No notice code of another family (doomsday.*,
// coder.*) is accepted: those mean something to readers of the transcript.
var refusalCodes = map[string]bool{
	"agent_not_setup": true, "auth": true, "quota": true, "rate-limited": true, "missing-binary": true, "budget": true, "timeout": true,
}

// ValidRefusalCode says whether code is "" or a known failure code.
func ValidRefusalCode(code string) bool { return code == "" || refusalCodes[code] }

// CleanFailureText makes text safe to store and to print: control characters (ESC, CR, DEL, C0 and C1), line breaks
// and bidirectional overrides are dropped or become a space, and it is cut to max runes (an ellipsis marks the cut).
func CleanFailureText(text string, max int) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), r == 0x200E, r == 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x061C:
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
			n++
		}
		if n >= max {
			return strings.TrimSpace(b.String()) + "…"
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// RecordFailedTurnNotice records a turn that was refused or failed before it began, as a system turn that says why:
// the turn is failed with the reason, its notice is an error carrying the reason, and turn.finished carries the
// status, the reason and the code, so a reopened conversation shows what failed (the live stream's turn.finished for
// such a turn is not stored anywhere else). The reason is cleaned and capped here; the code must be a known failure
// code or empty.
func (s *AIStore) RecordFailedTurnNotice(profileID, conversationID, code, reason string) error {
	if !ValidRefusalCode(code) {
		return ErrUnknownRefusalCode
	}
	reason = CleanFailureText(reason, MaxRefusalReasonRunes)
	return s.recordSystemTurn(profileID, conversationID, code, reason, chatevents.SeverityError, chatevents.StatusFailed, reason)
}

// HasWorkTurn says whether the conversation has a turn that is not a system turn (a notice or a recorded refusal).
func (s *AIStore) HasWorkTurn(profileID, conversationID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ? AND NOT (prompt = '' AND id LIKE ?)`,
		profileID, conversationID, systemTurnPrefix+"%").Scan(&n)
	return n > 0, err
}
