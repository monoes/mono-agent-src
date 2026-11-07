package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// A chat message that a stop could not write is owed. The first stop records that in the chat's own settings document
// (coder_sessions.settings, Settings.OwedStopNotice, written through the one writer Store.UpdateSettings) BEFORE it reports
// the failure, so the next stop can write it and clear the flag. The flag is a closed code and times, never text: the message
// is rebuilt from them when it is written. It names the run (charter, generation, the time its stop was recorded), so another
// run's flag is not honoured, and it is honoured only inside the repair window.
//
// Who can write it: the key is operator-only (coderhost.OperatorOnly): an agent's patch that names it is refused whole, and
// the operator's own stop is the only caller here. Were it forged anyway, it could not make a stop write anything but the
// canonical wording for a known code, and only into a chat the run itself owns.
//
// A chat whose boss was archived between the two stops is no longer one of the run's chats; its flag is not written, and it is
// cleared when the repair sees the run past its window.

// Owed-message codes: how the verdict of the stop that owes the message reads.
const (
	owedPlain      = "plain"       // "stopped"
	owedPartial    = "partial"     // "stopped (another run of this profile was NOT stopped)"
	owedBestEffort = "best_effort" // a best-effort stop: its exact limit text is not kept
	owedLater      = "later"       // a repair's own message: the sessions were ended by a later stop
)

const (
	verdictPartial    = "stopped (another run of this profile was NOT stopped)"
	verdictBestEffort = "stopped (best effort; the stop output of that time has the exact limit)"
)

// owedCode classifies a verdict line into the closed set.
func owedCode(verdict string) string {
	switch {
	case strings.HasPrefix(verdict, "stopped (another run of this profile was NOT stopped"):
		return owedPartial
	case strings.HasPrefix(verdict, "stopped (best effort"):
		return owedBestEffort
	}
	return owedPlain
}

type owedChat struct {
	Code string
	At   int64
}

// message is the text owed, rebuilt from the closed code and the time.
func (o owedChat) message() string {
	at := time.Unix(o.At, 0)
	switch o.Code {
	case owedPartial:
		return stopMessage(verdictPartial, at)
	case owedBestEffort:
		return stopMessage(verdictBestEffort, at)
	case owedLater:
		return laterMessage(at)
	}
	return stopMessage("stopped", at)
}

func laterMessage(at time.Time) string {
	return cleanLine(fmt.Sprintf("Open-ended run stopped earlier; its sessions were ended by a later stop at %s UTC", at.UTC().Format("2006-01-02 15:04:05")))
}

func (w Wiring) coder() *coderhost.Store { return coderhost.NewStore(w.DB) }

// updateFlag changes chat conv's settings through the existing-row writer: it never creates the row of a chat deleted in
// between (nothing is owed to a chat that is gone: ErrNoSession is not an error here), and leaves the chat's updated_at alone.
func (w Wiring) updateFlag(conv string, change coderhost.SettingsChange) error {
	if _, err := w.coder().UpdateExistingSettings(w.Profile, conv, change); err != nil && !errors.Is(err, coderhost.ErrNoSession) {
		return err
	}
	return nil
}

// ownsFlag says whether the flag in cur names exactly this run.
func ownsFlag(cur coderhost.Settings, run RunRef, intentAt int64) bool {
	o := cur.OwedStopNotice
	return o != nil && o.Charter == run.CharterID && o.Generation == run.Generation && o.IntentAt == intentAt
}

// writePending records that chat conv is owed the message of o, for run (whose stop was recorded at intentAt).
func (w Wiring) writePending(run RunRef, intentAt int64, conv string, o owedChat) error {
	return w.updateFlag(conv, func(_ context.Context, _ *sql.Conn, cur coderhost.Settings) (coderhost.Settings, error) {
		cur.OwedStopNotice = &coderhost.OwedStopNotice{Charter: run.CharterID, Generation: run.Generation, IntentAt: intentAt, Code: o.Code, At: o.At}
		return cur, nil
	})
}

// readPending is the message owed to chat conv by run, when its flag names exactly this run and a known code and a time inside
// the run's window.
func (w Wiring) readPending(run RunRef, intentAt int64, conv string) (owedChat, bool) {
	st, err := w.coder().GetSettings(w.Profile, conv)
	o := st.OwedStopNotice
	if err != nil || o == nil || o.Charter != run.CharterID || o.Generation != run.Generation || o.IntentAt != intentAt {
		return owedChat{}, false
	}
	if o.At < intentAt-60 || o.At > intentAt+int64(repairWindow/time.Second)+3600 {
		return owedChat{}, false
	}
	return owedChat{Code: o.Code, At: o.At}, true
}

// claimPending decides, inside one settings transaction, who writes the owed message of run to chat conv. If the flag no longer
// names this run (another repair took it, or a stop of another run replaced it) nothing is claimed. If the chat already holds a
// stop message since the stop was recorded (the first write may have committed and still reported an error), the flag is
// cleared and written reports it. Otherwise the flag is cleared and claimed is true: only that caller writes the message, so two
// concurrent repairs cannot both write it. A caller whose write then fails must put the flag back (writePending). A crash between
// the claim and the write loses the notice; the stop itself is already recorded.
func (w Wiring) claimPending(ctx context.Context, run RunRef, intentAt int64, conv string) (claimed, written bool, err error) {
	err = w.updateFlag(conv, func(ctx context.Context, tx *sql.Conn, cur coderhost.Settings) (coderhost.Settings, error) {
		claimed, written = false, false
		if !ownsFlag(cur, run, intentAt) {
			return cur, nil
		}
		if w.ChatNoteSince != nil {
			has, cerr := w.ChatNoteSince(ctx, tx, conv, intentAt)
			if cerr != nil {
				return cur, cerr
			}
			written = has
		}
		claimed = !written
		cur.OwedStopNotice = nil
		return cur, nil
	})
	if err != nil {
		return false, false, err
	}
	return claimed, written, nil
}

// clearPending removes chat conv's flag when it names this run; another run's flag stays.
func (w Wiring) clearPending(run RunRef, intentAt int64, conv string) error {
	return w.updateFlag(conv, func(_ context.Context, _ *sql.Conn, cur coderhost.Settings) (coderhost.Settings, error) {
		if ownsFlag(cur, run, intentAt) {
			cur.OwedStopNotice = nil
		}
		return cur, nil
	})
}
