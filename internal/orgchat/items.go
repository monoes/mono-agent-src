// Package orgchat is chatting with a running org's boss (monoes/mono-agent#229):
// the boss thread built from the org's bus log and its human items, and
// resolving those items from the chat safely — idempotent, and refused once
// the org has stopped.
package orgchat

import (
	"encoding/json"
	"fmt"
)

// Kinds of human item, as `org questions|approvals|gates` list them.
const (
	KindQuestion = "question"
	KindApproval = "approval"
	KindGate     = "gate"
)

// Resolution states an item ends in.
const (
	StateAnswered = "answered"
	StateApproved = "approved"
	StateDenied   = "denied"
	StateRejected = "rejected"
	// StateDismissed: the operator closed a question without answering
	// (`org questions dismiss`, monomind 2.21+).
	StateDismissed = "dismissed"
)

// Question is one ask_human question (`org questions <org> --all`).
type Question struct {
	QuestionID string  `json:"questionId"`
	Role       string  `json:"role"`
	Question   string  `json:"question"`
	TS         int64   `json:"ts"`
	Answer     *string `json:"answer"`
	State      string  `json:"state,omitempty"`
	Blocking   *bool   `json:"blocking,omitempty"`
}

// Approval is one tool approval request (`org approvals <org> --all`).
type Approval struct {
	RoleID     string  `json:"roleId"`
	Action     string  `json:"action"`
	Question   string  `json:"question"`
	TS         int64   `json:"ts"`
	Approved   *bool   `json:"approved"`
	RequestID  *string `json:"requestId"`
	ResolvedBy *string `json:"resolvedBy"`
	// Input is the call's arguments, redacted and cut by monomind.
	Input map[string]interface{} `json:"input"`
}

// Gate is one decision gate (`org gates <org> --all`).
type Gate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	RoleID      string `json:"roleId"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"createdAt"`
	ResolvedBy  string `json:"resolvedBy,omitempty"`
}

// HumanItems is everything an org asked a person, resolved or not.
type HumanItems struct {
	Questions []Question
	Approvals []Approval
	Gates     []Gate
}

func parseList[T any](raw []byte, what string) ([]T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var p struct {
		Items []T `json:"items"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("orgchat: %s: %w", what, err)
	}
	return p.Items, nil
}

// ParseQuestions, ParseApprovals and ParseGates read the `--all` listings.
func ParseQuestions(raw []byte) ([]Question, error) { return parseList[Question](raw, "questions") }
func ParseApprovals(raw []byte) ([]Approval, error) { return parseList[Approval](raw, "approvals") }
func ParseGates(raw []byte) ([]Gate, error)         { return parseList[Gate](raw, "gates") }

// approvalRef names an approval the way the chat addresses it: its request
// id (monomind M5), else role:action:ts.
func approvalRef(a Approval) string {
	if a.RequestID != nil && *a.RequestID != "" {
		return *a.RequestID
	}
	return fmt.Sprintf("%s:%s:%d", a.RoleID, a.Action, a.TS)
}

func approvalState(a Approval) string {
	switch {
	case a.Approved == nil:
		return ""
	case *a.Approved:
		return StateApproved
	default:
		return StateDenied
	}
}

func gateState(g Gate) string {
	switch g.Status {
	case "pending", "":
		return ""
	case "approved":
		return StateApproved
	default:
		return StateRejected
	}
}

// findApproval picks the approval ref names: an exact request id or
// role:action:ts, else (a bare role:action) the pending one for that pair,
// else its latest.
func findApproval(list []Approval, ref string) (Approval, bool) {
	for _, a := range list {
		if approvalRef(a) == ref {
			return a, true
		}
	}
	var latest *Approval
	for i := range list {
		a := list[i]
		if a.RoleID+":"+a.Action != ref {
			continue
		}
		if a.Approved == nil {
			return a, true
		}
		if latest == nil || a.TS > latest.TS {
			latest = &list[i]
		}
	}
	if latest != nil {
		return *latest, true
	}
	return Approval{}, false
}
