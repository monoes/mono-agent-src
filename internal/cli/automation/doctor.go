package automation

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	tw "github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"
)

// doctorSelectorJSON is one selector of `automation doctor --json`.
type doctorSelectorJSON struct {
	Key      string `json:"key"`
	OK       int    `json:"ok"`
	Fail     int    `json:"fail"`
	Healed   int    `json:"healed"`
	LastOK   string `json:"lastOk"`   // RFC3339, "" when never
	LastFail string `json:"lastFail"` // RFC3339, "" when never
	Status   string `json:"status"`   // ok | decaying | broken
	// Suggestion is the fix for a decaying/broken selector ("" when ok).
	Suggestion string `json:"suggestion,omitempty"`
	// Stale: health data for a key the installed version no longer has
	// (status "stale", no suggestion: there is nothing to re-record).
	Stale bool `json:"stale,omitempty"`
	// RerecordedAt is when the selector was last re-recorded (RFC3339, "" never).
	RerecordedAt string `json:"rerecordedAt,omitempty"`
	// Repair is the replacement Jev fallback picks propose, if any; once
	// ready, Suggestion is its accept-repair fix.
	Repair *automation.RepairProposal `json:"repair,omitempty"`
}

// doctorAutomationJSON is one automation of `automation doctor --json`
// (contracts §5), plus a few fields the human output needs.
type doctorAutomationJSON struct {
	ID                string                 `json:"id"`
	Version           string                 `json:"version"`
	Enabled           bool                   `json:"enabled"`
	Available         bool                   `json:"available"`
	UnavailableReason string                 `json:"unavailableReason,omitempty"`
	Native            string                 `json:"native,omitempty"` // requires.native bot id
	Issues            []automation.IssueJSON `json:"issues"`
	Selectors         []doctorSelectorJSON   `json:"selectors"`
	Session           automationSession      `json:"session"`
}

// newAutomationDoctorCmd builds `automation doctor [id]`: registry issues,
// availability (policy, missing native bot, engine), login state and
// selector health for each installed automation.
func (d Deps) newAutomationDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor [id]",
		Short: "Check installed automations: issues, login state and selector health",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			var infos []automation.InstalledInfo
			if len(args) == 1 {
				info, err := reg.Info(args[0])
				if err != nil {
					return err
				}
				infos = []automation.InstalledInfo{*info}
			} else if infos, err = reg.List(false); err != nil {
				return err
			}
			sessions, health, repairs := d.loadDoctorState()
			rows := make([]doctorAutomationJSON, 0, len(infos))
			for _, info := range infos {
				rows = append(rows, doctorAutomation(reg, info, sessions.lookupInfo(info), health[info.ID], repairs[info.ID]))
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJSONTo(out, map[string]any{"automations": rows})
			}
			d.printAutomationDoctor(out, rows)
			return nil
		},
	}
	accept := d.newAutomationAcceptRepairCmd()
	d.WithJSONErrors(accept)
	cmd.AddCommand(accept)
	return cmd
}

// newAutomationAcceptRepairCmd builds `automation doctor accept-repair <id>
// <key>`: replace the selector with the candidates Jev fallback picks
// proposed (automation.AcceptRepair), as `automation rerecord` would.
func (d Deps) newAutomationAcceptRepairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accept-repair <id> <selectorKey>",
		Short: "Replace a selector with the repair Jev fallback picks proposed",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			res, err := automation.AcceptRepair(db.DB, reg, args[0], args[1], time.Now())
			if res == nil {
				return err
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJSONTo(out, res)
			}
			fmt.Fprintf(out, "Repaired %s selector %s (%d candidate(s) from %d Jev pick(s), saved in the %s)\n",
				res.Automation, res.Key, len(res.Candidates), res.Picks, res.Where)
			return nil
		},
	}
}

// loadDoctorState reads sessions and selector health. Like `automation
// list`, it never creates a database: no DB yet means no logins and no runs.
func (d Deps) loadDoctorState() (sessionIndex, map[string][]automation.SelectorHealth, map[string][]automation.RepairProposal) {
	health := map[string][]automation.SelectorHealth{}
	repairs := map[string][]automation.RepairProposal{}
	if _, err := os.Stat(d.DBPath()); err != nil {
		return sessionIndex{}, health, repairs
	}
	db, err := d.OpenDB()
	if err != nil {
		stderrf("warning: login state and selector health unavailable: %v\n", err)
		return sessionIndex{}, health, repairs
	}
	defer db.Close()
	if all, err := automation.LoadRepairProposals(db.DB, ""); err != nil {
		stderrf("warning: selector repair proposals unavailable: %v\n", err)
	} else {
		for _, p := range all {
			repairs[p.AutomationID] = append(repairs[p.AutomationID], p)
		}
	}
	return querySessionIndex(db.DB, d.ProfileID(), time.Now()), groupSelectorHealth(db.DB), repairs
}

func groupSelectorHealth(db *sql.DB) map[string][]automation.SelectorHealth {
	out := map[string][]automation.SelectorHealth{}
	rows, err := automation.LoadSelectorHealth(db, "")
	if err != nil {
		stderrf("warning: selector health unavailable: %v\n", err)
		return out
	}
	for _, h := range rows {
		out[h.AutomationID] = append(out[h.AutomationID], h)
	}
	return out
}

