package health

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/testdb"
)

func TestCoderSpawnIntentsWarnsOnAHeldIntent(t *testing.T) {
	db := testdb.Open(t)
	defer db.Close()
	env := &Env{DB: db.DB}
	if r := checkCoderSpawnIntents(context.Background(), env); r.Status != StatusOK {
		t.Fatalf("no intents: %+v", r)
	}
	if _, err := db.DB.Exec(`INSERT INTO coder_spawn_intents (profile_id, spawner, scope_hash, state, created_at) VALUES ('p', 'c', 'h', 'needs-operator', 'x')`); err != nil {
		t.Fatal(err)
	}
	if r := checkCoderSpawnIntents(context.Background(), env); r.Status != StatusWarn {
		t.Fatalf("a held intent: %+v", r)
	}
}
