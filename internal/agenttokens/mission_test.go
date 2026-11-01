package agenttokens

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var missionB = MissionBinding{Session: 4, Task: 9, Role: RoleVerifier, Actor: "v4-t9", Owner: "run-a"}

func TestMissionTokenCarriesItsBindingAndStoresOnlyTheHash(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tok, secret, err := s.CreateMission(ctx, "default", missionB, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Mission == nil || *tok.Mission != missionB || tok.ExpiresAt == nil || tok.Bound() {
		t.Fatalf("token: %+v", tok)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM agent_tokens WHERE id = ?`, tok.ID).Scan(&stored); err != nil || stored != Hash(secret) || stored == secret {
		t.Fatalf("stored %q", stored)
	}
	got, err := s.VerifyMission(ctx, "default", secret)
	if err != nil || got.Mission == nil || *got.Mission != missionB {
		t.Fatalf("verify: %+v %v", got, err)
	}
}

func TestMissionTokenIsNotAnAPIToken(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, secret, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the HTTP API's check accepted a mission token: %v", err)
	}
	_, plain, err := s.Create(ctx, "default", "plain", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", plain); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a plain token passed as a mission token: %v", err)
	}
}

func TestMissionTokenRefusals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	for name, b := range map[string]MissionBinding{
		"no session": {Task: 1, Role: RoleWorker, Actor: "a", Owner: "o"},
		"no task":    {Session: 1, Role: RoleWorker, Actor: "a", Owner: "o"},
		"no role":    {Session: 1, Task: 1, Actor: "a", Owner: "o"},
		"odd role":   {Session: 1, Task: 1, Role: "admin", Actor: "a", Owner: "o"},
		"no actor":   {Session: 1, Task: 1, Role: RoleWorker, Actor: " ", Owner: "o"},
		"no owner":   {Session: 1, Task: 1, Role: RoleWorker, Actor: "a"},
	} {
		if _, _, err := s.CreateMission(ctx, "default", b, time.Hour); !errors.Is(err, ErrMission) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := s.CreateMission(ctx, "default", missionB, 0); !errors.Is(err, ErrMission) {
		t.Errorf("a mission token that never expires: %v", err)
	}

	// expired, revoked, another profile, forged: one error each
	past := NewStore(s.db)
	past.SetClock(func() time.Time { return now.Add(-2 * time.Hour) })
	_, expired, _ := past.CreateMission(ctx, "default", missionB, time.Hour)
	if _, err := s.VerifyMission(ctx, "default", expired); !errors.Is(err, ErrInvalid) {
		t.Errorf("expired: %v", err)
	}
	tok, secret, _ := s.CreateMission(ctx, "default", missionB, time.Hour)
	if _, err := s.VerifyMission(ctx, "other", secret); !errors.Is(err, ErrInvalid) {
		t.Errorf("another profile: %v", err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret+"x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("forged: %v", err)
	}
	if _, err := s.Revoke(ctx, "default", tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrInvalid) {
		t.Errorf("revoked: %v", err)
	}
}

func TestRevokeOwnerEndsOnlyThatRunsTokens(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	b2 := missionB
	b2.Owner = "run-b"
	_, a1, _ := s.CreateMission(ctx, "default", missionB, time.Hour)
	_, a2, _ := s.CreateMission(ctx, "default", missionB, time.Hour)
	_, other, _ := s.CreateMission(ctx, "default", b2, time.Hour)
	n, err := s.RevokeOwner(ctx, "run-a")
	if err != nil || n != 2 {
		t.Fatalf("revoked %d: %v", n, err)
	}
	for _, sec := range []string{a1, a2} {
		if _, err := s.VerifyMission(ctx, "default", sec); !errors.Is(err, ErrInvalid) {
			t.Errorf("a token of the run survived: %v", err)
		}
	}
	if _, err := s.VerifyMission(ctx, "default", other); err != nil {
		t.Errorf("another run's token was revoked: %v", err)
	}
	if n, _ := s.RevokeOwner(ctx, ""); n != 0 {
		t.Errorf("an empty owner revoked %d", n)
	}
}

func countRows(t *testing.T, s *Store, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A sweep revokes the owner's still-active tokens and purges the expired mission tokens, and
// touches neither another owner's live token nor a token that is not a mission token.
func TestSweepMissionsRevokesTheOwnersAndPurgesTheExpired(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	past := NewStore(s.db)
	past.SetClock(func() time.Time { return time.Now().Add(-3 * time.Hour) })
	if _, _, err := past.CreateMission(ctx, "default", missionB, time.Hour); err != nil { // expired two hours ago
		t.Fatal(err)
	}
	b2 := missionB
	b2.Owner = "run-b"
	_, live, _ := s.CreateMission(ctx, "default", missionB, time.Hour)
	_, other, _ := s.CreateMission(ctx, "default", b2, time.Hour)
	plain, plainSecret, err := s.Create(ctx, "default", "plain", ScopeRead, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.SweepMissions(ctx, "run-a", "run-a/new", nil)
	if err != nil || n != 2 { // the expired one was never revoked, so it counts
		t.Fatalf("swept %d: %v", n, err)
	}
	if _, err := s.VerifyMission(ctx, "default", live); !errors.Is(err, ErrInvalid) {
		t.Errorf("the owner's active token survived: %v", err)
	}
	if _, err := s.VerifyMission(ctx, "default", other); err != nil {
		t.Errorf("another owner's token was revoked: %v", err)
	}
	if _, err := s.Verify(ctx, plainSecret); err != nil {
		t.Errorf("a plain token was touched: %v", err)
	}
	if got := countRows(t, s, `mission_session > 0 AND expires_at < ?`, time.Now().UTC().Format(timeFmt)); got != 0 {
		t.Errorf("%d expired mission tokens remain", got)
	}
	if got := countRows(t, s, `mission_session > 0`); got != 2 {
		t.Errorf("the unexpired rows should remain (one revoked, one live): %d", got)
	}
	if got := countRows(t, s, `id = ?`, plain.ID); got != 1 {
		t.Errorf("a non-mission row was removed")
	}
}

// The name carries 8 random bytes; many mints of one binding all succeed.
func TestMissionTokenNameSuffixIsLong(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tok, _, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if i := strings.LastIndex(tok.Name, "-"); len(tok.Name)-i-1 != 16 {
		t.Fatalf("suffix of %q is not 8 bytes of hex", tok.Name)
	}
	for i := 0; i < 50; i++ {
		if _, _, err := s.CreateMission(ctx, "default", missionB, time.Hour); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
}

// The carrier's name must not look like a secret to a runtime that drops such variables by name.
func TestMissionCarrierNameEscapesNameBasedEnvFilters(t *testing.T) {
	up := strings.ToUpper(EnvMissionToken)
	for _, w := range []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "CREDENTIAL", "AUTH"} {
		if strings.Contains(up, w) {
			t.Errorf("%s contains %s: a runtime's default environment policy would drop it", EnvMissionToken, w)
		}
	}
}

// A run that is still running keeps its tokens through another run's sweep; the sweeper's own are
// never taken; a run the liveness check does not know is dead.
func TestSweepMissionsSparesRunningAndOwnRuns(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mint := func(owner string) string {
		b := missionB
		b.Owner = owner
		_, secret, err := s.CreateMission(ctx, "default", b, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	running, dead, self, other := mint("conv-1/t1"), mint("conv-1/t2"), mint("conv-1/t3"), mint("conv-2/t1")
	live := func(_ context.Context, o string) bool { return o == "conv-1/t1" }
	n, err := s.SweepMissions(ctx, "conv-1", "conv-1/t3", live)
	if err != nil || n != 1 {
		t.Fatalf("swept %d: %v", n, err)
	}
	for name, sec := range map[string]string{"running": running, "own": self, "other conversation": other} {
		if _, err := s.VerifyMission(ctx, "default", sec); err != nil {
			t.Errorf("the %s token was revoked: %v", name, err)
		}
	}
	if _, err := s.VerifyMission(ctx, "default", dead); !errors.Is(err, ErrInvalid) {
		t.Errorf("the dead run's token survived: %v", err)
	}
}

// A failing owner in the middle does not leave the owners after it live: all are tried, the count is
// what was revoked, and the error names the owner that failed.
func TestSweepMissionsContinuesPastAFailingOwner(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	secrets := map[string]string{}
	for _, owner := range []string{"grp/a", "grp/b", "grp/c", "grp/d"} {
		b := missionB
		b.Owner = owner
		_, sec, err := s.CreateMission(ctx, "default", b, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		secrets[owner] = sec
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_b BEFORE UPDATE ON agent_tokens WHEN OLD.mission_owner = 'grp/b'
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	n, err := s.SweepMissions(ctx, "grp", "", nil)
	if err == nil || !strings.Contains(err.Error(), "grp/b") || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("the error does not name the failing owner: %v", err)
	}
	if n != 3 {
		t.Errorf("revoked %d, want 3", n)
	}
	for owner, sec := range secrets {
		_, verr := s.VerifyMission(ctx, "default", sec)
		if owner == "grp/b" && verr != nil {
			t.Errorf("the failing owner's token was revoked: %v", verr)
		}
		if owner != "grp/b" && !errors.Is(verr, ErrInvalid) {
			t.Errorf("%s survived the sweep: %v", owner, verr)
		}
	}
}

// The sweep a conductor runs also removes the v2 binding rows that are past retention, at most once an
// interval, and a failure of it never fails the sweep.
func TestSweepMissionsRunsTheV2RetentionSweepOnceAnInterval(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	clock := time.Now()
	s.SetClock(func() time.Time { return clock })
	old := func(id string) {
		t.Helper()
		_, err := s.db.ExecContext(ctx, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES (?, 'default', ?, ?, ?, 2, 'm')`,
			id, "h-"+id, time.Now().UTC().Add(-40*24*time.Hour).Format("2006-01-02T15:04:05.000Z"), time.Now().UTC().Add(-30*24*time.Hour).Format("2006-01-02T15:04:05.000Z"))
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	v2 := func() int {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_tokens_v2`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	old("a")
	if _, err := s.SweepMissions(ctx, "run-a", "run-a/new", nil); err != nil {
		t.Fatal(err)
	}
	if v2() != 0 {
		t.Fatalf("the v2 row past retention was not removed by the sweep (%d left)", v2())
	}
	old("b")
	if _, err := s.SweepMissions(ctx, "run-a", "run-a/new", nil); err != nil {
		t.Fatal(err)
	}
	if v2() != 1 {
		t.Fatal("the retention sweep ran again inside its interval")
	}
	clock = clock.Add(purgeInterval + time.Minute)
	if _, err := s.SweepMissions(ctx, "run-a", "run-a/new", nil); err != nil {
		t.Fatal(err)
	}
	if v2() != 0 {
		t.Fatal("the retention sweep did not run after its interval")
	}
}