// doctorAutomation assembles one automation's report. Selectors are the
// package's declared keys plus any key with recorded health, sorted.
func doctorAutomation(reg *automation.Registry, info automation.InstalledInfo,
	session automationSession, health []automation.SelectorHealth, repairs []automation.RepairProposal) doctorAutomationJSON {
	row := doctorAutomationJSON{
		ID: info.ID, Version: info.Version, Enabled: info.Enabled,
		Available: info.Available, UnavailableReason: info.UnavailableReason,
		Issues: []automation.IssueJSON{}, Selectors: []doctorSelectorJSON{}, Session: session,
	}
	byKey := map[string]doctorSelectorJSON{}
	var declared map[string]bool // nil when the package could not be read
	pkg, err := reg.Get(info.ID)
	if err != nil {
		row.Issues = append(row.Issues, automation.IssueJSON{Severity: "error", Code: "open_failed", Message: err.Error()})
	} else {
		row.Native = pkg.Manifest.Requires.Native
		row.Issues = append(row.Issues, automation.Validate(pkg)...)
		if keys, err := automation.DeclaredSelectorKeys(pkg); err == nil {
			declared = keys
			for key := range keys {
				byKey[key] = doctorSelectorJSON{Key: key, Status: automation.HealthOK}
			}
		}
	}
	if !info.Available {
		row.Issues = append(row.Issues, automation.IssueJSON{Severity: "error", Code: "unavailable", Message: info.UnavailableReason})
	}
	if info.PendingUpdate != "" {
		row.Issues = append(row.Issues, automation.IssueJSON{Severity: "warning", Code: "pending_update",
			Message: fmt.Sprintf("built-in %s is held back because this copy was modified", info.PendingUpdate)})
	}
	for _, h := range health {
		s := doctorSelectorJSON{Key: h.Key, OK: h.OK, Fail: h.Fail, Healed: h.Healed,
			LastOK: rfc3339OrEmpty(h.LastOK), LastFail: rfc3339OrEmpty(h.LastFail), Status: h.Status,
			RerecordedAt: rfc3339OrEmpty(h.RerecordedAt)}
		if declared != nil && !declared[h.Key] {
			s.Stale, s.Status = true, automation.HealthStale
		}
		byKey[h.Key] = s
	}
	for _, p := range repairs {
		if s, ok := byKey[p.Key]; ok && !s.Stale {
			p := p
			s.Repair = &p
			byKey[p.Key] = s
		}
	}
	for _, s := range byKey {
		switch {
		case s.Repair != nil && s.Repair.Ready:
			s.Suggestion = acceptRepairSuggestion(info.ID, s.Key)
		case s.Status != automation.HealthOK && !s.Stale:
			s.Suggestion = rerecordSuggestion(info.ID, s.Key)
		}
		row.Selectors = append(row.Selectors, s)
	}
	sort.Slice(row.Selectors, func(i, j int) bool { return row.Selectors[i].Key < row.Selectors[j].Key })
	return row
}

// rerecordSuggestion is the fix doctor offers for a decaying/broken selector.
func rerecordSuggestion(id, key string) string {
	return "run: monoagentcli automation rerecord " + id + " " + key
}

// acceptRepairSuggestion is the fix doctor offers for a ready repair proposal.
func acceptRepairSuggestion(id, key string) string {
	return "run: monoagentcli automation doctor accept-repair " + id + " " + key
}

func rfc3339OrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// printAutomationDoctor prints one table row per automation, then the
// issues and the selectors that need attention.
func (d Deps) printAutomationDoctor(out io.Writer, rows []doctorAutomationJSON) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "No automations installed.")
		return
	}
	table := d.NewTable(out, []string{"ID", "Version", "Status", "Login", "Issues", "Selectors ok/decaying/broken"},
		[]tw.Align{tw.AlignLeft, tw.AlignLeft, tw.AlignLeft, tw.AlignLeft, tw.AlignRight, tw.AlignLeft})
	for _, r := range rows {
		status := "ok"
		switch {
		case !r.Available:
			status = "unavailable"
		case !r.Enabled:
			status = "disabled"
		}
		login := "-"
		if r.Session.LoggedIn {
			login = r.Session.Username
		} else if r.Session.Username != "" {
			login = r.Session.Username + " (expired)"
		}
		counts := map[string]int{}
		for _, s := range r.Selectors {
			counts[s.Status]++
		}
		table.Append([]string{r.ID, r.Version, status, login, fmt.Sprint(countProblems(r.Issues)),
			fmt.Sprintf("%d/%d/%d", counts[automation.HealthOK], counts[automation.HealthDecaying], counts[automation.HealthBroken])})
	}
	table.Render()
	for _, r := range rows {
		var lines []string
		for _, is := range r.Issues {
			lines = append(lines, fmt.Sprintf("  %-7s %s: %s", is.Severity, is.Code, is.Message))
		}
		for _, s := range r.Selectors {
			if s.Repair != nil && s.Repair.Ready {
				lines = append(lines, fmt.Sprintf("  %-7s selector %s: Jev found %q %d time(s) in a row (last %s)\n          %s",
					"repair", s.Key, s.Repair.Label, s.Repair.Picks, s.Repair.LastSeen.UTC().Format(time.RFC3339), s.Suggestion))
				continue
			}
			if s.Status == automation.HealthOK {
				continue
			}
			if s.Stale {
				lines = append(lines, fmt.Sprintf("  %-7s selector %s: not in %s %s (old health data only)",
					s.Status, s.Key, r.ID, r.Version))
				continue
			}
			lines = append(lines, fmt.Sprintf("  %-7s selector %s: ok %d, healed %d, failed %d (last fail %s)\n          %s",
				s.Status, s.Key, s.OK, s.Healed, s.Fail, orDash(s.LastFail), s.Suggestion))
		}
		if len(lines) > 0 {
			fmt.Fprintf(out, "\n%s\n%s\n", r.ID, strings.Join(lines, "\n"))
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
