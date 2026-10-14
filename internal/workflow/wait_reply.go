package workflow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/tracesig"
	"github.com/monoes/mono-agent/internal/vault"
)

// Kinds of reply a node can wait for (WaitReplySpec.Kind). The first three
// are n8n's sendAndWait response types; WaitReplyWebhook is core.wait's
// resume "webhook": any call to the URL is the reply.
const (
	WaitReplyApproval   = "approval"
	WaitReplyFreeText   = "freeText"
	WaitReplyCustomForm = "customForm"
	WaitReplyWebhook    = "webhook"
)

// Outcomes of a reply (WaitReplyResult.Status).
const (
	WaitReplyPending  = "pending"
	WaitReplyAnswered = "answered"
	WaitReplyExpired  = "expired"
)

// waitReplyPoll is how long a waiting run sleeps before the resume loop
// re-runs its node anyway. The answer wakes it at once; the poll only
// catches a wake that was lost (the answering process stopped first).
const waitReplyPoll = time.Hour

// WaitReplySpec describes the reply a node waits for.
type WaitReplySpec struct {
	Kind string `json:"kind"`
	// ApproveLabel and DisapproveLabel are the approval buttons' captions
	// ("" = Approve / Disapprove). ApprovalType "double" offers both buttons;
	// "single" (or "") only Approve.
	ApproveLabel    string `json:"approve_label,omitempty"`
	DisapproveLabel string `json:"disapprove_label,omitempty"`
	ApprovalType    string `json:"approval_type,omitempty"`
	// FormFields are customForm's fields; freeText asks one "Response".
	FormFields []WaitReplyField `json:"form_fields,omitempty"`
	// Title and Message head the page the links open; ButtonLabel is the
	// form's submit button ("" = Submit).
	Title       string `json:"title,omitempty"`
	Message     string `json:"message,omitempty"`
	ButtonLabel string `json:"button_label,omitempty"`
	// LinkLabel captions a form's link in the message ("" = Respond).
	LinkLabel string `json:"link_label,omitempty"`
	// Limit, when set, is when the wait gives up: the reply then reports
	// WaitReplyExpired. Zero waits until someone answers.
	Limit time.Time `json:"limit,omitempty"`
	// HTTPMethod restricts a webhook reply to one method ("" = any).
	HTTPMethod string `json:"http_method,omitempty"`
	// ExecutionURL makes the reply answer the execution's resume URL
	// ($execution.resumeUrl) instead of a URL of its own, as n8n's Wait node
	// does. One such reply should be pending per execution at a time.
	ExecutionURL bool `json:"execution_url,omitempty"`
}

