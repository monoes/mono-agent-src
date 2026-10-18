// Package hilsend makes every outbound action an agent starts wait for a
// person's approval. An email, DM, comment, post, connection request or
// publish started from an agent context (a coder chat lead or worker, an org
// role) is prepared but not performed: it is stored as a pending Human-in-Loop
// item of kind "send" and the node returns "queued for approval". The operator
// approves (optionally editing the text) or rejects it; approving runs the
// original node with the exact previewed payload and records the publication.
//
// Enforcement lives here, in the node layer, not in any prompt: the workflow
// engine and `node run` consult Guard before a node runs.
package hilsend

import "strconv"

// State is a send item's lifecycle position.
type State string

const (
	StatePending  State = "pending"  // waiting for the operator
	StateApproved State = "approved" // approved, being performed right now
	StateRejected State = "rejected" // the operator said no; never performed
	StateSent     State = "sent"     // performed and registered
	StateFailed   State = "failed"   // approved, but the node failed (Error says why)
)

// Action is what the item would do.
const (
	ActionEmail   = "email"
	ActionDM      = "dm"
	ActionComment = "comment"
	ActionPost    = "post"
	ActionConnect = "connect"
	ActionPublish = "publish"
)

// Payload is the approval card's contract (`hil list --json` carries it as
// item.send, `hil show <id>` returns it alone). JSON example:
//
//	{"kind":"send","id":"7c1e…","state":"pending",
//	 "preparedBy":{"role":"outreach","agentId":"coder-lead","conversationId":"c42"},
//	 "action":"dm","channel":"instagram",
//	 "account":{"platform":"instagram","handle":"acme"},
//	 "recipient":{"personId":"p9","name":"Ada","profileUrl":"https://www.instagram.com/ada/"},
//	 "text":"Hi Ada …","subject":"",
//	 "attachments":[{"kind":"image","name":"cover.png","path":"/…/cover.png","size":12034,"mime":"image/png"}],
//	 "createdAt":"2026-10-13T10:00:00Z","nodeType":"instagram.send_dms"}
//
// Text and Subject are the CURRENT (possibly edited) values: they are exactly
// what approving performs. Attachments are read for preview with
// `hil attachment <id> <n>`; the recipient card with `hil recipient <id>`.
type Payload struct {
	Kind        string       `json:"kind"` // always "send"
	ID          string       `json:"id"`
	State       State        `json:"state"`
	PreparedBy  PreparedBy   `json:"preparedBy"`
	Action      string       `json:"action"`  // email|dm|comment|post|connect|publish
	Channel     string       `json:"channel"` // email, instagram, slack, …
	Account     Account      `json:"account"`
	Recipient   Recipient    `json:"recipient"`
	Text        string       `json:"text"`
	Subject     string       `json:"subject,omitempty"`
	Attachments []Attachment `json:"attachments"`
	CreatedAt   string       `json:"createdAt"`

	// TextReadOnly: the op names no text field, so the text (if any) says
	// what the step sends and can't be edited (ErrNoText).
	TextReadOnly bool `json:"textReadOnly,omitempty"`
	// NodeType is the node that performs the send on approval.
	NodeType string `json:"nodeType"`
	// Error is why a failed item failed.
	Error string `json:"error,omitempty"`
	// Result is the node's output once sent.
	Result any `json:"result,omitempty"`
	// ResolvedAt is when the operator approved or rejected it.
	ResolvedAt string `json:"resolvedAt,omitempty"`
	// AlwaysAllowed is set on a resolve response when the operator also
	// allowed this destination for good.
	AlwaysAllowed bool `json:"alwaysAllowed,omitempty"`
}

// PreparedBy names who prepared the send. Org is set for an org role,
// ConversationID for a coder chat.
//
// An agent's process names itself (Detect reads its environment), so
// whatever it says is a claim; Label shows it as one. System marks the one
// kind MonoAgent derives itself, a run of a workflow the user never
// approved (unapprovedRun, and browser.jev's aiCaller for the browser
// cards); nothing in an environment sets it.
type PreparedBy struct {
	Role           string `json:"role,omitempty"`
	AgentID        string `json:"agentId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	Org            string `json:"org,omitempty"`
	// Workflow is the workflow whose run prepared the send, when that run is
	// why it waits (UnapprovedWorkflowAgent).
	Workflow string `json:"workflow,omitempty"`
	System   bool   `json:"system,omitempty"`
}

// Label is the "prepared by" the user reads: MonoAgent's own label as it
// is ("unapproved workflow wf1"), an agent's as what it claims to be
// (`an agent (claims: "outreach")`, quoted so it can't pass for more), or
// "" when there is nothing to show.
func (p PreparedBy) Label() string {
	if p.System && p.Role != "" {
		return p.Role
	}
	claim := p.Role
	if claim == "" {
		claim = p.AgentID
	}
	if claim == "" {
		return ""
	}
	return "an agent (claims: " + strconv.Quote(claim) + ")"
}

// Account is the connected account the send goes out from.
type Account struct {
	Platform string `json:"platform"`
	Handle   string `json:"handle,omitempty"`
}

// Recipient is who receives it. PersonID is set when the people table knows
// them; the card then shows the person's profile. Label: Name describes the
// recipient ("the ticket's requester", "the client of invoice inv9") and is
// no address, so no always-allow rule and no People row is keyed on it.
type Recipient struct {
	PersonID   string `json:"personId,omitempty"`
	Name       string `json:"name"`
	ProfileURL string `json:"profileUrl,omitempty"`
	Label      bool   `json:"label,omitempty"`
}

// Attachment is one previewable item. Kind is image, file or link.
type Attachment struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	Size int64  `json:"size,omitempty"`
	Mime string `json:"mime,omitempty"`
}
