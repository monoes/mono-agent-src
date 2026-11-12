package tasks

import "context"

// ocResult is what OverCommitOf would have answered for one session: the value, or the error that walk would return.
type ocResult struct {
	oc  OverCommit
	err error
}

// OverCommitAll answers OverCommitOf for every listed session of the profile in one read snapshot. A session whose read
// fails shows as not over-committed, as a failed OverCommitOf does in `task session list`. Read only.
func (s *Store) OverCommitAll(ctx context.Context, profileID string, ids []int64) (map[int64]OverCommit, error) {
	out := make(map[int64]OverCommit, len(ids))
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		res, err := s.overCommitAll(ctx, x, profileID, ids)
		if err != nil {
			return err
		}
		for id, r := range res {
			if r.err != nil {
				r.oc = OverCommit{Session: id}
			}
			out[id] = r.oc
		}
		return nil
	})
	return out, err
}

// overCommitAll: first cut, one walk per session (the shape of the list before the fix).
func (s *Store) overCommitAll(ctx context.Context, x dbx, profileID string, ids []int64) (map[int64]ocResult, error) {
	out := make(map[int64]ocResult, len(ids))
	for _, id := range ids {
		oc, err := s.overCommitWalk(ctx, x, profileID, id)
		out[id] = ocResult{oc, err}
	}
	return out, nil
}
