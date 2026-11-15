package ai

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// MaxRefusalReasonRunes caps a refusal's reason, stored and shown.
const MaxRefusalReasonRunes = 500

// MaxRefusalNotices bounds the refusal notices one conversation keeps (the newest).
const MaxRefusalNotices = 20

// pruneOldestRefusals makes room for one more refusal: when the conversation holds MaxRefusalNotices or more, the
// oldest recorded refusals and their events are removed, so the newest reasons are the ones kept.
func pruneOldestRefusals(tx *sql.Tx, profileID, conversationID string) error {
	const old = `SELECT id FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ? AND id LIKE ? AND status = 'failed' AND prompt = ''
		ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?`
	args := []any{profileID, conversationID, systemTurnPrefix + "%", MaxRefusalNotices - 1}
	if _, err := tx.Exec("DELETE FROM ai_chat_events WHERE profile_id = ? AND turn_id IN ("+old+")", append([]any{profileID}, args...)...); err != nil {
		return fmt.Errorf("prune refusal events: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM ai_chat_turns WHERE id IN ("+old+")", args...); err != nil {
		return fmt.Errorf("prune refusals: %w", err)
	}
	return nil
}

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
