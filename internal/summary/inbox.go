package summary

import (
	"context"
	"database/sql"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/peoplereview"
)

type HILSection struct {
	WorkflowPending int `json:"workflow_pending"`
	PeopleReview    int `json:"people_review"`
	Drafts          int `json:"drafts"`
	LinkSuggestions int `json:"link_suggestions"`
	// CoderItems counts, per kind, the requests of coder sessions that the person has to answer now and that
	// nothing times out (today "parked_question"; a kind is added in coderhost). Counts only, never text.
	CoderItems map[string]int `json:"coder_items,omitempty"`
	// CoderItemIDs are those items' ids, so a surface can tell when a NEW one appears. Numbers only.
	CoderItemIDs       []int64 `json:"coder_item_ids,omitempty"`
	Total              int     `json:"total"`
	OldestWaitingSince string  `json:"oldest_waiting_since"`
	Error              string  `json:"error,omitempty"`
}

type PeopleSection struct {
	Total   int    `json:"total"`
	Added7d int    `json:"added_7d"`
	Lists   int    `json:"lists"`
	Error   string `json:"error,omitempty"`
}

type DocumentCounts struct {
	Total         int `json:"total"`
	Indexed       int `json:"indexed"`
	IndexErrors   int `json:"index_errors"`
	Summarising   int `json:"summarising"`
	SummaryErrors int `json:"summary_errors"`
}

type ActivitySection struct {
	Captures7d    int            `json:"captures_7d"`
	CapturesTotal int            `json:"captures_total"`
	Documents     DocumentCounts `json:"documents"`
	MessagesIn7d  int            `json:"messages_in_7d"`
	MessagesOut7d int            `json:"messages_out_7d"`
	// MessagesUnread counts inbound messages not marked read (all time).
	MessagesUnread int    `json:"messages_unread"`
	Error          string `json:"error,omitempty"`
}

type ApplicationsSection struct {
	ByStatus           map[string]int `json:"by_status"`
	Evaluated          int            `json:"evaluated"`
	UnevaluatedPending int            `json:"unevaluated_pending"`
	Added7d            int            `json:"added_7d"`
	Error              string         `json:"error,omitempty"`
}

func weekAgo(o Options) string { return sqlTime(o.Now.Add(-7 * 24 * time.Hour)) }

// hilSection counts what a person must decide, without asking Jev.
// Org questions/approvals/gates come from `org summary`, not here.
func hilSection(ctx context.Context, o Options) *HILSection {
	s := &HILSection{}
	if o.DB == nil {
		s.Error = noDB
		return s
	}
	// Same scope as `hil list` (h.profile_id), minus org-triggered rows:
	// those are org items, counted by `org summary` (orgdecide.PendingHIL).
	var oldest any
	err := o.DB.QueryRowContext(ctx, `SELECT COUNT(*), MIN(h.created_at) FROM hil_pending h
		LEFT JOIN workflow_executions e ON e.id = h.execution_id
		WHERE h.status = 'pending' AND h.profile_id = ?
		  AND COALESCE(e.trigger_type, '') NOT IN ('org_tool', 'org_message')`, o.ProfileID).Scan(&s.WorkflowPending, &oldest)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.OldestWaitingSince = timeString(oldest)
	counts := []struct {
		dst  *int
		q    string
		args []any
	}{
		{&s.PeopleReview, `SELECT COUNT(*) FROM people WHERE profile_id = ? AND category = ?`, []any{o.ProfileID, peoplereview.Pending}},
		{&s.Drafts, `SELECT COUNT(*) FROM person_messages WHERE profile_id = ? AND status = 'draft'`, []any{o.ProfileID}},
		{&s.LinkSuggestions, `SELECT COUNT(*) FROM person_links WHERE profile_id = ? AND status = 'suggested'`, []any{o.ProfileID}},
	}
	for _, c := range counts {
		if err := o.DB.QueryRowContext(ctx, c.q, c.args...).Scan(c.dst); err != nil {
			s.Error = err.Error()
			return s
		}
	}
	s.Total = s.WorkflowPending + s.PeopleReview + s.Drafts + s.LinkSuggestions
	items, ids, err := coderItems(ctx, o)
	if err != nil {
		s.Error = err.Error() // the rest is still counted
		return s
	}
	s.CoderItems, s.CoderItemIDs = items, ids
	coder := 0
	for _, n := range items {
		coder += n
	}
	s.Total = s.WorkflowPending + s.PeopleReview + s.Drafts + s.LinkSuggestions + coder
	return s
}

