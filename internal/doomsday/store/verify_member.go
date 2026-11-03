package store

import "context"

// KindVerify is the member kind of a finish round's verification unit (note 13.6 G3, note 13.3 P1).
const KindVerify = "verify"

// RegisterVerify registers the verification unit of a finish round as a live member of the run.
//
// Register admits work only while the run is running, and a finish happens after the run reached quiescent (the finish is a
// Stop with cause=finish, followed by the rounds), so the verification unit cannot be a registry member through it. This is
// its own conditional insert: the run must be quiescent with the finish cause, and the member's kind is fixed to `verify`.
// A live member blocks a new run of the profile (Enter) until it is released on proof that its unit is gone, so a host that
// dies with a verification unit running leaves it counted.
func (s *SQLStore) RegisterVerify(ctx context.Context, m Member) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO doomsday_members(profile, generation, member_id, kind, pid, stamp, live)
SELECT ?,?,?,?,?,?,1 WHERE EXISTS (SELECT 1 FROM doomsday_runs WHERE profile = ? AND generation = ? AND state = 'quiescent' AND cause = ?)`,
		m.Profile, m.Generation, m.ID, KindVerify, m.Pid, m.Stamp, m.Profile, m.Generation, FinishCause)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return &Refusal{Reason: "the run is not a finish at quiescent"}
	}
	return nil
}
