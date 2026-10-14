package record

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/recording"
)

// newRecordAPICmd builds `record api`: the JSON API calls a page made while
// it was recorded, and http.request nodes / a workflow built from them.
func newRecordAPICmd(d Deps) *cobra.Command {
	var emitWorkflow bool
	var emitNode int
	var only, name string
	cmd := &cobra.Command{
		Use:   "api <recording>",
		Short: "List the API calls a recording captured, or turn them into http.request nodes",
		Long: `Lists the JSON fetch/XHR calls the page made while it was recorded.

--emit-node N prints call N as an http.request node; --emit-workflow prints a
workflow (manual trigger → one http.request node per call) ready for
"workflow import --file". Credential headers the page sent become
@secret:<name> references: their values were never recorded, so add those
vault secrets before running. Cookies are never recorded.`,
		Example: `  monoagentcli record api rec-20261013-101010-abcd
  monoagentcli record api rec-20261013-101010-abcd --emit-node 2
  monoagentcli record api rec-20261013-101010-abcd --emit-workflow --calls 1,3 > wf.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if emitWorkflow && emitNode > 0 {
				return d.InvalidInput("use either --emit-workflow or --emit-node")
			}
			if err := applyRecordScope(d); err != nil {
				return err
			}
			dir, err := recording.Find(args[0])
			if err != nil {
				return err
			}
			entries, err := recording.LoadNetwork(dir)
			if err != nil {
				return err
			}
			calls := recording.APICalls(entries)
			out := cmd.OutOrStdout()
			switch {
			case emitNode > 0:
				if emitNode > len(calls) {
					return d.InvalidInput("no call %d (the recording has %d)", emitNode, len(calls))
				}
				n := recording.BuildHTTPNode(calls[emitNode-1])
				return d.WriteJSON(out, map[string]any{"type": "http.request", "name": n.Name, "config": n.Config, "secrets": nonNilStrings(n.Secrets)})
			case emitWorkflow:
				picked, err := d.pickCalls(calls, only)
				if err != nil {
					return err
				}
				if len(picked) == 0 {
					return d.InvalidInput("the recording captured no JSON API calls")
				}
				sum, _, err := recording.Load(dir)
				if err != nil {
					return err
				}
				wfName := strings.TrimSpace(name)
				if wfName == "" {
					wfName = "API calls from " + recordFirstNonBlank(sum.Title, sum.ID)
				}
				wf, _ := recording.BuildAPIWorkflow(wfName, picked)
				return d.WriteJSON(out, wf)
			case d.JSONOutput():
				if calls == nil {
					calls = []recording.APICall{}
				}
				return d.WriteJSON(out, map[string]any{"calls": calls})
			}
			printAPICalls(out, calls)
			return nil
		},
	}
	cmd.Flags().BoolVar(&emitWorkflow, "emit-workflow", false, "Print an importable workflow with one http.request node per call")
	cmd.Flags().IntVar(&emitNode, "emit-node", 0, "Print call N as an http.request node")
	cmd.Flags().StringVar(&only, "calls", "", "With --emit-workflow: only these call numbers, comma-separated (default all)")
	cmd.Flags().StringVar(&name, "name", "", "With --emit-workflow: the workflow name")
	return cmd
}

func (d Deps) pickCalls(calls []recording.APICall, only string) ([]recording.APICall, error) {
	if strings.TrimSpace(only) == "" {
		return calls, nil
	}
	var out []recording.APICall
	for _, s := range splitCSV(only) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > len(calls) {
			return nil, d.InvalidInput("--calls: no call %q (the recording has %d)", s, len(calls))
		}
		out = append(out, calls[n-1])
	}
	return out, nil
}

func printAPICalls(w io.Writer, calls []recording.APICall) {
	if len(calls) == 0 {
		fmt.Fprintln(w, "No JSON API calls were captured in this recording.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "N\tMETHOD\tSTATUS\tAFTER\tCREDENTIALS\tURL")
	for _, c := range calls {
		var creds []string
		for _, h := range c.RequestHeaders {
			if h.Credential {
				creds = append(creds, h.Name)
			}
		}
		fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\n", c.N, c.Method, c.Status, recordFirstNonBlank(c.AfterEvent, "-"),
			recordFirstNonBlank(strings.Join(creds, ","), "-"), truncateCell(c.URL, 80))
	}
	tw.Flush()
	fmt.Fprintln(w, "\n--emit-node N prints one as an http.request node; --emit-workflow prints them all as a workflow.")
}
