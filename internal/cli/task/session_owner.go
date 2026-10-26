package task

import (
	"github.com/monoes/mono-agent/internal/coderhost"
)

// ownChat is the chat a calling agent runs in, read only from a verified coder-session
// token (the host mints it for a turn it started; see internal/coderhost). A bare
// environment label is never trusted: "" means the caller has no verified chat, and the
// store then refuses any conversation link.
func (d Deps) ownChat() (string, error) {
	tok := d.Getenv(coderhost.SessionTokenEnv)
	if tok == "" {
		return "", nil
	}
	db, err := d.OpenDB()
	if err != nil {
		return "", err
	}
	defer db.Close()
	c, err := coderhost.NewStore(db.DB).CallerFor(tok, d.ProfileID())
	if err != nil {
		return "", nil
	}
	return c.ConversationID, nil
}