func peopleSection(ctx context.Context, o Options) *PeopleSection {
	s := &PeopleSection{}
	if o.DB == nil {
		s.Error = noDB
		return s
	}
	err := o.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(`+sinceExpr("created_at")+` >= julianday(?)),0)
		FROM people WHERE profile_id = ?`, weekAgo(o), o.ProfileID).Scan(&s.Total, &s.Added7d)
	if err == nil {
		err = o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM social_lists WHERE profile_id = ?`, o.ProfileID).Scan(&s.Lists)
	}
	s.Error = errString(err)
	return s
}

// activitySection reads vault_documents directly rather than calling
// `profile documents list`, which syncs (writes) first — summary is read-only.
func activitySection(ctx context.Context, o Options) *ActivitySection {
	s := &ActivitySection{}
	var errs []error
	if o.Captures != nil {
		caps, err := o.Captures()
		if err != nil {
			errs = append(errs, err)
		}
		bound := o.Now.Add(-7 * 24 * time.Hour)
		for _, c := range caps {
			s.CapturesTotal++
			if t, err := time.Parse(time.RFC3339, c.CapturedAt); err == nil && !t.Before(bound) {
				s.Captures7d++
			}
		}
	}
	if o.DB == nil {
		s.Error = noDB
		return s
	}
	err := o.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(indexed = 1),0),
		COALESCE(SUM(COALESCE(index_error,'') <> ''),0)
		FROM vault_documents WHERE profile_id = ?`, o.ProfileID).
		Scan(&s.Documents.Total, &s.Documents.Indexed, &s.Documents.IndexErrors)
	if err != nil {
		errs = append(errs, err)
	} else if o.SummaryState != nil {
		if err := countSummaryStates(ctx, o, &s.Documents); err != nil {
			errs = append(errs, err)
		}
	}
	err = o.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(direction = 'inbound'),0), COALESCE(SUM(direction = 'outbound' AND status <> 'draft'),0)
		FROM person_messages WHERE profile_id = ? AND `+sinceExpr("created_at")+` >= julianday(?)`,
		o.ProfileID, weekAgo(o)).Scan(&s.MessagesIn7d, &s.MessagesOut7d)
	if err != nil {
		errs = append(errs, err)
	}
	// Matches idx_person_messages_unread (050).
	if err := o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_messages
		WHERE profile_id = ? AND direction = 'inbound' AND read_at IS NULL`, o.ProfileID).Scan(&s.MessagesUnread); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		s.Error = errs[0].Error()
	}
	return s
}

func countSummaryStates(ctx context.Context, o Options, d *DocumentCounts) error {
	rows, err := o.DB.QueryContext(ctx, `SELECT capture_dir FROM vault_documents
		WHERE profile_id = ? AND COALESCE(capture_dir,'') <> ''`, o.ProfileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dir sql.NullString
		if err := rows.Scan(&dir); err != nil {
			return err
		}
		switch o.SummaryState(dir.String) {
		case "pending", "running":
			d.Summarising++
		case "error", "stalled":
			d.SummaryErrors++
		}
	}
	return rows.Err()
}

func applicationsSection(ctx context.Context, o Options) *ApplicationsSection {
	s := &ApplicationsSection{ByStatus: map[string]int{"pending": 0, "applied": 0, "rejected": 0, "cancelled": 0}}
	if o.DB == nil {
		s.Error = noDB
		return s
	}
	rows, err := o.DB.QueryContext(ctx, `SELECT status, COUNT(*) FROM applications WHERE profile_id = ? GROUP BY status`, o.ProfileID)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	for rows.Next() {
		var st string
		var n int
		if rows.Scan(&st, &n) == nil {
			s.ByStatus[st] = n
		}
	}
	rows.Close()
	err = o.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(DISTINCT e.application_id) FROM application_evaluations e
		   JOIN applications a ON a.id = e.application_id WHERE a.profile_id = ?),
		(SELECT COUNT(*) FROM applications a WHERE a.profile_id = ? AND a.status = 'pending'
		   AND NOT EXISTS (SELECT 1 FROM application_evaluations e WHERE e.application_id = a.id)),
		(SELECT COUNT(*) FROM applications WHERE profile_id = ? AND `+sinceExpr("created_at")+` >= julianday(?))`,
		o.ProfileID, o.ProfileID, o.ProfileID, weekAgo(o)).Scan(&s.Evaluated, &s.UnevaluatedPending, &s.Added7d)
	s.Error = errString(err)
	return s
}

// coderItems counts, per kind, the requests of coder sessions that the person has to act on now. What
// is open is decided by coderhost (the question's answerer today and the session's state), not here.
func coderItems(ctx context.Context, o Options) (map[string]int, []int64, error) {
	items, err := coderhost.NewStore(o.DB).OpenPersonItems(o.ProfileID)
	if err != nil {
		return nil, nil, err
	}
	counts, ids := map[string]int{}, []int64{}
	for _, it := range items {
		counts[it.Kind]++
		ids = append(ids, it.ID)
	}
	return counts, ids, nil
}
