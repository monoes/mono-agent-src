package people

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/peopledupes"
	"github.com/spf13/cobra"
)

type dedupeReport struct {
	Applied bool                      `json:"applied"`
	Backup  string                    `json:"backup,omitempty"`
	Groups  []peopledupes.Group       `json:"groups"`
	Results []peopledupes.GroupResult `json:"results,omitempty"`
}

func (d Deps) newPeopleDedupeCmd() *cobra.Command {
	var (
		apply  bool
		backup string
	)
	cmd := &cobra.Command{
		Use:   "dedupe",
		Short: "Find (and with --apply merge) people stored twice under a different case",
		Long: "Lists groups of people who are one person written twice: the same username under a " +
			"platform spelled in two cases (instagram / INSTAGRAM), or the same email address in two " +
			"cases. For each group it shows the row that would win (the one with the most data), " +
			"and which messages, tags and other rows would move to it. Nothing is changed unless " +
			"--apply is given. People of different profiles are never merged.\n\n" +
			"--apply merges every group in its own transaction. The winner keeps its own data and " +
			"fills its empty fields from the others; an approved person is never demoted (the " +
			"approved introduction is kept), and a group holding both an approved and a rejected " +
			"row is skipped for you to decide. Before it changes anything it writes the rows it " +
			"will remove and re-point to --backup (default: a new file in ~/.monoagent/backups) " +
			"and prints the path. " + clicore.OperatorOnlyHelp + " Only --apply is the user's.",
		Example: `  monoagentcli --json people dedupe
  monoagentcli people dedupe --apply
  monoagentcli people dedupe --apply --backup ~/dedupe-undo.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply {
				if why := opgate.Refusal(); why != "" {
					return clicore.OperatorOnly("only the user merges people (it removes rows): the user runs this in their own terminal after reading the dry run; this is not the user's terminal: %s", why)
				}
			}
			if backup != "" && !apply {
				return d.InvalidInput("--backup needs --apply")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			groups, err := peopledupes.Find(cmd.Context(), db.DB, d.ProfileID())
			if err != nil {
				return err
			}
			rep := dedupeReport{Applied: apply, Groups: groups}
			if groups == nil {
				rep.Groups = []peopledupes.Group{}
			}
			if apply && len(groups) > 0 {
				if backup == "" {
					if backup, err = defaultDedupeBackup(); err != nil {
						return err
					}
				}
				if rep.Results, err = peopledupes.Apply(cmd.Context(), db.DB, d.ProfileID(), groups, backup); err != nil {
					return err
				}
				if _, statErr := os.Stat(backup); statErr == nil {
					rep.Backup = backup
				}
			}
			if d.JSONOutput() {
				if err := printReviewJSON(rep); err != nil {
					return err
				}
			} else {
				d.printDedupe(rep)
			}
			for _, r := range rep.Results {
				if r.Err != "" {
					return fmt.Errorf("a group could not be merged and was rolled back; see the report (undo file: %s)", rep.Backup)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Merge the groups (default is a dry run that changes nothing)")
	cmd.Flags().StringVar(&backup, "backup", "", "Where --apply writes the undo file (default: ~/.monoagent/backups/people-dedupe-<time>.json)")
	return cmd
}

func defaultDedupeBackup() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory for the undo file (use --backup): %w", err)
	}
	dir := filepath.Join(home, ".monoagent", "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s (use --backup): %w", dir, err)
	}
	return filepath.Join(dir, "people-dedupe-"+time.Now().UTC().Format("20060102T150405Z")+".json"), nil
}

func (d Deps) printDedupe(rep dedupeReport) {
	if len(rep.Groups) == 0 {
		fmt.Println("No duplicate people found.")
		return
	}
	for i, g := range rep.Groups {
		fmt.Printf("Group %d: %s (%s)\n", i+1, g.Key, g.Reason)
		table := d.NewTable(os.Stdout, []string{"", "ID", "Platform", "Username", "Category", "Fields", "Messages", "Tags"}, nil)
		for _, m := range g.Members {
			role := "merge into winner"
			if m.ID == g.Winner {
				role = "WINNER"
			}
			table.Append([]string{role, m.ID, m.Platform, truncateStr(m.Username, 30), m.Category,
				fmt.Sprint(m.Score), fmt.Sprint(m.Messages), fmt.Sprint(m.Tags)})
		}
		table.Render()
		if len(g.Repoint) > 0 {
			var parts []string
			for k, n := range g.Repoint {
				parts = append(parts, fmt.Sprintf("%s x%d", k, n))
			}
			fmt.Printf("  would re-point: %s\n", strings.Join(parts, ", "))
		}
		if g.Conflict != "" {
			fmt.Printf("  NOT merged: %s\n", g.Conflict)
		}
		if i < len(rep.Results) {
			r := rep.Results[i]
			switch {
			case r.Err != "":
				fmt.Printf("  FAILED, rolled back: %s\n", r.Err)
			case r.Skipped != "":
				fmt.Printf("  skipped: %s\n", r.Skipped)
			default:
				fmt.Printf("  merged %d row(s)\n", r.Merged)
			}
		}
	}
	if rep.Applied {
		if rep.Backup != "" {
			fmt.Printf("\nUndo file: %s\n", rep.Backup)
		}
		return
	}
	fmt.Printf("\n%d group(s). This was a dry run; nothing changed. Merge them with: monoagentcli people dedupe --apply\n", len(rep.Groups))
}
