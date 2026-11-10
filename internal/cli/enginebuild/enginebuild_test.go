package enginebuild

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/action"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/scheduler"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

type fakeProvider struct {
	profile string
	closed  int
}

func (p *fakeProvider) GetPage(context.Context, string, string) (browserpkg.PageInterface, error) {
	return nil, errors.New("no browser in tests")
}
func (p *fakeProvider) Close() { p.closed++ }

type noBots struct{}

func (noBots) GetAdapter(string) (action.BotAdapter, bool) { return nil, false }

// Build opens the database first, then reads the profile (OpenDB resolves
// --profile), and hands it to the session provider; the cleanup closes it.
func TestBuildWiresTheEngine(t *testing.T) {
	db := testdb.Open(t)
	var order []string
	var provider *fakeProvider
	var storeDB *storage.Database
	engine, cleanup, err := Build(Options{
		ProfileID:        func() string { order = append(order, "profile"); return "work" },
		AllowAllProfiles: true,
	}, Deps{
		OpenDB: func() (*storage.Database, error) { order = append(order, "db"); return db, nil },
		NewSessionProvider: func(_ zerolog.Logger, profile string) SessionProvider {
			provider = &fakeProvider{profile: profile}
			return provider
		},
		BotRegistry: noBots{},
		OpenWorkflowStore: func(d *storage.Database) *workflow.HybridWorkflowStore {
			storeDB = d
			return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(d.DB))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, ok := engine.Scheduler().(*scheduler.Scheduler)
	if !ok {
		t.Fatalf("engine scheduler is %T, want *scheduler.Scheduler", engine.Scheduler())
	}
	defer sched.Stop() //nolint:errcheck
	if len(order) != 2 || order[0] != "db" || order[1] != "profile" {
		t.Fatalf("calls = %v, want [db profile]", order)
	}
	if provider == nil || provider.profile != "work" {
		t.Fatalf("session provider = %+v, want default profile work", provider)
	}
	if storeDB != db {
		t.Fatal("the workflow store was not opened over the engine's database")
	}
	cleanup()
	if provider.closed != 1 {
		t.Fatalf("cleanup closed the session provider %d times, want 1", provider.closed)
	}
}

// The database Build opened is the engine's to close: left open, its file stays
// locked on Windows, where a command's own temp dir (a test's) cannot be removed.
func TestBuildCleanupClosesTheDatabaseItOpened(t *testing.T) {
	db := testdb.Open(t)
	_, cleanup, err := Build(Options{ProfileID: func() string { return "work" }}, Deps{
		OpenDB: func() (*storage.Database, error) { return db, nil },
		NewSessionProvider: func(zerolog.Logger, string) SessionProvider {
			return &fakeProvider{}
		},
		BotRegistry: noBots{},
		OpenWorkflowStore: func(d *storage.Database) *workflow.HybridWorkflowStore {
			return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(d.DB))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if err := db.DB.Ping(); err == nil {
		t.Fatal("the database is still open after cleanup")
	}
}

func TestBuildReportsADatabaseThatDoesNotOpen(t *testing.T) {
	boom := errors.New("boom")
	engine, cleanup, err := Build(Options{
		ProfileID: func() string { t.Fatal("profile read without a database"); return "" },
	}, Deps{
		OpenDB: func() (*storage.Database, error) { return nil, boom },
		NewSessionProvider: func(zerolog.Logger, string) SessionProvider {
			t.Fatal("session provider built without a database")
			return nil
		},
	})
	if engine != nil || !errors.Is(err, boom) || err.Error() != "open database: boom" {
		t.Fatalf("Build = %v, %v; want nil, open database: boom", engine, err)
	}
	cleanup() // a no-op, safe to defer
}
