package orgbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/workflow"
)

type replyJob struct {
	profileID, org, role, executionID, chainID string
	hop                                        int
}

// sendReplies answers every finished automation-role run that has not
// replied yet (the reply's own endpoint_reply crossing is the marker; the
// run's own org.send crossings are workflow_out and do not count). The
// marker is a crossing that settled as anything but an error: Ledger.Admit
// records the row inside Send BEFORE the delivery and only a failed
// delivery flips it to 'error', so counting the bare row dropped a reply
// permanently on one transient `monomind org inbox` failure and left the
// sender waiting. A refusal (hop or repeat limit) is deliberate and final,
// so it still counts; errors are retried up to maxReplyAttempts times.
func (r *Receiver) sendReplies(ctx context.Context) error {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT c.profile_id, c.org_name, c.role_id, c.execution_id, c.chain_id, c.hop
		FROM org_bridge_calls c JOIN workflow_executions e ON e.id = c.execution_id
		WHERE c.direction = ? AND c.status = 'ok' AND e.status IN ('SUCCESS', 'FAILED', 'CANCELLED')
		  AND NOT EXISTS (SELECT 1 FROM org_bridge_calls x
		                  WHERE x.execution_id = c.execution_id AND x.direction = ? AND x.status <> 'error')
		  AND (SELECT COUNT(*) FROM org_bridge_calls x
		       WHERE x.execution_id = c.execution_id AND x.direction = ?) < ?`,
		DirEndpointIn, DirEndpointReply, DirEndpointReply, maxReplyAttempts)
	if err != nil {
		return err
	}
	var jobs []replyJob
	for rows.Next() {
		var j replyJob
		if err := rows.Scan(&j.profileID, &j.org, &j.role, &j.executionID, &j.chainID, &j.hop); err == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	for _, j := range jobs {
		if err := r.reply(ctx, j); err != nil {
			r.Logf("orgbridge: reply for %s: %v", j.executionID, err)
		}
	}
	return nil
}

func (r *Receiver) reply(ctx context.Context, j replyJob) error {
	exec, err := r.Store.GetExecution(ctx, j.executionID)
	if err != nil || exec == nil {
		return fmt.Errorf("load execution: %v", err)
	}
	msg, _ := exec.TriggerData["org_message"].(map[string]interface{})
	from, _ := msg["from"].(string)
	subject, _ := msg["subject"].(string)
	if from == "" {
		return errors.New("execution has no org message to reply to")
	}
	replyMode := "last_node"
	root := r.RootOf(j.profileID)
	if doc, err := orgdesign.Load(root, j.org); err == nil {
		if role, _ := doc.FindRole(j.role); role != nil && role.Automation != nil && role.Automation.Reply != "" {
			replyMode = role.Automation.Reply
		}
	}
	body := ReplyBody(exec, replyMode)
	targetOrg, to := j.org, from
	if i := strings.IndexByte(from, ':'); i > 0 {
		targetOrg, to = from[:i], from[i+1:]
	}
	_, err = Send(ctx, NewLedger(r.DB), SendRequest{
		ProfileID: j.profileID, Root: root, Org: targetOrg, To: to, From: j.org + ":" + j.role,
		Subject: "re: " + subject, Body: body, Trace: Trace{ChainID: j.chainID, Hop: j.hop},
		OriginOrg: j.org, Direction: DirEndpointReply, WorkflowID: exec.WorkflowID, ExecutionID: exec.ID,
	})
	return err
}

// ReplyBody renders a finished run as the automation role's reply: the
// chosen node's output items, redacted and bounded, or the failure.
func ReplyBody(exec *workflow.WorkflowExecution, mode string) string {
	if exec.Status != "SUCCESS" {
		b, _ := json.Marshal(map[string]interface{}{"status": strings.ToLower(exec.Status), "error": exec.ErrorMessage, "execution_id": exec.ID})
		return string(b)
	}
	nodes := append([]workflow.WorkflowExecutionNode(nil), exec.Nodes...)
	sort.SliceStable(nodes, func(i, k int) bool {
		a, b := nodes[i].FinishedAt, nodes[k].FinishedAt
		if a == nil || b == nil {
			return a != nil
		}
		return a.Before(*b)
	})
	var pick *workflow.WorkflowExecutionNode
	want := strings.TrimPrefix(mode, "node:")
	for i := range nodes {
		n := &nodes[i]
		if n.Status != "SUCCESS" {
			continue
		}
		if strings.HasPrefix(mode, "node:") {
			if n.NodeName == want {
				pick = n
			}
			continue
		}
		pick = n // last finished successful node
	}
	out := map[string]interface{}{"status": "success", "execution_id": exec.ID}
	if pick != nil {
		items := make([]interface{}, 0, len(pick.OutputItems))
		for _, it := range pick.OutputItems {
			items = append(items, workflow.RedactItemJSON(it).JSON)
		}
		out["node"] = pick.NodeName
		out["items"] = items
	}
	b, _ := json.Marshal(out)
	if len(b) > maxReplyBytes {
		b, _ = json.Marshal(map[string]interface{}{
			"status": "success", "execution_id": exec.ID, "node": out["node"], "truncated": true,
			"preview": string(b[:maxReplyBytes-512]),
		})
	}
	return string(b)
}
