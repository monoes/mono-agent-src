package task

import (
	"context"
	"errors"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// ownChat is the chat a calling agent runs in, read only from a verified coder-session
// token (the host mints it for a turn it started; see internal/coderhost). A bare
// environment label is never trusted: "" means the caller has no verified chat, and the
// store then refuses any conversation link. A token that is present but fails is refused
// here, with the reason, rather than read as "no chat".
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
	// The token names a profile by id; --profile may give a name.
	profile := d.ProfileID()
	if id, rerr := profiledir.Resolve(context.Background(), db.DB, profile); rerr == nil {
		profile = id
	}
	c, err := coderhost.NewStore(db.DB).CallerFor(tok, profile)
	switch {
	case err == nil:
		return c.ConversationID, nil
	case errors.Is(err, coderhost.ErrVaultKey):
		return "", d.InvalidInput("the session token could not be checked: the vault key is unavailable")
	case errors.Is(err, coderhost.ErrNotInTurnTree):
		return "", d.InvalidInput("the session token was presented from outside the session's turn (a detached process)")
	default:
		return "", d.InvalidInput("the session token is not valid for this profile (unknown, expired, or another profile's)")
	}
}
