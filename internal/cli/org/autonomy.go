package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/storage"
)

// pausedIndefinitely stands for "paused until resumed".
var pausedIndefinitely = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

func newOrgAutonomyCmd(env *orgEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autonomy",
		Short: "Choose who decides an org's approvals, questions, and gates (manual, mid, full)",
		Long: "Levels: manual — a person decides everything. mid — rules approve routine items, the " +
			"decider handles consequential ones, a person handles irreversible ones. full — rules approve " +
			"routine items and the decider handles everything else, with no person involved. Decisions are " +
			"made by `monoagentcli daemon`; without it every org behaves as manual.",
	}
	cmd.AddCommand(newOrgAutonomyShowCmd(env), newOrgAutonomySetCmd(env), newOrgAutonomyPauseCmd(env, true),
		newOrgAutonomyPauseCmd(env, false), newOrgAutonomyDecisionsCmd(env), newOrgAutonomyNeedsYouCmd(env))
	return cmd
}

func autonomyView(a *orgdecide.Autonomy) map[string]interface{} {
	now := time.Now()
	var paused interface{}
	if a.PausedUntil != nil && a.PausedUntil.After(now) {
		paused = a.PausedUntil.UTC().Format(time.RFC3339)
	}
	tiers := a.Tiers
	if tiers == nil {
		tiers = map[string]string{}
	}
	_, daemonLive := daemonhb.Read()
	effective := a.EffectiveLevel(now)
	if !daemonLive {
		effective = orgdesign.LevelManual
	}
	updated := ""
	if !a.UpdatedAt.IsZero() {
		updated = a.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return map[string]interface{}{
		"v": 1, "org": a.OrgName, "level": a.Level, "effective_level": effective, "paused_until": paused,
		"decider": map[string]interface{}{
			"kind": a.Decider.Kind, "runtime": a.Decider.Runtime, "model": a.Decider.Model,
			"fallback": a.Decider.Fallback, "timeout_seconds": a.Decider.TimeoutSeconds,
			"threshold": deciderThresholdView(a),
		},
		"tiers": tiers, "default_tiers": orgdecide.DefaultTiers, "policy": a.Policy,
		"on_decider_failure": a.OnDeciderFailure,
		"limits": map[string]interface{}{
			"max_decisions_per_run": a.Limits.MaxDecisionsPerRun, "max_decider_usd_per_run": a.Limits.MaxDeciderUSDPerRun,
		},
		"updated_at": updated, "updated_by": a.UpdatedBy, "daemon_running": daemonLive,
	}
}

// deciderThresholdView is the jev decider's gate, or null for other kinds.
func deciderThresholdView(a *orgdecide.Autonomy) interface{} {
	if a.Decider.Kind != orgdesign.DeciderJev {
		return nil
	}
	if a.Decider.Threshold == 0 {
		return orgdecide.DefaultJevThreshold
	}
	return a.Decider.Threshold
}

func newOrgAutonomyShowCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "show <org>",
		Short: "Show an org's autonomy settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, _, doc, err := loadOrgForEdit(env, args[0])
			if err != nil {
				return err
			}
			a, err := orgdecide.NewStore(db.DB).Get(cmd.Context(), profileID, doc.Name)
			if err != nil {
				return err
			}
			return printJSONValue(autonomyView(a))
		},
	}
}

