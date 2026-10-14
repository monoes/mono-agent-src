package summarycli

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/summary"
	"github.com/monoes/mono-agent/internal/workflow"
)

// summaryBridgeTimeout bounds the loopback bridge probe: summary is polled.
const summaryBridgeTimeout = 400 * time.Millisecond

func (d Deps) newSummaryCmd() *cobra.Command {
	var sections string
	var allProfiles bool
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "At-a-glance roll-up of everything that needs attention (read-only, local, fast)",
		Long: "Counts across workflows, runs, schedules, approvals, people, captures, applications, " +
			"automation packages, recordings, Jev usage, logins, the vault, the profile's task board and the background services. " +
			"Read-only and local: it never calls Jev, monomind or the network, so it is safe to poll. " +
			"A section that fails reports \"error\" inside itself; the command still succeeds. " +
			"The vault section is counts only. For orgs, use `org summary`.",
		Example: `  monoagentcli --json summary
  monoagentcli --json summary --section hil,executions
  monoagentcli --json summary --all-profiles`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			want, err := d.parseSummarySections(sections)
			if err != nil {
				return err
			}
			if allProfiles && want["tasks"] {
				return d.InvalidInput("a task board is one profile's, so --all-profiles has no tasks section: use --profile with --section tasks")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			var s summary.Summary
			if allProfiles {
				if s, err = d.buildGlobalSummary(cmd.Context(), db, want); err != nil {
					return err
				}
			} else {
				s = summary.Build(cmd.Context(), d.summaryOptions(db, d.ProfileID(), want))
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), s)
			}
			printSummaryText(cmd.OutOrStdout(), s)
			return nil
		},
	}
	cmd.Flags().StringVar(&sections, "section", "", "Comma-separated sections to include (default: all): "+strings.Join(summary.SectionNames, ","))
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "Roll up every profile (the dashboard's All profiles view); list rows carry profile_id and profile_name")
	return cmd
}

// readOnlyHybridStore is openWorkflowStore without its file→SQLite backfill:
// summary is polled and must not write workflow rows.
func readOnlyHybridStore(db *storage.Database) *workflow.HybridWorkflowStore {
	sqlStore := workflow.NewSQLiteWorkflowStore(db.DB)
	fileStore, err := workflow.NewWorkflowFileStore(expandPath("~/.monoagent/workflows"))
	if err != nil {
		return workflow.NewHybridWorkflowStore(nil, sqlStore)
	}
	return workflow.NewHybridWorkflowStore(fileStore, sqlStore)
}

func (d Deps) parseSummarySections(csv string) (map[string]bool, error) {
	if strings.TrimSpace(csv) == "" {
		return nil, nil
	}
	known := map[string]bool{}
	for _, n := range summary.SectionNames {
		known[n] = true
	}
	want := map[string]bool{}
	for _, n := range strings.Split(csv, ",") {
		n = strings.TrimSpace(n)
		if !known[n] {
			return nil, d.InvalidInput("unknown section %q (known: %s)", n, strings.Join(summary.SectionNames, ", "))
		}
		want[n] = true
	}
	return want, nil
}

type summaryAutomations struct {
	reg *automation.Registry
	db  *sql.DB
}

func (a summaryAutomations) List() ([]automation.InstalledInfo, error) { return a.reg.List(false) }
func (a summaryAutomations) SelectorHealth() ([]automation.SelectorHealth, error) {
	return automation.LoadSelectorHealth(a.db, "")
}
func (a summaryAutomations) DeclaredKeys(id string) (map[string]bool, bool) {
	return automation.RegistrySelectorKeys(a.reg)(id)
}

func summaryDaemon() *summary.DaemonStatus {
	hb, live := daemonhb.Read()
	if hb.PID <= 0 {
		return &summary.DaemonStatus{}
	}
	return &summary.DaemonStatus{Running: live, PID: hb.PID, APIAddr: hb.APIAddr, BridgeAddr: hb.BridgeAddr,
		Version: hb.Version, HeartbeatAgeMS: time.Since(hb.TS).Milliseconds()}
}

