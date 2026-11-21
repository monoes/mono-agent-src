package orgdecide

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/orgdesign"
)

// ReconcileResult reports what ReconcileAutonomy did.
type ReconcileResult struct {
	Lowered    bool   // the JSON asked for a lower level and the row took it
	Ignored    string // a raise or other change in the JSON that was not applied
	DocChanged bool   // the display copy in doc was rewritten
}

// ReconcileAutonomy applies the only change the org JSON may make to the
// enforced settings — a lower level — and rewrites the JSON's autonomy block
// as a display copy of the row (C-54). A role that edits its own org file to
// "full", or rewrites the decider or policy, changes nothing.
func ReconcileAutonomy(ctx context.Context, s *Store, profileID string, doc *orgdesign.Doc) (*ReconcileResult, error) {
	res := &ReconcileResult{}
	row, err := s.Get(ctx, profileID, doc.Name)
	if err != nil {
		return nil, err
	}
	if doc.Autonomy != nil && doc.Autonomy.Level != "" {
		want := orgdesign.LevelRank(doc.Autonomy.Level)
		have := orgdesign.LevelRank(row.Level)
		switch {
		case want >= 0 && want < have:
			row.Level = doc.Autonomy.Level
			if err := s.Put(ctx, row, "reconcile"); err != nil {
				return nil, err
			}
			res.Lowered = true
		case want > have:
			res.Ignored = fmt.Sprintf("the org file asks for level %q but the enforced level is %q; raise it with `monoagentcli org autonomy set`", doc.Autonomy.Level, row.Level)
		}
	}
	before, _ := json.Marshal(doc.Autonomy)
	if row.Stored {
		doc.Autonomy = DisplayCopy(row, doc.Autonomy)
	} else if doc.Autonomy != nil {
		// No enforced settings: the org is manual whatever the file says.
		doc.Autonomy = &orgdesign.Autonomy{Level: orgdesign.LevelManual, Extra: doc.Autonomy.Extra}
	}
	after, _ := json.Marshal(doc.Autonomy)
	res.DocChanged = string(before) != string(after)
	return res, nil
}

// DisplayCopy renders a row as the org JSON autonomy block, keeping keys
// the previous block carried that mono-agent does not model. paused_until
// is written only while the pause is active, so the next reconcile after it
// ends drops it.
func DisplayCopy(a *Autonomy, prev *orgdesign.Autonomy) *orgdesign.Autonomy {
	out := &orgdesign.Autonomy{
		Level: a.Level,
		Decider: &orgdesign.Decider{
			Kind: a.Decider.Kind, Runtime: a.Decider.Runtime, Model: a.Decider.Model,
			Fallback: a.Decider.Fallback, TimeoutSeconds: a.Decider.TimeoutSeconds,
		},
		Policy:           a.Policy,
		OnDeciderFailure: a.OnDeciderFailure,
		Limits: &orgdesign.AutonomyLimits{
			MaxDecisionsPerRun: a.Limits.MaxDecisionsPerRun, MaxDeciderUSDPerRun: a.Limits.MaxDeciderUSDPerRun,
		},
	}
	// The jev decider's threshold and provider stay in the row: the org
	// file's decider block is the shape monomind knows
	// (docs/plans/2026-09-16-org-unification-contracts.md).
	if a.PausedUntil != nil && a.PausedUntil.After(time.Now()) {
		out.PausedUntil = a.PausedUntil.UTC().Format(time.RFC3339)
	}
	if len(a.Tiers) > 0 {
		out.Tiers = map[string]string{}
		for k, v := range a.Tiers {
			out.Tiers[k] = v
		}
	}
	if prev != nil {
		out.Extra = prev.Extra
	}
	return out
}