func newOrgAutonomySetCmd(env *orgEnv) *cobra.Command {
	var level, decider, runtime, model, fallback, policy, policyFile, onFailure, by string
	var timeout, maxDecisions int
	var maxUSD, threshold float64
	var tiers, clearTiers []string
	c := &cobra.Command{
		Use:   "set <org>",
		Short: "Change an org's autonomy level, decider, tiers, or policy (level full is operator only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := env.refuseFullAutonomy(cmd.Flags().Changed("level") && level == orgdesign.LevelFull, args[0]); err != nil {
				return err
			}
			db, profileID, root, doc, err := loadOrgForEdit(env, args[0])
			if err != nil {
				return err
			}
			store := orgdecide.NewStore(db.DB)
			a, err := store.Get(ctx, profileID, doc.Name)
			if err != nil {
				return err
			}
			before := snapshotAutonomy(a)
			f := cmd.Flags()
			if f.Changed("level") {
				a.Level = level
			}
			if f.Changed("decider") {
				a.Decider.Kind = decider
			}
			if f.Changed("decider-runtime") {
				a.Decider.Runtime = runtime
			}
			if f.Changed("decider-model") {
				a.Decider.Model = model
			}
			if f.Changed("fallback") {
				a.Decider.Fallback = fallback
			}
			if f.Changed("decider-timeout") {
				a.Decider.TimeoutSeconds = timeout
			}
			if f.Changed("decider-threshold") {
				if !(threshold > 0 && threshold <= 1) {
					return env.d.InvalidInput("--decider-threshold %v must be in (0,1]", threshold)
				}
				a.Decider.Threshold = threshold
			}
			if f.Changed("policy") {
				a.Policy = policy
			}
			if f.Changed("policy-file") {
				b, err := os.ReadFile(policyFile)
				if err != nil {
					return err
				}
				a.Policy = string(b)
			}
			if f.Changed("on-decider-failure") {
				a.OnDeciderFailure = onFailure
			}
			if f.Changed("max-decisions") {
				a.Limits.MaxDecisionsPerRun = maxDecisions
			}
			if f.Changed("max-decider-usd") {
				a.Limits.MaxDeciderUSDPerRun = maxUSD
			}
			if a.Tiers == nil {
				a.Tiers = map[string]string{}
			}
			for _, kv := range tiers {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return env.d.InvalidInput("--tier %q must be <class>=<tier>", kv)
				}
				a.Tiers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			for _, k := range clearTiers {
				delete(a.Tiers, k)
			}
			if err := env.refuseWidening(before, a, doc.Name); err != nil {
				return err
			}
			if err := env.refuseNotOperator("changing org " + doc.Name + "'s autonomy"); err != nil {
				return err
			}
			if err := a.Validate(); err != nil {
				return env.d.InvalidInput("%v", err)
			}
			// Manual uses no decider: turning an org down to it must not
			// fail on a decider nobody asked to change (#477).
			deciderChanged := false
			for _, name := range []string{"decider", "decider-runtime", "decider-model", "fallback", "decider-threshold"} {
				deciderChanged = deciderChanged || f.Changed(name)
			}
			if deciderChanged || a.Level != orgdesign.LevelManual {
				if err := env.checkDeciderAvailable(ctx, db.DB, profileID, root, doc.Name, a); err != nil {
					return err
				}
			}
			if err := store.Put(ctx, a, by); err != nil {
				return err
			}
			if err := grantDeciderTools(ctx, env, db, profileID, root, doc, a); err != nil {
				return err
			}
			doc.Autonomy = orgdecide.DisplayCopy(a, doc.Autonomy)
			if _, err := orgenv.SaveReconciled(ctx, db, profileID, root, doc, env.genOptions(profileID)); err != nil {
				return err
			}
			return printJSONValue(autonomyView(a))
		},
	}
	f := c.Flags()
	f.StringVar(&level, "level", "", "manual | mid | full")
	f.StringVar(&decider, "decider", "", strings.Join(orgdesign.DeciderKinds, " | ")+" (jev: TypeSafe Jev picks the verdict, the model decides below --decider-threshold)")
	f.StringVar(&runtime, "decider-runtime", "", "Runtime for the model decider (e.g. claude)")
	f.StringVar(&model, "decider-model", "", "Model for the model decider")
	f.StringVar(&fallback, "fallback", "", "Decider for items the chosen one cannot take (model)")
	f.IntVar(&timeout, "decider-timeout", 0, "Seconds before an item goes to the fallback")
	f.Float64Var(&threshold, "decider-threshold", 0, "jev only: lowest top-verdict probability jev decides at, in (0,1] (default 0.8)")
	f.StringVar(&policy, "policy", "", "Instructions the decider follows (\"\" clears)")
	f.StringVar(&policyFile, "policy-file", "", "Read the policy from a file")
	f.StringArrayVar(&tiers, "tier", nil, "Override a class's tier: <class>=routine|consequential|irreversible (repeatable)")
	f.StringArrayVar(&clearTiers, "clear-tier", nil, "Remove a tier override (repeatable)")
	f.StringVar(&onFailure, "on-decider-failure", "", "full only: deny | human")
	f.IntVar(&maxDecisions, "max-decisions", 0, "Decider decisions allowed per run")
	f.Float64Var(&maxUSD, "max-decider-usd", 0, "Decider spend allowed per run (USD)")
	f.StringVar(&by, "by", "cli", "Recorded as updated_by: cli | gui | chat")
	return c
}

