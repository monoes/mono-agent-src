package capturecli

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
)

// Profiles, for the capture commands and for the extension's picker.
//
// A capture can name the profile it belongs to, which decides the inbox it
// lands in (internal/capture/profile.go) and the knowledge store that
// ingests it. Two things need the profile list to make that work: the
// extension popup, which offers the choice, and `capture list`, which has
// to be able to look inside a profile's inbox rather than only the default
// one.

// defaultDBPath mirrors the --db-path flag default in root.go. The
// extension's profile source is installed where the bridge is built, which
// has no globalConfig in scope; a user who moved the database with
// --db-path gets no picker rather than the wrong picker.
const defaultDBPath = "~/.monoagent/monoagent.db"

// openProfileDB opens the monoagent database read-only-ish for a profile
// lookup. A database that is not there yet is an ordinary state (nothing
// has been created), not an error worth a stack trace — and it must not be
// CREATED by a question asked from a browser, which is why the file is
// checked before the driver is given a chance to make one.
func openProfileDB(path string) (*storage.Database, error) {
	expanded := expandPath(path)
	if expanded == "" {
		expanded = expandPath(defaultDBPath)
	}
	if _, err := os.Stat(expanded); err != nil {
		return nil, fmt.Errorf("no monoagent database at %s", expanded)
	}
	db, err := storage.NewDatabase(expanded)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", expanded, err)
	}
	return db, nil
}

// extensionProfileSource answers the extension's profile.list requests. It
// opens the database per request and closes it again: the popup asks once
// when it opens, and holding a connection open for the life of the daemon
// to serve that would be the wrong trade.
func extensionProfileSource(path string) extension.ProfileSource {
	return func(ctx context.Context) ([]profiledir.Profile, error) {
		db, err := openProfileDB(path)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		return profiledir.List(ctx, db.DB)
	}
}

// captureProfile is a profile a capture command was pointed at.
type captureProfile struct {
	profiledir.Profile
	// Inbox is where this profile's captures land.
	Inbox string
}

// resolveCaptureProfile turns what the user typed after --profile into a
// profile and its inbox. An id or a name both work, matching
// `profile switch`; an id that matches nothing in the database is rejected
// rather than guessed at, because listing the wrong (empty) directory looks
// exactly like "you have saved nothing", which is the most confusing
// possible answer.
func (d Deps) resolveCaptureProfile(db *sql.DB, idOrName string) (captureProfile, error) {
	want := strings.TrimSpace(idOrName)
	if want == "" {
		return captureProfile{}, d.InvalidInput("--profile was given but named no profile")
	}
	profiles, err := profiledir.List(context.Background(), db)
	if err != nil {
		return captureProfile{}, fmt.Errorf("listing profiles: %w", err)
	}
	for _, p := range profiles {
		if p.ID == want || strings.EqualFold(p.Name, want) {
			inbox, err := capture.ProfileInbox(p.ID)
			if err != nil {
				return captureProfile{}, err
			}
			return captureProfile{Profile: p, Inbox: inbox}, nil
		}
	}
	return captureProfile{}, d.InvalidInput("no profile matches %q (checked both id and name); try `monoagentcli profile list`", want)
}

// captureProfileInboxes returns every profile's inbox, for the listing that
// shows the whole library at once.
func captureProfileInboxes(db *sql.DB) ([]captureProfile, error) {
	profiles, err := profiledir.List(context.Background(), db)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	out := make([]captureProfile, 0, len(profiles))
	for _, p := range profiles {
		inbox, err := capture.ProfileInbox(p.ID)
		if err != nil {
			continue // profiledir.List already skips these; belt and braces
		}
		out = append(out, captureProfile{Profile: p, Inbox: inbox})
	}
	return out, nil
}
