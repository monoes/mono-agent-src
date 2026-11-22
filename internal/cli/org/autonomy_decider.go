package org

import (
	"context"
	"database/sql"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// checkDeciderAvailable enforces the plan §6.1 decider rules that need the
// outside world: parent needs a holding org listing this org, boss needs
// monomind tool providers, jev needs a client of its decision provider (for
// TypeSafe, a key for the profile), and
// model (also jev's fallback) needs a model its runtime offers.
func (env *orgEnv) checkDeciderAvailable(ctx context.Context, db *sql.DB, profileID, root, org string, a *orgdecide.Autonomy) error {
	switch a.Decider.Kind {
	case orgdesign.DeciderParent:
		docs, _, err := orgdesign.LoadAll(root)
		if err != nil {
			return err
		}
		if orgdesign.HoldingOwner(docs, org) == "" {
			return env.d.InvalidInput("decider parent needs a holding org that lists %q in its children", org)
		}
	case orgdesign.DeciderBoss:
		set, err := monomind.Capabilities(ctx)
		if err != nil {
			return err
		}
		if err := set.Require(monomind.CapOrgToolProviders, "the boss decider"); err != nil {
			return err
		}
	case orgdesign.DeciderModel:
		return env.checkDeciderModel(ctx, a)
	case orgdesign.DeciderJev:
		// The provider the daemon will use: the decider's own, else the
		// profile's decider surface, else the machine default (D9).
		if _, err := jevconf.NewClientFor(ctx, db, profileID, a.Decider.Provider, "", "", jevconf.Decider); err != nil {
			provider := a.Decider.Provider
			if provider == "" {
				provider = jevconf.ActiveProvider(db, profileID, jevconf.Decider)
			}
			if provider == jev.ProviderTypeSafe {
				return env.d.InvalidInput("decider jev needs a TypeSafe API key: store the machine-wide key with `monoagentcli jev key set` (key on stdin) or set TYPESAFE_API_KEY (%v)", err)
			}
			return env.d.InvalidInput("decider jev cannot use its decision provider: %v", err)
		}
		return env.checkDeciderModel(ctx, a)
	}
	return nil
}

// Seams for checkDeciderModel's tests: both shell out to a runtime binary,
// which a unit test has no business doing.
var (
	scanAgentRuntimes = monomind.Scan
	listRuntimeModels = monomind.ListModels
	orgStatusJSON     = monomind.OrgStatus
)

// checkDeciderModel rejects a model the decider's runtime does not have.
//
// A model decider is only as good as its model name, and a wrong one was
// accepted in silence: it surfaced mid-run as "decider failed: … runner-error:
// done reported nonzero exit_code 1", which reads like a broken decider rather
// than a name that never existed. Two easy ways in — a typo, and
// `--decider-runtime codex` on an org whose decider still carries the default
// Claude model — so the check pays for itself.
//
// Deliberately fail-open on our own inability to look: a runtime with no
// discovery command, an uninstalled binary, or a listing that errors leaves
// the model as typed. Only a definite mismatch is refused.
func (env *orgEnv) checkDeciderModel(ctx context.Context, a *orgdecide.Autonomy) error {
	model := strings.TrimSpace(a.Decider.Model)
	runtimeID := strings.TrimSpace(a.Decider.Runtime)
	if model == "" || runtimeID == "" {
		return nil
	}
	scan, err := scanAgentRuntimes(ctx)
	if err != nil || scan == nil {
		return nil
	}
	binary := ""
	for _, e := range scan.Agents {
		if e.ID == runtimeID {
			if !e.Installed {
				return env.d.InvalidInput("decider runtime %q is not installed%s", runtimeID, installHint(e.InstallHint))
			}
			if e.Binary != nil {
				binary = *e.Binary
			}
			break
		}
	}
	models, err := listRuntimeModels(ctx, runtimeID, binary)
	if err != nil || len(models) == 0 {
		return nil // no catalog to check against — leave the name as typed
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		// Claude lists aliases with the model each resolves to ("fable" →
		// "claude-fable-5-1"); either name reaches the same model.
		if m.ID == model || m.ResolvedID == model {
			return nil
		}
		names = append(names, m.ID)
	}
	return env.d.InvalidInput("decider model %q is not one %s offers; available: %s",
		model, runtimeID, strings.Join(names, ", "))
}

func installHint(hint string) string {
	if strings.TrimSpace(hint) == "" {
		return ""
	}
	return " — " + hint
}
