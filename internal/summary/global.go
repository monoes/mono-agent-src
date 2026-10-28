package summary

import (
	"sort"
	"time"
)

// The global dashboard: every profile's summary rolled into one. Each
// section is still computed per profile by Build, so the queries stay
// profile-scoped. Merge only adds, concatenates and tags. Sections that are
// the same whichever profile asks (services, automations) come from one
// build and are copied as they are.

const (
	ScopeProfile = "profile"
	ScopeGlobal  = "global"
)

// ProfilePart is one profile's summary, as Merge takes it.
type ProfilePart struct {
	ID   string
	Name string
	S    Summary
}

func (p ProfilePart) label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// ProfileHeadline is one profile's row in the global dashboard.
type ProfileHeadline struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Current         bool   `json:"current"`
	WorkflowsActive int    `json:"workflows_active"`
	Running         int    `json:"running"`
	Failed24h       int    `json:"failed_24h"`
	WaitingForYou   int    `json:"waiting_for_you"`
	Error           string `json:"error,omitempty"`
}

// Merge rolls per-profile summaries into the global one. shared holds the
// profile-independent sections; current is the profile the caller runs as.
func Merge(parts []ProfilePart, shared Summary, current string) Summary {
	out := Summary{
		V: 1, GeneratedAt: shared.GeneratedAt, Scope: ScopeGlobal,
		Services: shared.Services, Automations: shared.Automations,
		Profiles: make([]ProfileHeadline, 0, len(parts)),
	}
	for _, p := range parts {
		out.Profiles = append(out.Profiles, headline(p, current))
		addRuns(&out, p)
		addInbox(&out, p)
		addSystem(&out, p)
	}
	if out.Executions != nil {
		out.Executions.Recent = SortRecent(out.Executions.Recent, recentLimit)
	}
	if out.Schedules != nil {
		sort.SliceStable(out.Schedules.Upcoming, func(i, j int) bool {
			return before(out.Schedules.Upcoming[i].NextRun, out.Schedules.Upcoming[j].NextRun)
		})
	}
	return out
}