func newOrgAutonomyPauseCmd(env *orgEnv, pause bool) *cobra.Command {
	var all bool
	var forDur string
	use, short := "resume <org>|--all", "Resume autonomy after a pause"
	if pause {
		use, short = "pause <org>|--all", "Drop an org to manual right away (for a while, or until resumed)"
	}
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			store := orgdecide.NewStore(db.DB)
			var orgs []string
			switch {
			case all:
				if orgs, err = orgdesign.ListOrgNames(root); err != nil {
					return err
				}
			case len(args) == 1:
				orgs = []string{args[0]}
			default:
				return env.d.InvalidInput("name an org or pass --all")
			}
			until := pausedIndefinitely
			if pause && forDur != "" {
				d, err := time.ParseDuration(forDur)
				if err != nil || d <= 0 {
					return env.d.InvalidInput("--for %q must be a duration like 30m or 2h", forDur)
				}
				until = time.Now().Add(d)
			}
			// Every org is checked before any is written: a refused one
			// leaves them all as they were.
			var changed []*orgdecide.Autonomy
			for _, org := range orgs {
				a, err := store.Get(ctx, profileID, org)
				if err != nil {
					return err
				}
				before := snapshotAutonomy(a)
				if pause {
					u := pauseUntil(a, until)
					a.PausedUntil = &u
				} else {
					a.PausedUntil = nil
				}
				if err := env.refuseWidening(before, a, org); err != nil {
					return err
				}
				changed = append(changed, a)
			}
			if !pause {
				if err := env.refuseNotOperator("resuming autonomy"); err != nil {
					return err
				}
			}
			var views []map[string]interface{}
			for _, a := range changed {
				if err := store.Put(ctx, a, "cli"); err != nil {
					return err
				}
				views = append(views, autonomyView(a))
			}
			// Every row is written before any file, and a file that cannot
			// be rewritten is reported after the rest are: one bad org file
			// never keeps another org's row or file from being updated.
			var failed, reasons []string
			for _, org := range orgs {
				if err := rewriteAutonomyDisplayCopy(ctx, env, db, profileID, root, store, org); err != nil {
					failed = append(failed, org)
					reasons = append(reasons, err.Error())
				}
			}
			var out interface{} = map[string]interface{}{"v": 1, "orgs": views}
			if !all && len(views) == 1 {
				out = views[0]
			}
			if err := printJSONValue(out); err != nil {
				return err
			}
			if len(failed) > 0 {
				verb := "resumed"
				if pause {
					verb = "paused"
				}
				return fmt.Errorf("%s in the database; org file(s) %s not updated: %s",
					verb, strings.Join(failed, ", "), strings.Join(reasons, "; "))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "Every org in the profile")
	if pause {
		c.Flags().StringVar(&forDur, "for", "", "How long, e.g. 30m or 2h (default: until resumed)")
	}
	return c
}

// rewriteAutonomyDisplayCopy rewrites an org file's autonomy block from its
// row, so monomind sees a pause or resume. An org with no file has nothing
// to display it in.
func rewriteAutonomyDisplayCopy(ctx context.Context, env *orgEnv, db *storage.Database, profileID, root string, store *orgdecide.Store, org string) error {
	doc, err := orgdesign.Load(root, org)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("org %q: %w", org, err)
	}
	a, err := store.Get(ctx, profileID, org)
	if err != nil {
		return err
	}
	doc.Autonomy = orgdecide.DisplayCopy(a, doc.Autonomy)
	if _, err := orgenv.SaveReconciled(ctx, db, profileID, root, doc, env.genOptions(profileID)); err != nil {
		return fmt.Errorf("org %q: %w", org, err)
	}
	return nil
}

// idleDeadline reports how long a running org has before its idle watchdog
// stops it, and what is holding the watchdog off.
//
// "Answer this or the org stops in N seconds" is the difference between a
// pending item and a deadline, and needs-you reported it as null for every
// item because nothing ever filled it in. monomind publishes both in `org
// status --json` under the org-idle-deadline capability; a legitimate wait
// (a pending approval, question or gate) reports a hold and no deadline,
// which is exactly what a person needs to see.
//
// Both are nil when the org is not running, when monomind is older than the
// capability, or when status cannot be read — the list is worth showing
// without them.
func idleDeadline(ctx context.Context, root, org string) (interface{}, interface{}) {
	raw, err := orgStatusJSON(ctx, root, org)
	if err != nil {
		return nil, nil
	}
	var st struct {
		IdleStopInSeconds *float64 `json:"idle_stop_in_seconds"`
		IdleHold          *string  `json:"idle_hold"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, nil
	}
	var seconds, hold interface{}
	if st.IdleStopInSeconds != nil {
		seconds = *st.IdleStopInSeconds
	}
	if st.IdleHold != nil && *st.IdleHold != "" {
		hold = *st.IdleHold
	}
	return seconds, hold
}

// ensureNewOrgAutonomy gives an org created by an explicit command its
// starting autonomy row: mid by default (Q8), or manual when the document
// asks for it. A document can never start an org at full.
func ensureNewOrgAutonomy(ctx context.Context, db *storage.Database, profileID string, doc *orgdesign.Doc, by string) error {
	store := orgdecide.NewStore(db.DB)
	a, err := store.Get(ctx, profileID, doc.Name)
	if err != nil || a.Stored {
		return err
	}
	a.Level = orgdesign.LevelMid
	if doc.Autonomy != nil && doc.Autonomy.Level == orgdesign.LevelManual {
		a.Level = orgdesign.LevelManual
	}
	if doc.Autonomy != nil && doc.Autonomy.Policy != "" {
		a.Policy = doc.Autonomy.Policy
	}
	if err := store.Put(ctx, a, by); err != nil {
		return fmt.Errorf("store starting autonomy: %w", err)
	}
	return nil
}
