package org

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orggrant"
)

// Profile lifecycle hooks for orgs (plan C-24). A profile's orgs live in its
// folder and its grants, endpoints, and autonomy rows are keyed by its id,
// so moving the folder or deleting the profile has an org half:
//
//	move:   org serve --stop  → move the folder → org reconcile → org serve
//	delete: org teardown-profile → remove the profile
//
// The GUI's "Move profile folder" runs the move sequence through these
// commands; there is no profile delete in the CLI or GUI yet, and whatever
// adds one calls `org teardown-profile` first, while the profile row (and
// its root_dir) still exists.

// orgStopReport is what stopping a folder's org processes did.
type orgStopReport struct {
	ServePID     int
	ServeStatus  string // stopped | not-running | error | running (dry run)
	StoppedOrgs  []string
	RunningOrgs  []string // dry run only
	Warnings     []string
	serveStopErr error
}

// runningOrgs asks monomind which orgs of root are running.
func runningOrgs(ctx context.Context, root string) ([]string, error) {
	raw, err := monomind.OrgStatus(ctx, root, "")
	if err != nil {
		return nil, err
	}
	var st struct {
		Items []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("unreadable org status: %w", err)
	}
	var out []string
	for _, it := range st.Items {
		if it.Status == "running" && it.Name != "" {
			out = append(out, it.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// stopRootOrgs asks every running org of root to stop, then stops the
// folder's `org serve`. A failure to list or stop an org is a warning — the
// serve stop still runs; a failure to stop the serve is kept for the caller.
// With dryRun it only reports what is running.
func stopRootOrgs(ctx context.Context, root string, dryRun bool) *orgStopReport {
	rep := &orgStopReport{StoppedOrgs: []string{}, Warnings: []string{}, ServeStatus: "not-running"}
	if _, err := os.Stat(root); err != nil {
		return rep // nothing can run out of a folder that is not there
	}
	running, err := runningOrgs(ctx, root)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "could not list running orgs: "+err.Error())
	}
	if dryRun {
		rep.RunningOrgs = append([]string{}, running...)
		if hb, live := monomind.ReadServeHeartbeat(root); live {
			rep.ServePID, rep.ServeStatus = hb.PID, "running"
		}
		return rep
	}
	for _, name := range running {
		if _, err := monomind.OrgStop(ctx, root, name); err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("stopping org %s: %v", name, err))
			continue
		}
		rep.StoppedOrgs = append(rep.StoppedOrgs, name)
	}
	pid, stopped, err := monomind.OrgServeStop(ctx, root)
	rep.ServePID = pid
	if stopped {
		rep.ServeStatus = "stopped"
	}
	if err != nil {
		rep.ServeStatus = "error"
		rep.serveStopErr = err
		rep.Warnings = append(rep.Warnings, err.Error())
	}
	return rep
}

func (r *orgStopReport) json(root string) map[string]interface{} {
	return map[string]interface{}{
		"v": 1, "root": root, "pid": r.ServePID, "status": r.ServeStatus,
		"stopped_orgs": r.StoppedOrgs, "warnings": r.Warnings,
	}
}

func newOrgTeardownProfileCmd(env *orgEnv) *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "teardown-profile",
		Short: "Stop the profile folder's orgs and org daemon and revoke every org grant, endpoint, autonomy setting and API key of the profile",
		Long: "The org half of deleting a profile (select it with the global --profile). It revokes every " +
			"automation grant and automation-role endpoint of the profile (endpoint ids in a rotation grace " +
			"window included), removes its autonomy settings, and expires its pending delegated decisions; " +
			"then it stops the orgs running out of the profile's folder and the folder's `org serve`, and " +
			"strips the dead tool-provider blocks from the org files (an automation role keeps its endpoint " +
			"URL, whose id is refused from now on). Org files, run data, and the decision ledger are " +
			"otherwise left in place. It also revokes the profile's API keys (the OpenAI-compatible API). " +
			"Run it before removing the profile, " +
			"while its folder setting still exists. --dry-run reports what it would do.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if env.Project() != "" {
				return env.d.InvalidInput("teardown-profile acts on a whole profile; select it with --profile, not --project")
			}
			if why := operator.Refusal(); why != "" && !dryRun {
				return clicore.OperatorOnly("teardown-profile revokes the profile's API keys, org grants and endpoints, which cuts off whatever uses them and only the user decides: %s; "+
					"run it yourself in your terminal or the app (--dry-run shows what it would do)", why)
			}
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			store := orggrant.NewStore(db.DB)
			keyStore := apikeys.NewStore(db.DB)
			var revoked orggrant.ProfileRevocation
			var keys int // the profile's active API keys, revoked (dry run: that would be)
			if dryRun {
				revoked, err = store.ProfileFootprint(ctx, profileID)
				if err == nil {
					keys, err = keyStore.CountActive(ctx, profileID)
				}
			} else {
				// Revoke first: a provider or endpoint that outlives a failed
				// process stop can then no longer act for the profile.
				revoked, err = store.RevokeProfile(ctx, profileID)
				if err == nil {
					keys, err = keyStore.RevokeProfile(ctx, profileID)
				}
			}
			if err != nil {
				return err
			}
			stop := stopRootOrgs(ctx, root, dryRun)
			out := map[string]interface{}{
				"v": 1, "profile": profileID, "root": root, "dry_run": dryRun,
				"revoked": revoked, "org_serve": map[string]interface{}{"pid": stop.ServePID, "status": stop.ServeStatus},
				"stopped_orgs": stop.StoppedOrgs, "warnings": stop.Warnings, "api_keys": keys,
			}
			if dryRun {
				out["running_orgs"] = stop.RunningOrgs
			} else if _, err := os.Stat(root); err == nil {
				// Strip the now-dead provider blocks from the org files,
				// which outlive the profile when its folder is a project
				// the user keeps.
				saved := 0
				for _, o := range env.reconciler(db).Profile(ctx, orgdecide.ProfileRoot{ProfileID: profileID, Root: root}) {
					if o.Saved {
						saved++
					}
					if o.Error != "" {
						stop.Warnings = append(stop.Warnings, o.Org+": "+o.Error)
					}
				}
				out["org_files_rewritten"] = saved
				out["warnings"] = stop.Warnings
			}
			if err := printJSONValue(out); err != nil {
				return err
			}
			if stop.serveStopErr != nil {
				return fmt.Errorf("grants and endpoints revoked, but the org daemon could not be stopped: %w", stop.serveStopErr)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be revoked and stopped without changing anything")
	return c
}

func newOrgReconcileCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile every org file of the profile's folder with its grants, endpoints, and autonomy rows",
		Long: "Rewrites each org's generated blocks (tool providers, approval tools, endpoint URLs) from the " +
			"enforcement rows and saves the files that changed — the same pass `monoagentcli daemon` runs at " +
			"startup. Run it after a profile's folder moves, before starting `org serve` there.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			var warnings []string
			orgs := env.reconciler(db).Profile(cmd.Context(), orgdecide.ProfileRoot{ProfileID: profileID, Root: root})
			if orgs == nil {
				orgs = []orgenv.ReconcileOutcome{}
			}
			for _, o := range orgs {
				if o.Error != "" {
					warnings = append(warnings, o.Org+": "+o.Error)
				}
			}
			if warnings == nil {
				warnings = []string{}
			}
			return printJSONValue(map[string]interface{}{"v": 1, "profile": profileID, "root": root, "orgs": orgs, "warnings": warnings})
		},
	}
}