// SortRecent orders runs newest first and keeps at most limit of them
// (limit <= 0 keeps all).
func SortRecent(rows []ExecRow, limit int) []ExecRow {
	sort.SliceStable(rows, func(i, j int) bool { return before(rows[j].CreatedAt, rows[i].CreatedAt) })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// before compares two of this package's timestamps (normaliseTime writes
// RFC3339 UTC); anything unparseable falls back to string order.
func before(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA == nil && errB == nil {
		return ta.Before(tb)
	}
	return a < b
}

// tagErr appends one profile's section error, prefixed with whose it is.
func tagErr(have, err, who string) string {
	if err == "" {
		return have
	}
	e := who + ": " + err
	if have == "" {
		return e
	}
	return have + "; " + e
}

func headline(p ProfilePart, current string) ProfileHeadline {
	h := ProfileHeadline{ID: p.ID, Name: p.label(), Current: p.ID == current}
	if w := p.S.Workflows; w != nil {
		h.WorkflowsActive = w.Active
		h.Error = tagErr(h.Error, w.Error, "workflows")
	}
	if e := p.S.Executions; e != nil {
		h.Running = e.Running
		h.Failed24h = e.Last24h.Failed
		h.Error = tagErr(h.Error, e.Error, "executions")
	}
	if hl := p.S.HIL; hl != nil {
		h.WaitingForYou = hl.Total
		h.Error = tagErr(h.Error, hl.Error, "hil")
	}
	return h
}

func addRuns(out *Summary, p ProfilePart) {
	who := p.label()
	if s := p.S.Workflows; s != nil {
		if out.Workflows == nil {
			out.Workflows = &WorkflowsSection{}
		}
		d := out.Workflows
		d.Total += s.Total
		d.Active += s.Active
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.Executions; s != nil {
		if out.Executions == nil {
			out.Executions = &ExecutionsSection{Recent: []ExecRow{}}
		}
		d := out.Executions
		d.Running += s.Running
		d.Queued += s.Queued
		d.Waiting += s.Waiting
		d.Last24h.Total += s.Last24h.Total
		d.Last24h.Success += s.Last24h.Success
		d.Last24h.Failed += s.Last24h.Failed
		d.Last24h.Cancelled += s.Last24h.Cancelled
		for _, r := range s.Recent {
			r.ProfileID, r.ProfileName = p.ID, who
			d.Recent = append(d.Recent, r)
		}
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.Schedules; s != nil {
		if out.Schedules == nil {
			out.Schedules = &SchedulesSection{Upcoming: []ScheduleRow{}, Invalid: []ScheduleIssue{}}
		}
		d := out.Schedules
		d.DaemonRunning = d.DaemonRunning || s.DaemonRunning
		for _, r := range s.Upcoming {
			r.ProfileID, r.ProfileName = p.ID, who
			d.Upcoming = append(d.Upcoming, r)
		}
		for _, r := range s.Invalid {
			r.ProfileID, r.ProfileName = p.ID, who
			d.Invalid = append(d.Invalid, r)
		}
		d.Error = tagErr(d.Error, s.Error, who)
	}
}

func addInbox(out *Summary, p ProfilePart) {
	who := p.label()
	if s := p.S.HIL; s != nil {
		if out.HIL == nil {
			out.HIL = &HILSection{}
		}
		d := out.HIL
		d.WorkflowPending += s.WorkflowPending
		d.PeopleReview += s.PeopleReview
		d.Drafts += s.Drafts
		d.LinkSuggestions += s.LinkSuggestions
		for k, n := range s.CoderItems {
			if d.CoderItems == nil {
				d.CoderItems = map[string]int{}
			}
			d.CoderItems[k] += n
		}
		d.Total += s.Total
		if s.OldestWaitingSince != "" && (d.OldestWaitingSince == "" || before(s.OldestWaitingSince, d.OldestWaitingSince)) {
			d.OldestWaitingSince = s.OldestWaitingSince
		}
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.People; s != nil {
		if out.People == nil {
			out.People = &PeopleSection{}
		}
		d := out.People
		d.Total += s.Total
		d.Added7d += s.Added7d
		d.Lists += s.Lists
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.Activity; s != nil {
		if out.Activity == nil {
			out.Activity = &ActivitySection{}
		}
		d := out.Activity
		d.Captures7d += s.Captures7d
		d.CapturesTotal += s.CapturesTotal
		d.Documents.Total += s.Documents.Total
		d.Documents.Indexed += s.Documents.Indexed
		d.Documents.IndexErrors += s.Documents.IndexErrors
		d.Documents.Summarising += s.Documents.Summarising
		d.Documents.SummaryErrors += s.Documents.SummaryErrors
		d.MessagesIn7d += s.MessagesIn7d
		d.MessagesOut7d += s.MessagesOut7d
		d.MessagesUnread += s.MessagesUnread
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.Applications; s != nil {
		if out.Applications == nil {
			out.Applications = &ApplicationsSection{ByStatus: map[string]int{}}
		}
		d := out.Applications
		for k, v := range s.ByStatus {
			d.ByStatus[k] += v
		}
		d.Evaluated += s.Evaluated
		d.UnevaluatedPending += s.UnevaluatedPending
		d.Added7d += s.Added7d
		d.Error = tagErr(d.Error, s.Error, who)
	}
}

func addSystem(out *Summary, p ProfilePart) {
	who := p.label()
	if s := p.S.Recordings; s != nil {
		if out.Recordings == nil {
			out.Recordings = &RecordingsSection{}
		}
		d := out.Recordings
		d.Total += s.Total
		d.Unsaved += s.Unsaved
		d.Incomplete += s.Incomplete
		if before(d.LatestStartedAt, s.LatestStartedAt) {
			d.LatestStartedAt = s.LatestStartedAt
		}
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.Jev; s != nil {
		if out.Jev == nil {
			out.Jev = &JevSection{}
		}
		d := out.Jev
		if s.KeyConfigured && !d.KeyConfigured || d.KeySource == "" {
			d.KeySource = s.KeySource
		}
		d.KeyConfigured = d.KeyConfigured || s.KeyConfigured
		d.SurfacesEnabled += s.SurfacesEnabled
		d.Calls24h += s.Calls24h
		d.Failures24h += s.Failures24h
		d.EstimatedUSD24h += s.EstimatedUSD24h
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.Accounts; s != nil {
		if out.Accounts == nil {
			out.Accounts = &AccountsSection{Sessions: []SessionRow{}}
		}
		d := out.Accounts
		for _, r := range s.Sessions {
			r.ProfileID, r.ProfileName = p.ID, who
			d.Sessions = append(d.Sessions, r)
		}
		d.Active += s.Active
		d.Expired += s.Expired
		d.ExpiringSoon += s.ExpiringSoon
		d.Error = tagErr(d.Error, s.Error, who)
	}
	if s := p.S.Vault; s != nil {
		if out.Vault == nil {
			out.Vault = &VaultSection{}
		}
		d := out.Vault
		d.Secrets += s.Secrets
		d.Images += s.Images
		d.ImageBytes += s.ImageBytes
		d.Error = tagErr(d.Error, s.Error, who)
	}
}