// WaitReplyField is one field of a reply form.
type WaitReplyField struct {
	Label string `json:"label"`
	// Name is the answer's key ("" = Label).
	Name string `json:"name,omitempty"`
	// Type is text (""), textarea, number, email, password, date, dropdown,
	// radio or checkbox; the last three pick from Options.
	Type        string   `json:"type,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
	// Multiple lets a dropdown pick several options.
	Multiple bool `json:"multiple,omitempty"`
}

// Key is the field's key in the answer.
func (f WaitReplyField) Key() string {
	if f.Name != "" {
		return f.Name
	}
	return f.Label
}

// WaitReply is a reply a node waits for: the links to put in its message.
type WaitReply struct {
	Token string
	// URL opens the reply page (the form, or both approval buttons).
	URL string
	// ApproveURL and DisapproveURL are an approval's one-button links
	// (DisapproveURL only for ApprovalType "double"); FormURL is a form's.
	ApproveURL, DisapproveURL, FormURL string
	// Links are the links to show in the message, in order, with their
	// captions: Disapprove then Approve, or the form's one link.
	Links []ReplyLink
	Limit time.Time
}

// ReplyLink is one captioned link of a reply.
type ReplyLink struct {
	Label, URL string
	// Primary marks the main action (Approve, Respond) for styling.
	Primary bool
}

// Pause is the error a node returns to wait for this reply: the run waits
// (WAITING, no slot held) until the answer wakes it or the limit passes.
// The reason (shown with the waiting run) names the reply URL.
func (r WaitReply) Pause() error {
	d := waitReplyPoll
	if !r.Limit.IsZero() {
		if left := time.Until(r.Limit); left < d {
			d = max(left, time.Second)
		}
	}
	return PauseFor(d, "waiting for a reply at "+r.URL)
}

// WaitReplyResult is the state of a node's open reply.
type WaitReplyResult struct {
	WaitReply
	Kind   string
	Status string // WaitReplyPending, WaitReplyAnswered or WaitReplyExpired
	// Sent reports MarkWaitReplySent was called for this reply.
	Sent bool
	// Answer is the reply: {approved}, {text}, the form's fields (all with
	// respondedAt), or for a webhook the request as a trigger.webhook item.
	Answer map[string]interface{}
}

// Items is the answer as n8n's sendAndWait outputs it: one item
// {data: answer}; a webhook reply is the request item itself.
func (r *WaitReplyResult) Items() []Item {
	if r.Kind == WaitReplyWebhook {
		return []Item{{JSON: r.Answer}}
	}
	return []Item{{JSON: map[string]interface{}{"data": r.Answer}}}
}

// replyKey signs reply tokens (a variable so tests need no key file). The
// machine's trace key is shared by every monoagent process, so a token one
// process made, another verifies.
var replyKey = tracesig.Key

// signReply derives an unguessable token from parts.
func signReply(parts ...string) (string, error) {
	key, err := replyKey()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("monoagent-reply|" + strings.Join(parts, "|")))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

// ExecutionResumeURL is executionID's resume URL ($execution.resumeUrl):
// a call to it answers the execution's pending core.wait webhook or form.
func ExecutionResumeURL(executionID string) (string, error) {
	tok, err := signReply("execution", executionID)
	if err != nil {
		return "", err
	}
	return replyURL(tok), nil
}

func replyURL(token string) string { return PublicBaseURL() + "/reply/" + token }

func replyOf(token, kind string, spec WaitReplySpec) WaitReply {
	r := WaitReply{Token: token, URL: replyURL(token), Limit: spec.Limit}
	switch kind {
	case WaitReplyApproval:
		r.ApproveURL = r.URL + "/approve"
		if spec.ApprovalType == "double" {
			r.DisapproveURL = r.URL + "/decline"
			r.Links = append(r.Links, ReplyLink{Label: orDefault(spec.DisapproveLabel, "Disapprove"), URL: r.DisapproveURL})
		}
		r.Links = append(r.Links, ReplyLink{Label: orDefault(spec.ApproveLabel, "Approve"), URL: r.ApproveURL, Primary: true})
	case WaitReplyFreeText, WaitReplyCustomForm:
		r.FormURL = r.URL
		r.Links = []ReplyLink{{Label: orDefault(spec.LinkLabel, "Respond"), URL: r.FormURL, Primary: true}}
	}
	return r
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func replyDB(ctx context.Context) (*sql.DB, error) {
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, errors.New("wait reply: database not available in context")
	}
	return db, nil
}

// NewWaitReply opens a reply for this run of the node and returns its
// links. The node puts them in its message, calls MarkWaitReplySent once
// the message is out, and returns reply.Pause(). Calling it again before the
// run has read the outcome (WaitReplyAnswer) returns the same reply.
func NewWaitReply(ctx context.Context, input NodeInput, spec WaitReplySpec) (WaitReply, error) {
	switch spec.Kind {
	case WaitReplyApproval, WaitReplyFreeText, WaitReplyWebhook:
	case WaitReplyCustomForm:
		if len(spec.FormFields) == 0 {
			return WaitReply{}, fmt.Errorf("%w: wait reply: a custom form needs at least one field", ErrInvalidConfig)
		}
	default:
		return WaitReply{}, fmt.Errorf("%w: wait reply: kind must be approval, freeText, customForm or webhook, got %q", ErrInvalidConfig, spec.Kind)
	}
	if input.ExecutionID == "" || input.NodeID == "" {
		return WaitReply{}, errors.New("wait reply: a reply needs a running execution")
	}
	db, err := replyDB(ctx)
	if err != nil {
		return WaitReply{}, err
	}
	if open, err := openReply(ctx, db, input); err != nil || open != nil {
		if open != nil {
			return open.WaitReply, nil
		}
		return WaitReply{}, err
	}
	var run int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(run_index)+1, 0) FROM wait_replies WHERE execution_id=? AND node_id=?`,
		input.ExecutionID, input.NodeID).Scan(&run); err != nil {
		return WaitReply{}, fmt.Errorf("wait reply: %w", err)
	}
	var token string
	if spec.ExecutionURL {
		token, err = signReply("execution", input.ExecutionID)
	} else {
		token, err = signReply("node", input.ExecutionID, input.NodeID, fmt.Sprint(run))
	}
	if err != nil {
		return WaitReply{}, fmt.Errorf("wait reply: %w", err)
	}
	specJSON, _ := json.Marshal(spec)
	var expires interface{}
	if !spec.Limit.IsZero() {
		expires = formatResumeAfter(spec.Limit)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ExecutionID, input.NodeID, run, input.WorkflowID, token, spec.Kind, string(specJSON), expires); err != nil {
		return WaitReply{}, fmt.Errorf("wait reply: store: %w", err)
	}
	return replyOf(token, spec.Kind, spec), nil
}

