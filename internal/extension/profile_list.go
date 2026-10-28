package extension

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// profile.list — "which profiles can I save into?"
//
// The extension's popup offers a profile picker so a page can be filed into
// one brain rather than another, and it has to fill that picker with the
// user's REAL profiles, not a list typed into the extension. This is the
// question it asks, over the same request channel as doc.lookup.
//
// The answer comes from whatever the host process hands over via
// SetProfileSource — this package never opens a database. Two reasons: the
// transport has no business owning one, and the method should only be
// advertised (ping's `methods`) when something can actually answer it. An
// extension talking to a process with no source simply sees no profile.list
// and keeps saving the way it always has.
//
//	extension → Go  {"kind":"request","id":"req-…","method":"profile.list"}
//	Go → extension  {"kind":"reply","id":"req-…","ok":true,
//	                 "data":{"profiles":[{"id":"…","name":"Work","default":true}]}}

// MethodProfileList lists the profiles a capture can be filed into.
const MethodProfileList = "profile.list"

// ProfileSource answers "which profiles exist". The host process supplies
// it (see cmd/monoagentcli/capture_profiles.go); an error means "cannot
// answer right now", which the extension renders as absence rather than
// failure.
type ProfileSource func(ctx context.Context) ([]profiledir.Profile, error)

// ProfileList is the reply's shape.
type ProfileList struct {
	Profiles []profiledir.Profile `json:"profiles"`
	// Default is the id the picker should pre-select when it has no stored
	// choice — empty when there are no profiles at all.
	Default string `json:"default,omitempty"`
}

// SetProfileSource installs (or replaces) the source behind profile.list,
// registering the handler with it. Passing nil takes the method away again.
//
// Registration hangs off the source on purpose: HandleRequest both adds and
// replaces, and RequestMethods is what the extension probes, so "there is a
// source" and "the method is advertised" are the same fact rather than two
// that can drift.
func (s *Server) SetProfileSource(src ProfileSource) {
	s.pendMu.Lock()
	s.profileSource = src
	s.pendMu.Unlock()
	if src == nil {
		s.handlerMu.Lock()
		delete(s.handlers, MethodProfileList)
		s.handlerMu.Unlock()
		return
	}
	s.HandleRequest(MethodProfileList, func(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
		return listProfiles(ctx, src, req.Origin.Profile)
	})
}

// listProfiles is the handler's body, kept separate so a test can call it
// without a server. bound is the profile the asking browser is bound to.
// When that profile exists it becomes the default, so the capture picker
// and the automations agree on which brain this browser belongs to.
func listProfiles(ctx context.Context, src ProfileSource, bound string) (*ProfileList, error) {
	profiles, err := src(ctx)
	if err != nil {
		// Not an error the user did anything about: no database yet, a
		// locked one, a CLI built without profiles. The picker falls back
		// to saving the way it did before.
		return nil, Unavailable("%s", fmt.Sprintf("cannot list profiles: %v", err))
	}
	out := &ProfileList{Profiles: profiles}
	if out.Profiles == nil {
		// A picker renders an empty list; it cannot render a null.
		out.Profiles = []profiledir.Profile{}
	}
	if bound != "" && slices.ContainsFunc(out.Profiles, func(p profiledir.Profile) bool { return p.ID == bound }) {
		for i := range out.Profiles {
			out.Profiles[i].Default = out.Profiles[i].ID == bound
		}
	}
	for _, p := range out.Profiles {
		if p.Default {
			out.Default = p.ID
			break
		}
	}
	return out, nil
}

// profileCheckTimeout bounds the profile listing a capture waits on before
// it is written.
const profileCheckTimeout = 5 * time.Second

// fileUnderLiveProfile keeps a capture out of a deleted profile: one queued
// in the extension before its profile was deleted still names it, and
// writing it there would recreate the folder of a profile that is gone. It
// lands in the default inbox instead, with a warning, as one with no profile
// does. With no source, or one that cannot answer right now, the profile is
// kept: a locked database is no reason to move a capture out of what may
// well be a live profile.
func (s *Server) fileUnderLiveProfile(env *capture.Envelope) {
	id := strings.TrimSpace(env.Meta.Profile)
	if id == "" || !profiledir.ValidProfileID(id) {
		return // the writer settles these (resolveInbox)
	}
	s.pendMu.Lock()
	src := s.profileSource
	s.pendMu.Unlock()
	if src == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), profileCheckTimeout)
	defer cancel()
	profiles, err := src(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Str("profile", id).Msg("cannot check the capture's profile: filing it under it anyway")
		return
	}
	if slices.ContainsFunc(profiles, func(p profiledir.Profile) bool { return p.ID == id }) {
		return
	}
	s.logger.Warn().Str("profile", id).Msg("capture names a profile that no longer exists")
	env.Meta.Profile = ""
	env.Warnings = append(env.Warnings, fmt.Sprintf("profile %.64q no longer exists: saved to the default inbox instead", id))
}

// dropDeletedBinding removes the profiles that no longer exist from a
// browser's set: the user deleted them, and the browser must not stay
// matched to them. A browser left with an empty set is a default browser.
// With no source, or one that cannot answer right now, the set is kept, as
// in fileUnderLiveProfile.
func (s *Server) dropDeletedBinding(c *extConn) {
	set := c.boundProfiles()
	if len(set) == 0 {
		return
	}
	s.pendMu.Lock()
	src := s.profileSource
	s.pendMu.Unlock()
	if src == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), profileCheckTimeout)
	defer cancel()
	profiles, err := src(ctx)
	if err != nil {
		return
	}
	var gone []string
	for _, id := range set {
		if !slices.ContainsFunc(profiles, func(p profiledir.Profile) bool { return p.ID == id }) {
			gone = append(gone, id)
			s.logger.Warn().Str("instance", c.id).Str("profile", id).Msg("browser is bound to a profile that no longer exists: dropping it from the browser's profiles")
		}
	}
	if len(gone) == 0 {
		return
	}
	// The set may have changed while the source answered: take the deleted
	// ids out of what is there now, and touch nothing else.
	c.mu.Lock()
	c.profiles = slices.DeleteFunc(slices.Clone(c.profiles), func(id string) bool { return slices.Contains(gone, id) })
	c.mu.Unlock()
}
