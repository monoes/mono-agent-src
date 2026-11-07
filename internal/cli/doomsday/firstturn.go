package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// FirstTurnState is where the boss's first turn stands: "started" (a turn exists), "pending" (queued, not yet taken), "failed"
// (the host refused or could not run it: Error says why) or "none" (nothing was ever queued for the boss).
type FirstTurnState struct {
	State string `json:"first_turn,omitempty"`
	Error string `json:"first_turn_error,omitempty"`
}

// FirstTurnOf reads the first turn of the profile's open run's boss. The start queues the turn and returns; the host decides
// later (its dispatch gate can refuse, e.g. "no run without binding" after binding was switched off), so this is the one way to
// see a first turn that failed after a successful start. A profile with no open run, or a run without a recorded boss, answers
// the zero state. Read-only.
func (w Wiring) FirstTurnOf(ctx context.Context) (FirstTurnState, error) {
	if w.DB == nil {
		return FirstTurnState{}, errors.New("no database to read the boss's first turn from")
	}
	var conv string
	err := w.DB.QueryRowContext(ctx, `SELECT s.conversation_id FROM task_sessions s
		JOIN doomsday_runs r ON r.profile = s.profile_id AND r.charter_id = s.doomsday_charter_id AND r.generation = s.doomsday_generation AND r.state <> 'quiescent'
		WHERE s.profile_id = ? AND COALESCE(s.parent_session_id, 0) = 0 AND s.archived_at = '' AND s.conversation_id IS NOT NULL AND `+coderhost.LeadSessionSQL+`
		ORDER BY s.id LIMIT 1`, w.Profile).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		return FirstTurnState{}, nil
	}
	if err != nil {
		return FirstTurnState{}, err
	}
	var turns int
	if err := w.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ?`, w.Profile, conv).Scan(&turns); err != nil {
		return FirstTurnState{}, err
	}
	if turns > 0 {
		return FirstTurnState{State: "started"}, nil
	}
	var status, errText string
	err = w.DB.QueryRowContext(ctx, `SELECT status, COALESCE(error, '') FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ?
		AND kind = 'send' AND issuer = '' AND substr(turn_id, 1, 2) = 't-' ORDER BY id DESC LIMIT 1`, w.Profile, conv).Scan(&status, &errText)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return FirstTurnState{State: "none"}, nil
	case err != nil:
		return FirstTurnState{}, err
	case status == coderhost.CmdFailed:
		return FirstTurnState{State: "failed", Error: cleanReason(errText)}, nil
	}
	return FirstTurnState{State: "pending"}, nil
}

const maxReasonRunes = 300

// ansiSeq is a terminal escape sequence (CSI or OSC); dropping only the ESC byte would leave its text behind.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)

// cleanReason is the host's error text made safe to show: control characters (escape sequences included) become spaces and it
// is cut to maxReasonRunes. The text comes from launcher and database errors, which can carry paths and raw bytes.
func cleanReason(s string) string {
	s = ansiSeq.ReplaceAllString(s, " ")
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsControl(r) || r == '\uFFFD' {
			r = ' '
		}
		out = append(out, r)
		if len(out) >= maxReasonRunes {
			out = append(out, '\u2026')
			break
		}
	}
	return strings.TrimSpace(string(out))
}
