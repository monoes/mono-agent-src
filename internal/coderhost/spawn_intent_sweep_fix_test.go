package coderhost

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Round-5 findings on the sweep (spawn_intent_sweep.go).

// (1) The candidate read applies each row's ACTUAL lease before its limit: sweeping rows that are still inside their
// back-off must not fill the pass and starve an expired pending intent.
func TestRowsInsideTheirBackOffDoNotStarveAnExpiredIntent(t *testing.T) {
	f := newMC(t, true)
	now := time.Now().UTC()
	f.st.now = func() time.Time { return now }
	at := func(d time.Duration) string { return now.Add(-d).Format(timeLayout) }
	for i := 0; i < 25; i++ { // claimed 3 minutes ago after 3 attempts: their lease is 8 minutes
		if _, err := f.st.db.Exec(`INSERT INTO coder_spawn_intents (profile_id, spawner, scope_hash, child, state, generation, created_at, claimed_at, attempts)
			VALUES ('p', ?, 'h', '', 'sweeping', 5, ?, ?, 3)`, fmt.Sprintf("backoff-%02d", i), at(time.Hour), at(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.db.Exec(`INSERT INTO coder_spawn_intents (profile_id, spawner, scope_hash, child, state, generation, created_at)
		VALUES ('p', 'expired', 'h', '', 'pending', 5, ?)`, at(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := f.st.SweepSpawnIntents(context.Background(), 20)
	if err != nil || n != 1 {
		t.Fatalf("the sweep ended %d (err %v), want the expired intent", n, err)
	}
	if got := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents WHERE spawner = 'expired'`); got != 0 {
		t.Errorf("the expired intent is still there")
	}
	if got := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); got != 25 {
		t.Errorf("%d rows left, the 25 inside their back-off must stay", got)
	}
}

// (2) The delete transaction re-checks the claim itself: when the claim changes between the earlier checks and the
// guarded transaction, the conversation stays.
func TestTheDeleteTransactionRechecksTheClaim(t *testing.T) {
	f := newMC(t, true)
	f.crashedSpawn(t)
	f.afterLease(time.Minute)
	f.st.beforeSweepDelete = func() {
		if _, err := f.st.db.Exec(`UPDATE coder_spawn_intents SET generation = generation + 1`); err != nil {
			t.Error(err)
		}
	}
	_, _ = f.st.SweepSpawnIntents(context.Background(), 20)
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations: a claim that changed before the delete must keep the conversation", n)
	}
}
