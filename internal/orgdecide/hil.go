package orgdecide

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/hilscrub"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// PendingHIL lists Human-in-Loop items paused inside executions an org
// started — through a grant (org_tool) or a message to an automation role
// (org_message). They are that org's decisions, at the tier of the grant
// that started them (U12, Q12, class hil:<alias>). HIL items of standalone
// workflows are never touched.
func PendingHIL(ctx context.Context, db *sql.DB, profileID, org string, doc *orgdesign.Doc) ([]Item, error) {
	// Rows an older version wrote may hold a granted secret's value: they are
	// scrubbed before the review text is read (#1594).
	hilscrub.Ensure(ctx, db, profileID)
	rows, err := db.QueryContext(ctx, `
		SELECT h.id, COALESCE(h.node_name, ''), COALESCE(h.readonly_data, ''), COALESCE(e.trigger_data, '{}'), e.trigger_type, h.execution_id, COALESCE(h.created_at, '')
		FROM hil_pending h JOIN workflow_executions e ON e.id = h.execution_id
		WHERE h.status = 'pending' AND h.profile_id = ? AND e.trigger_type IN ('org_tool', 'org_message')`, profileID)
	if err != nil {
		return nil, fmt.Errorf("orgdecide: hil items: %w", err)
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var id, node, readonly, triggerData, triggerType, execID, created string
		if err := rows.Scan(&id, &node, &readonly, &triggerData, &triggerType, &execID, &created); err != nil {
			return nil, err
		}
		readonly = hilscrub.Mask(db, profileID, id, readonly) // while a pass has failed
		var td struct {
			Org struct {
				Name       string `json:"name"`
				Role       string `json:"role"`
				Automation string `json:"automation"`
			} `json:"org"`
			Message struct {
				Org  string `json:"org"`
				Role string `json:"role"`
				From string `json:"from"`
			} `json:"org_message"`
		}
		_ = json.Unmarshal([]byte(triggerData), &td)
		alias, requester := "", ""
		switch triggerType {
		case "org_tool":
			if td.Org.Name != org {
				continue
			}
			alias, requester = td.Org.Automation, td.Org.Role
		case "org_message":
			if td.Message.Org != org {
				continue
			}
			requester = td.Message.From
			if doc != nil {
				if r, _ := doc.FindRole(td.Message.Role); r != nil && r.Automation != nil {
					alias = r.Automation.Alias
				}
			}
		}
		if alias == "" {
			alias = "unknown"
		}
		ts := int64(0)
		if t, err := time.Parse("2006-01-02 15:04:05", created); err == nil {
			ts = t.UnixMilli()
		}
		items = append(items, Item{
			Kind: KindHIL, Ref: id, Requester: requester, Class: "hil:" + alias, Name: node,
			Text:      fmt.Sprintf("Paused at node %q of execution %s. Data for review:\n%s", node, execID, readonly),
			Summary:   fmt.Sprintf("automation %s paused at %q for review", alias, node),
			WaitingMS: ts, Hash: hashItem(KindHIL, requester, alias, node, readonly),
		})
	}
	return items, rows.Err()
}

// toPerson passes on record's result for an item left to a person:
// escalated to one, or kept pending when its decider failed. A HIL item
// then carries its org in node_config, so the org's bubble shows it
// (asksFor) as it waits on the person. One the org settles by rule or by
// its decider (a failure it denies included), or hands to a boss or parent
// role, stays untagged, on the Human in Loop page only.
func (s *Service) toPerson(ctx context.Context, profileID, org string, it Item) func(*Decision, error) (*Decision, error) {
	return func(d *Decision, err error) (*Decision, error) {
		if err != nil || it.Kind != KindHIL || s.DB == nil {
			return d, err
		}
		if _, uerr := s.DB.ExecContext(ctx, `UPDATE hil_pending SET node_config = json_set(COALESCE(NULLIF(node_config, ''), '{}'), '$.org', ?)
			WHERE id = ? AND profile_id = ? AND status = 'pending'`, org, it.Ref, profileID); uerr != nil {
			s.Logf("orgdecide: showing HIL item %s in %s's bubble: %v", it.Ref, org, uerr)
		}
		return d, err
	}
}

// resolveHIL approves or rejects a HIL item; the paused run resumes on the
// engine's next poll. It never approves one whose approval lets a message to
// a person go out: only the user decides that (`hil approve`), whatever
// routed it here.
func resolveHIL(ctx context.Context, db *sql.DB, id string, approve bool) error {
	status := "rejected"
	if approve {
		status = "approved"
		if ItemSends(ctx, db, "", "", Item{Kind: KindHIL, Ref: id}, nil) {
			return fmt.Errorf("HIL item %s lets a message to a person go out: only the user decides it (`monoagentcli hil approve|reject %s`)", id, id)
		}
	}
	res, err := db.ExecContext(ctx, `UPDATE hil_pending SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'pending'`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("HIL item %s is no longer pending", id)
	}
	_ = commsync.SetStatus(ctx, db, id, commsync.Status(status), commsync.Detail{})
	return nil
}
