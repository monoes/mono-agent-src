package orgsvc

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The real org services: every goroutine Start launches ends with its context,
// and Wait says so, in both generations; that is what lets the supervisor stop
// them and start them again.
func TestOrgServicesStopWhenTheirContextEnds(t *testing.T) {
	dbPath := t.TempDir() + "/org.db"
	testdb.MustSeed(dbPath)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	engine := workflow.NewWorkflowEngineWithStore(workflow.NewSQLiteWorkflowStore(db.DB), db.DB, nil,
		workflow.NewNodeTypeRegistry(), workflow.EngineConfig{WebhookAddr: "127.0.0.1:0"}, zerolog.Nop())
	orgs := New(db, engine, openTestWorkflowStore)
	orgs.logf = t.Logf
	orgs.watchInterval = 20 * time.Millisecond

	for generation := 1; generation <= 2; generation++ {
		ctx, cancel := context.WithCancel(context.Background())
		orgs.Start(ctx, engine)
		if orgs.Wait(50 * time.Millisecond) {
			t.Fatalf("generation %d: wait said the services had ended while their context was live", generation)
		}
		cancel()
		if !orgs.Wait(10 * time.Second) {
			t.Fatalf("generation %d: the org services did not end within 10s of their context ending", generation)
		}
	}
}
