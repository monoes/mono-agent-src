package monomind

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
)

// monomind 2.29 reports per-section spend itself: `org report --format json`
// carries a `sections` field (one row per allocated section, the root
// reserve when allocated, and the org; omitted when the org has no section
// budgets). SectionBudgets stays the source of the breakdown (roles, closure
// events and older monomind need it); this file cross-checks its numbers
// against monomind's own rows so a drift is visible instead of silent.

// CheckRow is one row of monomind's `sections` field.
type CheckRow struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	SpendUSD float64  `json:"spend_usd"`
	Budget   *float64 `json:"budget,omitempty"`
	State    string   `json:"state"`
}

// ReportCheck is the outcome of comparing the derivation with `org report`.
type ReportCheck struct {
	Source string `json:"source"`
	// Agree is true when every monomind row matches the derivation.
	Agree bool `json:"agree"`
	// Rows are monomind's rows, as printed.
	Rows []CheckRow `json:"rows,omitempty"`
	// Mismatches name each disagreeing row and field.
	Mismatches []string `json:"mismatches,omitempty"`
	// Note says why no comparison was made (older monomind, no budgets,
	// report unavailable). Agree is false then and nothing is claimed.
	Note string `json:"note,omitempty"`
}

const checkSource = "monomind org report --format json (sections)"

// monomind rounds spend to 4 decimals; allow half a unit of that.
const checkTolerance = 0.00005 + 1e-9

// CrossCheckSections compares the derived report with the `sections` rows of
// an `org report --format json` payload.
func CrossCheckSections(rep *BudgetReport, reportJSON []byte) *ReportCheck {
	c := &ReportCheck{Source: checkSource}
	var doc struct {
		Sections *[]CheckRow `json:"sections"`
	}
	if err := json.Unmarshal(reportJSON, &doc); err != nil {
		c.Note = "org report output is not JSON: " + err.Error()
		return c
	}
	if doc.Sections == nil || len(*doc.Sections) == 0 {
		c.Note = "org report has no sections field (monomind older than 2.29, or the org has no section budgets)"
		return c
	}
	c.Rows = *doc.Sections
	for _, row := range c.Rows {
		var mine *ScopeBudget
		switch row.Kind {
		case ScopeOrg:
			mine = &rep.Total
		case ScopeReserve:
			mine = rep.Reserve
		case ScopeSection:
			for i := range rep.Sections {
				if rep.Sections[i].Name == row.Name {
					mine = &rep.Sections[i]
				}
			}
		}
		if mine == nil {
			c.Mismatches = append(c.Mismatches, fmt.Sprintf("%s: monomind reports it, the derivation has no such scope", row.Name))
			continue
		}
		if math.Abs(mine.SpentUSD-row.SpendUSD) > checkTolerance {
			c.Mismatches = append(c.Mismatches, fmt.Sprintf("%s: spent %.4f, monomind says %.4f", row.Name, mine.SpentUSD, row.SpendUSD))
		}
		if (mine.AllocationUSD == nil) != (row.Budget == nil) || (row.Budget != nil && math.Abs(*mine.AllocationUSD-*row.Budget) > 1e-9) {
			c.Mismatches = append(c.Mismatches, fmt.Sprintf("%s: allocation differs from monomind's", row.Name))
		}
		if string(mine.State) != row.State {
			c.Mismatches = append(c.Mismatches, fmt.Sprintf("%s: state %s, monomind says %s", row.Name, mine.State, row.State))
		}
	}
	c.Agree = len(c.Mismatches) == 0
	return c
}

// attachCheck asks monomind for its own rows and records the comparison. A
// failing `org report` is a note, never an error: the derivation stands.
func attachCheck(ctx context.Context, rep *BudgetReport, projectRoot, name, run string) {
	if run == "" {
		run = rep.Run
	}
	out, err := OrgReport(ctx, projectRoot, name, false, run)
	if err != nil {
		rep.Check = &ReportCheck{Source: checkSource, Note: "org report unavailable: " + err.Error()}
		return
	}
	rep.Check = CrossCheckSections(rep, out)
}
