package application

import (
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/matching"

	"github.com/spf13/cobra"
)

func (d Deps) newEvaluateCmd() *cobra.Command {
	var runtime string
	cmd := &cobra.Command{
		Use:   "evaluate <id>",
		Short: "Score a job application's fit against your profile using a local AI agent or TypeSafe Jev",
		Long: `Score a job application's fit against your profile's ingested knowledge.

--runtime names a local agent runtime (default claude), or "jev" / "jev:<model>"
to score with TypeSafe Jev: one request answers the three hard gates and the four
rubric dimensions, and the overall score and verdict are computed with the same
weights and bands as the agent rubric. Jev needs a TypeSafe key (vault secret
"typesafe" or TYPESAFE_API_KEY); without one the command fails rather than
falling back to an agent. Sends the job posting and profile excerpts to TypeSafe.`,
		Args: cobra.ExactArgs(1),
		Example: `  monoagentcli application evaluate 1c2e... --runtime claude
  monoagentcli application evaluate 1c2e... --runtime jev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			verdict, err := matching.Evaluate(cmd.Context(), db.DB, d.ProfileID(), args[0], runtime)
			if err != nil {
				return fmt.Errorf("evaluating application: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(verdict)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (overall %.1f) — %s\n", verdict.Verdict, verdict.OverallScore, verdict.Rationale)
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "claude", "Local agent `runtime` (see monoagentcli agent scan --installed), or jev / jev:<model> for TypeSafe Jev")
	return cmd
}

func (d Deps) newEvaluatePendingCmd() *cobra.Command {
	var runtime string
	var limit int
	cmd := &cobra.Command{
		Use:   "evaluate-pending",
		Short: "Evaluate every pending job application with no evaluation yet",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			store := applications.NewStore(db.DB)
			apps, err := store.List(cmd.Context(), d.ProfileID(), applications.ListFilter{
				Kind: applications.KindJob, Status: applications.StatusPending,
			})
			if err != nil {
				return fmt.Errorf("listing pending applications: %w", err)
			}

			verdictCounts := map[string]int{}
			evaluated := 0
			for _, app := range apps {
				if limit > 0 && evaluated >= limit {
					break
				}
				var already int
				if err := db.DB.QueryRowContext(cmd.Context(),
					`SELECT COUNT(*) FROM application_evaluations WHERE application_id = ?`, app.ID,
				).Scan(&already); err != nil {
					return fmt.Errorf("checking existing evaluations: %w", err)
				}
				if already > 0 {
					continue
				}
				verdict, err := matching.Evaluate(cmd.Context(), db.DB, d.ProfileID(), app.ID, runtime)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: evaluating %s failed: %v\n", app.ID, err)
					continue
				}
				verdictCounts[verdict.Verdict]++
				evaluated++
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{"evaluated": evaluated, "verdicts": verdictCounts})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Evaluated %d application(s): %v\n", evaluated, verdictCounts)
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "claude", "Local agent runtime to use, or jev / jev:<model> for TypeSafe Jev")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum applications to evaluate (0 = no limit)")
	return cmd
}
