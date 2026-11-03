package dynorg

import (
	"context"
	"os/exec"
)

// GitPlacer makes a git command start inside the open run's containment unit (stop.Placer.Place in production).
// release is called after the command ended. An error means the command must not be started at all.
type GitPlacer func(cmd *exec.Cmd) (release func(), err error)

type gitPlacerKey struct{}

// withGitPlacer carries the placement of the run a merge or a recovery belongs to down to gitRun, the one place
// the conductor starts git. A nil placer returns ctx unchanged: with no run open the exec is exactly as before.
func withGitPlacer(ctx context.Context, p GitPlacer) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, gitPlacerKey{}, p)
}

// placeGit applies the placement carried by ctx, if any. With none it does nothing and returns a nil release.
func placeGit(ctx context.Context, cmd *exec.Cmd) (func(), error) {
	p, _ := ctx.Value(gitPlacerKey{}).(GitPlacer)
	if p == nil {
		return nil, nil
	}
	return p(cmd)
}

// placed is the context a begun merge runs its git commands in: they start inside the run's unit. Nil-safe (the
// mode is off).
func (m *mergeOp) placed(ctx context.Context) context.Context {
	if m == nil {
		return ctx
	}
	if m.dd.PlaceTicket == nil {
		return withGitPlacer(ctx, m.dd.Place)
	}
	// the step that runs the command is the current ticket at the moment the command is placed
	return withGitPlacer(ctx, func(cmd *exec.Cmd) (func(), error) {
		if m.cur == nil {
			return m.dd.Place(cmd)
		}
		return m.dd.PlaceTicket(m.cur.ID(), cmd)
	})
}

// placed is the context a recovery pass runs its git commands in.
func (r MergeRecovery) placed(ctx context.Context) (context.Context, error) {
	if r.PlaceFor == nil {
		return ctx, nil
	}
	p, err := r.PlaceFor(ctx)
	if err != nil {
		return ctx, err
	}
	return withGitPlacer(ctx, p), nil
}