// MarkWaitReplySent records that the node's open reply was delivered, so a
// re-run before the answer (a crash, a retry) does not send it again.
func MarkWaitReplySent(ctx context.Context, input NodeInput) error {
	db, err := replyDB(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`UPDATE wait_replies SET sent=1 WHERE execution_id=? AND node_id=? AND consumed=0`,
		input.ExecutionID, input.NodeID)
	return err
}

// DiscardWaitReply drops the node's open reply (its message could not be
// sent), so the node's next run opens a new one. Its links stop working.
func DiscardWaitReply(ctx context.Context, input NodeInput) error {
	db, err := replyDB(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`UPDATE wait_replies SET status='expired', consumed=1 WHERE execution_id=? AND node_id=? AND consumed=0`,
		input.ExecutionID, input.NodeID)
	return err
}

// WaitReplyAnswer returns the node's open reply, or nil when it has none
// (its first run, or the previous reply was already read). An answered or
// expired reply is returned once: reading it closes it, so the node's next
// run (a loop's next iteration) opens a new one.
func WaitReplyAnswer(ctx context.Context, input NodeInput) (*WaitReplyResult, error) {
	db, err := replyDB(ctx)
	if err != nil {
		return nil, err
	}
	res, err := openReply(ctx, db, input)
	if err != nil || res == nil {
		return nil, err
	}
	if res.Status == WaitReplyPending && !res.Limit.IsZero() && !time.Now().Before(res.Limit) {
		if _, err := db.ExecContext(ctx,
			`UPDATE wait_replies SET status='expired' WHERE execution_id=? AND node_id=? AND consumed=0 AND status='pending'`,
			input.ExecutionID, input.NodeID); err != nil {
			return nil, fmt.Errorf("wait reply: %w", err)
		}
		// An answer may have landed between the read and the update.
		if res, err = openReply(ctx, db, input); err != nil || res == nil {
			return nil, err
		}
	}
	if res.Status != WaitReplyPending {
		if _, err := db.ExecContext(ctx,
			`UPDATE wait_replies SET consumed=1 WHERE execution_id=? AND node_id=? AND consumed=0`,
			input.ExecutionID, input.NodeID); err != nil {
			return nil, fmt.Errorf("wait reply: %w", err)
		}
	}
	return res, nil
}

// openReply reads the node's unconsumed reply (nil when none).
func openReply(ctx context.Context, db *sql.DB, input NodeInput) (*WaitReplyResult, error) {
	var token, kind, specJSON, status, answer string
	var sent int
	err := db.QueryRowContext(ctx,
		`SELECT token, kind, spec, status, answer, sent FROM wait_replies
		 WHERE execution_id=? AND node_id=? AND consumed=0 ORDER BY run_index DESC LIMIT 1`,
		input.ExecutionID, input.NodeID).Scan(&token, &kind, &specJSON, &status, &answer, &sent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wait reply: load: %w", err)
	}
	var spec WaitReplySpec
	_ = json.Unmarshal([]byte(specJSON), &spec)
	res := &WaitReplyResult{WaitReply: replyOf(token, kind, spec), Kind: kind, Status: status, Sent: sent == 1}
	if answer != "" {
		_ = json.Unmarshal([]byte(answer), &res.Answer)
	}
	return res, nil
}

// ExecutionData is $execution with its resume URLs, for nodes that bind it
// themselves (core.code).
func ExecutionData(id string) map[string]interface{} { return executionData(id, true) }

// executionData is $execution: its id and, when withURLs, the resume URL
// n8n templates reach as $execution.resumeUrl / resumeFormUrl (one URL
// serves both a core.wait webhook and form).
func executionData(id string, withURLs bool) map[string]interface{} {
	m := map[string]interface{}{"id": id}
	if withURLs && id != "" {
		if u, err := ExecutionResumeURL(id); err == nil {
			m["resumeUrl"], m["resumeFormUrl"] = u, u
		}
	}
	return m
}
