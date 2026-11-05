package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The resource governor's construction in the engine (note B3). With no running run every
// part is a plain call.

var (
	governorMu sync.Mutex
	governors  = map[string]*doomsdaycli.GovernorWire{}
)

// processGovernor is the process's one GovernorWire per profile: the coder host's launcher and the strategy loop's tick share
// it, so the queue, the slots and the backoff are one governor, not two.
func processGovernor(cfg *globalConfig, db *sql.DB) *doomsdaycli.GovernorWire {
	governorMu.Lock()
	defer governorMu.Unlock()
	if g, ok := governors[cfg.ProfileID]; ok {
		return g
	}
	keys := doomsdaycli.VaultKeyer{DB: db, Profile: cfg.ProfileID}
	g := &doomsdaycli.GovernorWire{
		DB: db, Profile: cfg.ProfileID, Keys: doomsdaycli.NewCachedVaultKeyer(keys), Runs: store.New(db, keys),
		RunDir: stop.RunDir{Dir: doomsdayRunDirPath(cfg)}, Activity: processRunActivity(cfg.ProfileID),
		Dirs: []string{filepath.Dir(expandPath(cfg.DBPath)), os.TempDir()},
		ProviderOf: func(profile, conv string) string {
			a, err := ai.NewAIStore(db)
			if err != nil {
				return ""
			}
			c, err := a.GetConversation(conv, profile)
			if err != nil {
				return ""
			}
			if c.ProviderID != "" {
				return c.ProviderID
			}
			return c.RuntimeID
		},
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
	}
	governors[cfg.ProfileID] = g
	return g
}

// governLauncher puts the governor on the host's launcher (after the dispatch gate's own wrap, so a refusal costs no ticket)
// and tells it how to find a turn that outlived a restart of the host.
func governLauncher(cfg *globalConfig, st *coderhost.Store, h *coderhost.Host, db *sql.DB) {
	g := processGovernor(cfg, db)
	g.Alive = func(conv, turn string) bool {
		x, ok, err := st.GetSession(cfg.ProfileID, conv)
		return err == nil && ok && x.TurnID == turn && x.TurnPID > 0 && h.L.Adopt(x.TurnPID, x.TurnStamp) != nil
	}
	h.L = g.GovernLauncher(h.L)
}

// governorGroupFor adds `doomsday governor status` to the group (read-only: the read database and the verified charter).
func governorGroupFor(cfg *globalConfig, grp *cobra.Command, d doomsdaycli.Deps) {
	grp.AddCommand(doomsdaycli.GovernorCommands(d, governorStatusFor(cfg)))
}

func governorStatusFor(cfg *globalConfig) doomsdaycli.GovernorStatusFunc {
	return func(ctx context.Context, generation int64) (doomsdaycli.GovernorView, error) {
		db, err := openReadDB(cfg) // the read verbs create and migrate nothing
		if err != nil {
			return doomsdaycli.GovernorView{SchemaVersion: doomsdaycli.GovernorSchema, Off: true}, nil
		}
		defer db.Close()
		return doomsdaycli.GovernorReader{DB: db, Profile: cfg.ProfileID, Runs: store.New(db, doomsdaycli.VaultKeyer{DB: db, Profile: cfg.ProfileID})}.Read(ctx, generation)
	}
}