// summaryBridge probes the addresses `extension status` uses, in parallel,
// and reports the first (in probe order) that answers as a bridge.
func (d Deps) summaryBridge() (*summary.BridgeStatus, error) {
	addrs := d.ProbeAddrs()
	found := make([]*summary.BridgeStatus, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			if st, err := extension.FetchStatusTimeout(addr, summaryBridgeTimeout); err == nil {
				found[i] = &summary.BridgeStatus{Status: st.Status, Connected: st.Connected,
					Addr: addr, InFlight: st.InFlight, Version: st.Version}
			}
		}(i, addr)
	}
	wg.Wait()
	for _, b := range found {
		if b != nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("no bridge")
}

func printSummaryText(out io.Writer, s summary.Summary) {
	if w := s.Workflows; w != nil {
		fmt.Fprintf(out, "workflows     %d (%d active)\n", w.Total, w.Active)
	}
	if e := s.Executions; e != nil {
		fmt.Fprintf(out, "runs          %d running, %d queued · last 24h: %d ok, %d failed\n",
			e.Running, e.Queued, e.Last24h.Success, e.Last24h.Failed)
	}
	if sc := s.Schedules; sc != nil && len(sc.Upcoming) > 0 {
		state := ""
		if !sc.DaemonRunning {
			state = " (daemon not running — schedules will not fire)"
		}
		fmt.Fprintf(out, "next run      %s %s%s\n", sc.Upcoming[0].NextRun, sc.Upcoming[0].WorkflowName, state)
	}
	if h := s.HIL; h != nil {
		fmt.Fprintf(out, "needs you     %d (approvals %d, leads %d, drafts %d, links %d)\n",
			h.Total, h.WorkflowPending, h.PeopleReview, h.Drafts, h.LinkSuggestions)
	}
	if p := s.People; p != nil {
		fmt.Fprintf(out, "people        %d (+%d this week, %d lists)\n", p.Total, p.Added7d, p.Lists)
	}
	if a := s.Activity; a != nil {
		fmt.Fprintf(out, "activity      %d captures, %d messages in, %d out (7 days)\n", a.Captures7d, a.MessagesIn7d, a.MessagesOut7d)
	}
	if ap := s.Applications; ap != nil {
		fmt.Fprintf(out, "applications  %d pending, %d applied\n", ap.ByStatus["pending"], ap.ByStatus["applied"])
	}
	if a := s.Automations; a != nil {
		fmt.Fprintf(out, "automations   %d installed · selectors broken %d, decaying %d\n", a.Installed, a.Selectors.Broken, a.Selectors.Decaying)
	}
	if r := s.Recordings; r != nil {
		fmt.Fprintf(out, "recordings    %d (%d unsaved)\n", r.Total, r.Unsaved)
	}
	if j := s.Jev; j != nil {
		fmt.Fprintf(out, "jev           %d calls, $%.2f (24h)\n", j.Calls24h, j.EstimatedUSD24h)
	}
	if ac := s.Accounts; ac != nil {
		fmt.Fprintf(out, "logins        %d active (%d expiring), %d expired\n", ac.Active, ac.ExpiringSoon, ac.Expired)
	}
	if v := s.Vault; v != nil {
		fmt.Fprintf(out, "vault         %d secrets, %d images\n", v.Secrets, v.Images)
	}
	if tk := s.Tasks; tk != nil && tk.Inbox+tk.Ready+tk.InProgress+tk.Review > 0 { // a line only when there is open work
		line := fmt.Sprintf("tasks         %d inbox, %d ready, %d in progress (%d stale), %d to review", tk.Inbox, tk.Ready, tk.InProgress, tk.Stale, tk.Review)
		if tk.Next != nil {
			line += fmt.Sprintf(" · next #%d %s", tk.Next.ID, taskCut(tk.Next.Title, 60))
		}
		fmt.Fprintln(out, line)
	}
	if sv := s.Services; sv != nil {
		fmt.Fprintf(out, "services      daemon %s · bridge %s · org serve %s\n",
			onOff(sv.Daemon.Running), bridgeState(sv.Bridge), onOff(sv.OrgServe.Running))
	}
}

func onOff(b bool) string {
	if b {
		return "running"
	}
	return "stopped"
}

func bridgeState(b *summary.BridgeStatus) string {
	if b == nil {
		return "stopped"
	}
	return b.Status
}
