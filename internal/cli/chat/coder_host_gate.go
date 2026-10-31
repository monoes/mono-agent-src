package chat

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// gatedLauncher is the coder host's launcher with the peer-credential gate in front. While the setting
// security.peercred_binding is off it only passes every call on, untouched. While it is on and procbind.Refusal
// has a cause (this build does not issue bound tokens, the machine cannot run the service, or the service is
// not up), every turn start is refused with that cause: never a weaker mode. The setting is read at every start,
// so a hand-edited setting refuses too, and so does one switched on after the host started. A start that is already past
// this check when the setting is switched on goes on as it was (legacy) and the next start is refused; the window is the
// few milliseconds between the read and the launch, and switching the setting on is refused anyway while the build cannot
// issue bound tokens.
type gatedLauncher struct {
	inner coderhost.Launcher
	db    *sql.DB
	// service says whether the binding service is running (nil: it is not; 3b-2 supplies it).
	service func() bool
}

// bindingRefusal is a turn start refused by the gate.
type bindingRefusal struct{ msg string }

func (e *bindingRefusal) Error() string { return e.msg }

// refusal is nil when the turn may start.
func (g *gatedLauncher) refusal() error {
	var raw string
	err := g.db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, coderConfigName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no coder settings: the setting is off
	}
	if err != nil {
		return &bindingRefusal{"the turn was not started: the coder settings could not be read, so whether peer-credential binding is on is not known"}
	}
	// The same decoding as `coder status` and the setter (bindingFlag): conservative about junk and duplicate keys.
	var s struct {
		On bindingFlag `json:"peercredBinding"`
	}
	if json.Unmarshal([]byte(raw), &s) != nil {
		return &bindingRefusal{"the turn was not started: the coder settings are unreadable, so whether peer-credential binding is on is not known"}
	}
	if !s.On {
		return nil // off
	}
	if why := procbind.Refusal(g.service != nil && g.service()); why != "" {
		return &bindingRefusal{"the turn was not started: peer-credential binding is switched on, but " + why + ": switch it off (coder set --peercred-binding off)"}
	}
	return nil
}

func (g *gatedLauncher) Start(profileID, conversationID, turnID, message string, attachments []string, missionToken string) (coderhost.Turn, error) {
	if err := g.refusal(); err != nil {
		return nil, err
	}
	return g.inner.Start(profileID, conversationID, turnID, message, attachments, missionToken)
}

// StartCapped is Start under a spend cap; the inner launcher must offer it (the host asks for it by interface).
func (g *gatedLauncher) StartCapped(profileID, conversationID, turnID, message string, attachments []string, missionToken string, capUSD float64) (coderhost.Turn, error) {
	if err := g.refusal(); err != nil {
		return nil, err
	}
	b, ok := g.inner.(coderhost.BudgetLauncher)
	if !ok {
		return nil, errors.New("this launcher cannot run a turn under a spend cap")
	}
	return b.StartCapped(profileID, conversationID, turnID, message, attachments, missionToken, capUSD)
}

func (g *gatedLauncher) Adopt(pid int, stamp string) coderhost.Turn { return g.inner.Adopt(pid, stamp) }

func (g *gatedLauncher) Finish(profileID, conversationID, turnID, reason string) {
	g.inner.Finish(profileID, conversationID, turnID, reason)
}
