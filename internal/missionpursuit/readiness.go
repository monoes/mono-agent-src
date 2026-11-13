package missionpursuit

import (
	"context"
	"errors"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Readiness is what `pursuit readiness` reads: a mission's goal (if it has one), its pursuit (if started), and the consent block
// computed from the live facts. The blockers are the ones ConsentOf derives, so the checklist, `pursuit preview` and
// `pursuit begin` can never disagree about what is missing.
type Readiness struct {
	Chat      string
	Goal      Goal
	GoalFound bool
	Pursuit   *Pursuit // nil: not started
	Limits    AwayLimits
	Consent   Consent
}

// Readiness reads the readiness of a mission session and writes nothing. Unlike Preview it answers for a mission that has no
// goal yet and for one whose pursuit has started.
func (s *Store) Readiness(ctx context.Context, profileID string, session int64, limits AwayLimits, live LiveFunc) (Readiness, error) {
	lim, err := limits.resolve()
	if err != nil {
		return Readiness{}, err
	}
	var out Readiness
	err = s.read(ctx, func(x tasks.Queryer) error {
		chat, err := s.checkMission(ctx, x, profileID, session)
		if err != nil {
			return err
		}
		out.Chat = chat
		switch p, err := readPursuit(ctx, x, session); {
		case err == nil:
			out.Pursuit = &p
			lim = AwayLimits{ParkedMax: p.ParkedMax, Pct: p.Pct, Wakes: p.WakesMax}
		case !errors.Is(err, ErrNotStarted):
			return err
		}
		out.Limits = lim
		switch g, err := readGoal(ctx, x, session); {
		case err == nil:
			out.Goal, out.GoalFound = g, true
		case !errors.Is(err, tasks.ErrNotFound):
			return err
		}
		out.Consent, err = s.consentIn(ctx, x, session, chat, out.Goal, lim, live)
		return err
	})
	return out, err
}
