package capturecli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/capturetask"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// newCaptureTaskCmd files a capture as a task on a board (GLU-03).
//
// The board is an org's issue store, <root>/.monomind/orgs/<org>-issues.json
// — monomind's own file, the one the mastermind-issues skills list and the
// dashboard renders. There is no monotask client in this repo and no other
// task store, so wiring to that file is wiring to what exists; a second
// place for tasks would only be a second place to lose them.
func (d Deps) newCaptureTaskCmd() *cobra.Command {
	var (
		boardFlag   string
		title       string
		description string
		priority    string
		assignee    string
		workspace   string
		parent      string
		project     string
	)
	cmd := &cobra.Command{
		Use:   "task <envelope-path>",
		Short: "File a capture as a task on an org's board",
		Long: "Creates an issue on an org's board with the capture attached — every file of\n" +
			"the envelope is listed as an attachment, and the issue carries a `capture`\n" +
			"block with the URL, content hash and envelope path so the document can be\n" +
			"found again without reading the description.\n" +
			"\n" +
			"The board is an org under the project root: the same\n" +
			"<root>/.monomind/orgs/<org>-issues.json that `monomind` and the mastermind\n" +
			"issue skills read. Issues already on the board are left exactly as they were,\n" +
			"including fields this build does not know about.\n" +
			"\n" +
			"With one org under the root, --board may be left out.",
		Example: "  monoagentcli capture task ~/.monomind/inbox/2026-09-21T10-00-00Z-example-com\n" +
			"  monoagentcli capture task ./capture --board acme --title \"Answer this RFC\" --priority high\n" +
			"  monoagentcli capture task ./capture --board acme --assignee alice --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, closeEnv := d.OrgRoot(project)
			defer closeEnv()

			board, err := d.resolveBoard(root, boardFlag)
			if err != nil {
				return err
			}
			res, err := capturetask.Create(capturetask.Options{
				Root:         root,
				Org:          board,
				EnvelopePath: expandPath(args[0]),
				Title:        title,
				Description:  description,
				Priority:     priority,
				Assignee:     assignee,
				Workspace:    workspace,
				Parent:       parent,
			})
			if err != nil {
				return d.InvalidInput("%s", err.Error())
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Filed %s on %s — %s (%s, %d attachment(s))\n",
				res.Issue.ID, res.Org, res.Issue.Title, res.Issue.Priority, len(res.Issue.Attachments))
			fmt.Fprintln(cmd.OutOrStdout(), res.IssuesFile)
			return nil
		},
	}
	cmd.Flags().StringVar(&boardFlag, "board", "", "Org whose board to file on (default: the only org under the project root)")
	cmd.Flags().StringVar(&title, "title", "", "Task title (default: the capture's title, then its URL)")
	cmd.Flags().StringVar(&description, "description", "", "Text to put above the capture's provenance in the task body")
	cmd.Flags().StringVar(&priority, "priority", capturetask.DefaultPriority,
		"Task priority: "+strings.Join(capturetask.Priorities, ", "))
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assign the task to this member or agent")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace id to file the task under")
	cmd.Flags().StringVar(&parent, "parent", "", "Existing issue id to file this task under")
	cmd.Flags().StringVar(&project, "project", "", "Project root holding .monomind/orgs (default: the active profile's root)")
	return cmd
}

// resolveBoard picks the org to file on. Naming one is the normal case;
// leaving it out works only when there is exactly one, because guessing
// between two boards is how a task lands somewhere nobody looks.
func (d Deps) resolveBoard(root, named string) (string, error) {
	named = strings.TrimSpace(named)
	if named != "" {
		if !orgdesign.ValidOrgName(named) {
			return "", d.InvalidInput("invalid org name %q", named)
		}
		return named, nil
	}
	boards, err := capturetask.Boards(root)
	if err != nil {
		return "", err
	}
	switch len(boards) {
	case 0:
		return "", d.NotFound("no board to file on: %s holds no orgs — create one with `monoagentcli org create`, or pass --board", orgdesign.OrgsDir(root))
	case 1:
		return boards[0], nil
	}
	return "", d.InvalidInput("--board is required: %s holds %d orgs (%s)",
		orgdesign.OrgsDir(root), len(boards), strings.Join(boards, ", "))
}
