package record

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/recording"
)

// NewCommand builds `monoagentcli record …` (extension activity
// recordings). analyze/verify/save come from addRecordAnalyzeCommands
// (analyze.go).
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Browser activity recordings",
		Long: `Recordings are made with the extension's side panel (Record) and land in the
capture inbox as envelopes with source "recording". List, inspect and delete
them here; analyze turns one into a draft automation action.`,
	}
	cmd.AddCommand(
		newRecordListCmd(d),
		newRecordShowCmd(d),
		newRecordDeleteCmd(d),
		newRecordAPICmd(d),
		newRecordReplayCmd(d),
	)
	addRecordAnalyzeCommands(cmd, d)
	// Every record subcommand, analyze's included, prints {"error": …} on
	// stdout under --json (contracts §5): the GUI and the extension's
	// record.* bridge methods read it.
	for _, sub := range cmd.Commands() {
		withRecordJSONErrors(d, sub)
	}
	return cmd
}

// recordJSONErrorsAnnotation marks a command already wrapped by
// withRecordJSONErrors, so a second wrap cannot print the error twice.
const recordJSONErrorsAnnotation = "monoagent/json-errors"

// withRecordJSONErrors is withJSONErrors made idempotent. A record
// subcommand that wants to wrap itself should call this, not
// withJSONErrors, so NewCommand's own pass skips it.
func withRecordJSONErrors(d Deps, cmd *cobra.Command) {
	if cmd.Annotations[recordJSONErrorsAnnotation] != "" {
		return
	}
	d.WithJSONErrors(cmd)
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[recordJSONErrorsAnnotation] = "1"
}

// applyRecordScope points internal/recording at the inboxes this command
// should read: one profile's with --profile, otherwise every inbox a
// recording can land in. Every record subcommand calls it before touching
// recordings (analyze/verify/save included).
func applyRecordScope(d Deps) error {
	id := strings.TrimSpace(d.ProfileID())
	if id == "" {
		return recording.SetProfile("")
	}
	// --profile takes an id or a name; the name needs the database.
	return recording.SetProfile(d.ResolveProfile(id))
}

func newRecordListCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List recordings, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := applyRecordScope(d); err != nil {
				return err
			}
			list, err := recording.List()
			if err != nil {
				return err
			}
			if list == nil {
				list = []recording.Summary{}
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"recordings": list})
			}
			printRecordList(cmd.OutOrStdout(), list)
			return nil
		},
	}
}

func printRecordList(w io.Writer, list []recording.Summary) {
	if len(list) == 0 {
		fmt.Fprintln(w, "No recordings yet. Record one from the extension's side panel.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTARTED\tEVENTS\tSTATUS\tTITLE")
	for _, s := range list {
		status := "complete"
		if !s.Complete {
			status = "incomplete (" + recordFirstNonBlank(s.StopReason, "?") + ")"
		}
		if s.Automation != "" {
			status += " → " + s.Automation
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", s.ID, s.StartedAt, s.Events, status, truncateCell(recordFirstNonBlank(s.Title, s.URL), 50))
	}
	tw.Flush()
}

func newRecordShowCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "show <recording>",
		Short: "Show a recording's events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := applyRecordScope(d); err != nil {
				return err
			}
			dir, err := recording.Find(args[0])
			if err != nil {
				return err
			}
			sum, events, err := recording.Load(dir)
			if err != nil {
				return err
			}
			artifacts, err := recording.Artifacts(dir)
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"summary": sum, "events": events, "artifacts": artifacts})
			}
			printRecordShow(cmd.OutOrStdout(), sum, events, artifacts)
			return nil
		},
	}
}

func printRecordShow(w io.Writer, s *recording.Summary, events []recording.Event, artifacts []string) {
	fmt.Fprintf(w, "%s\n  %s\n  %s\n", recordFirstNonBlank(s.Title, s.ID), s.URL, s.Dir)
	if s.Goal != "" {
		fmt.Fprintf(w, "  goal: %s\n", s.Goal)
	}
	fmt.Fprintf(w, "  started %s, %d events, complete=%v", s.StartedAt, s.Events, s.Complete)
	if s.StopReason != "" {
		fmt.Fprintf(w, " (%s)", s.StopReason)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	for _, e := range events {
		fmt.Fprintf(w, "%4d  %-13s %s\n", e.Seq, e.Type, describeEvent(e))
	}
	fmt.Fprintf(w, "\nartifacts: %s\n", strings.Join(artifacts, ", "))
}

// describeEvent is one line of `record show`.
func describeEvent(e recording.Event) string {
	var parts []string
	if t := e.Target; t != nil {
		label := recordFirstNonBlank(t.AriaName, t.Label, t.Text, t.Placeholder, t.Name, t.ID, t.Tag)
		parts = append(parts, fmt.Sprintf("<%s> %q", t.Tag, truncateCell(label, 40)))
	}
	switch {
	case e.Masked:
		parts = append(parts, "value=•••")
	case e.Value != "":
		parts = append(parts, fmt.Sprintf("value=%q", truncateCell(e.Value, 40)))
	}
	if e.Key != "" {
		parts = append(parts, "key="+e.Key)
	}
	if e.Type == recording.EvNavigate || e.Type == recording.EvNavigated {
		parts = append(parts, e.URL)
	}
	return strings.Join(parts, " ")
}

func newRecordDeleteCmd(d Deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <recording>",
		Short: "Delete a recording",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := applyRecordScope(d); err != nil {
				return err
			}
			dir, err := recording.Find(args[0])
			if err != nil {
				return err
			}
			if !yes && !d.JSONOutput() && !confirmYes(os.Stdin, cmd.OutOrStdout(), "Delete recording "+dir+"?") {
				return d.InvalidInput("not deleted")
			}
			if err := recording.Delete(args[0]); err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s\n", dir)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Do not ask for confirmation")
	return cmd
}

func recordFirstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
