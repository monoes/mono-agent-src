package monomind

import "context"

type CheckRow struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	SpendUSD float64  `json:"spend_usd"`
	Budget   *float64 `json:"budget,omitempty"`
	State    string   `json:"state"`
}

type ReportCheck struct {
	Source     string     `json:"source"`
	Agree      bool       `json:"agree"`
	Rows       []CheckRow `json:"rows,omitempty"`
	Mismatches []string   `json:"mismatches,omitempty"`
	Note       string     `json:"note,omitempty"`
}

// stub: no comparison yet
func CrossCheckSections(rep *BudgetReport, reportJSON []byte) *ReportCheck {
	return &ReportCheck{}
}

func attachCheck(ctx context.Context, rep *BudgetReport, projectRoot, name, run string) {}
