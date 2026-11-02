package task

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// newSessionRenameCmd is `task session rename ID TITLE`: the operator renames any session, an
// agent only one it started.
func (d Deps) newSessionRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename ID TITLE...",
		Short: "Change a session's title (you: any session; an agent: one it started)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return d.InvalidInput("task session rename takes a session id and the new title")
			}
			id, err := d.parseSessionID(args[0])
			if err != nil {
				return err
			}
			actor, err := d.actorFor(cmd, "rename a session")
			if err != nil {
				return err
			}
			title := strings.Join(args[1:], " ")
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				se, err := store.RenameSession(ctx, p.ID, id, title, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeSession(cmd, p, se, "Renamed")
			})
		},
	}
}

// newSessionTreeCmd is `task session tree`: the sessions nested by parent, ordered by id.
func (d Deps) newSessionTreeCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "tree [--all]",
		Short: "Show the sessions nested below their parents (archived ones with --all)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return d.InvalidInput("task session tree takes no arguments (got %q)", cutArg(args[0]))
			}
			actor := d.callerFor(flagAs(cmd)).actor
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				tree, err := store.SessionTree(ctx, p.ID, all, actor)
				if err != nil {
					return d.taskErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), tree)
				}
				w := cmd.OutOrStdout()
				printProfileLine(w, p)
				if len(tree) == 0 {
					fmt.Fprintln(w, "No sessions.")
					return nil
				}
				printSessionTree(w, tree, 0)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include archived sessions")
	return cmd
}

func printSessionTree(w io.Writer, nodes []tasks.SessionNode, level int) {
	for _, n := range nodes {
		fmt.Fprintf(w, "%s#%d  %-8s %-9s %d/%d done  %s of %s  %s\n", strings.Repeat("  ", level), n.ID, n.State, n.Kind,
			n.Counts.Done, n.Counts.Total, money(n.SpentUSD), budgetWord(n.Session), taskCut(n.Title, 60))
		printSessionTree(w, n.Children, level+1)
	}
}
