package hilsend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Environment variables.
//
// MONOAGENT_AGENT_CONTEXT marks a process as an agent's: any non-empty value
// (the coder runtime sets it to "coder", an org role's runner to "org") makes
// every outbound node it runs wait for approval. The agent markers monomind
// and the coding agents already set (orgsign.AgentContextMarkers) and
// MONOAGENT_ACTOR count too, so a role that never heard of this variable is
// still gated. MONOAGENT_CONVERSATION_ID, MONOAGENT_AGENT_ROLE and
// MONOAGENT_AGENT_ID only fill the approval card's "prepared by".
//
// MONOAGENT_SEND_APPROVAL_ID and _PROOF are set by `hil resolve` on the one
// process that performs an approved send; nothing else should set them.
const (
	EnvAgentContext  = "MONOAGENT_AGENT_CONTEXT"
	EnvConversation  = "MONOAGENT_CONVERSATION_ID"
	EnvAgentRole     = "MONOAGENT_AGENT_ROLE"
	EnvAgentID       = "MONOAGENT_AGENT_ID"
	EnvApprovalID    = "MONOAGENT_SEND_APPROVAL_ID"
	EnvApprovalProof = "MONOAGENT_SEND_APPROVAL_PROOF"
)

// SettingKey is coder mode's `sendApproval` setting in the configs row
// "coder_mode" (cmd/monoagentcli/coder.go). Absent means on.
const (
	settingsRow = "coder_mode"
	SettingKey  = "sendApproval"
)

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := env(k); v != "" {
			return v
		}
	}
	return ""
}

// Detect reports whether this process is an agent's and who it is.
func Detect() (PreparedBy, bool) {
	if env(EnvAgentContext) == "" && orgsign.AgentContextMarker() == "" && env("MONOAGENT_ACTOR") == "" {
		return PreparedBy{}, false
	}
	return PreparedBy{
		Role:           firstEnv(EnvAgentRole, "MONOMIND_ORG_ROLE"),
		AgentID:        firstEnv(EnvAgentID, "MONOAGENT_ACTOR"),
		ConversationID: env(EnvConversation),
		Org:            env("MONOMIND_ORG_NAME"),
	}, true
}

// Enabled reads the sendApproval setting; on unless the operator turned it off.
func Enabled(db *sql.DB) bool {
	var raw string
	if err := db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, settingsRow).Scan(&raw); err != nil {
		return true
	}
	var s map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &s) != nil {
		return true
	}
	v, ok := s[SettingKey]
	if !ok {
		return true
	}
	var on bool
	if json.Unmarshal(v, &on) != nil {
		return true
	}
	return on
}

// UnapprovedWorkflowAgent is PreparedBy.AgentID on a send that a run of a
// workflow the user never approved prepared, or a run such a workflow called.
const UnapprovedWorkflowAgent = "unapproved-workflow"

// unapprovedRun reports whether ctx is a run of a workflow the user never
// approved (workflow.AIAuthoredFrom: the chat, an agent or a workflow wrote
// or changed it since the user last saved or activated it, and MCP's
// node_run), or a run such a workflow called, and names it for the approval
// card. Its sends wait for approval like an agent's, whoever started the
// run; a workflow the user approved sends as before, even when the chat
// starts it.
func unapprovedRun(ctx context.Context) (PreparedBy, bool) {
	wf, _ := vault.ExecIDsFromContext(ctx)
	if workflow.AIAuthoredFrom(ctx) {
		role := "an unapproved workflow step"
		if wf != "" {
			role = "unapproved workflow " + wf
		}
		return PreparedBy{AgentID: UnapprovedWorkflowAgent, Role: role, Workflow: wf, System: true}, true
	}
	if caller, ok := workflow.CalledByUnapproved(workflow.TriggerDataFrom(ctx)); ok {
		role := "a workflow an unapproved workflow called"
		switch {
		case wf != "" && caller != "":
			role = "workflow " + wf + ", called by unapproved workflow " + caller
		case caller != "":
			role = "a workflow unapproved workflow " + caller + " called"
		}
		return PreparedBy{AgentID: UnapprovedWorkflowAgent, Role: role, Workflow: wf, System: true}, true
	}
	return PreparedBy{}, false
}

// Guard is the workflow.ExecGuard that queues the sends of agents and of
// workflows the user never approved (unapprovedRun). It is installed by
// noderegistry.Build, so the engine and `node run` both pass through it.
func Guard(ctx context.Context, nodeType string, in workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, bool, error) {
	t, ok := classify(nodeType, config)
	if !ok {
		return nil, false, nil
	}
	who, agent := Detect()
	if !agent {
		if who, agent = unapprovedRun(ctx); !agent {
			return nil, false, nil
		}
	}
	db := vault.DBFromContext(ctx)
	if db == nil {
		// Fail closed: an agent's send never runs unchecked.
		return nil, true, fmt.Errorf("%s sends outward, and from an agent it must wait for approval, but there is no database to queue it in", nodeType)
	}
	profile, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		// Fail closed: an agent's send never runs under an unknown profile.
		return nil, true, fmt.Errorf("%s sends outward and ran without a profile: %w", nodeType, perr)
	}
	st := Store{DB: db, Profile: profile}
	if st.VerifyApproval(ctx, env(EnvApprovalID), env(EnvApprovalProof), nodeType) {
		return nil, false, nil
	}
	if !Enabled(db) {
		return nil, false, nil
	}
	prepared := prepare(st, nodeType, t, who, config, in)
	allowed := true
	for _, p := range prepared {
		// A label ("the ticket's requester") names no destination a rule
		// could stand for: every such send gets its card.
		allowed = allowed && !p.payload.Recipient.Label &&
			st.Allowed(ctx, p.payload.Action, p.payload.Account.Platform, p.payload.Account.Handle, keyOf(p.payload.Recipient))
	}
	if allowed && len(prepared) > 0 {
		return nil, false, nil
	}
	if inlineCredential(config) {
		// The card would ask the user to approve a send that can only fail.
		return nil, true, fmt.Errorf("%s sends outward, so here it waits for the operator's approval, but its credential is written into its config and an approval keeps no credentials: "+
			"save it as a connection (monoagentcli connect save) and set credential_id to it", nodeType)
	}
	items := make([]workflow.Item, 0, len(prepared))
	for _, p := range prepared {
		q, err := st.create(ctx, p.payload, p.replay)
		if err != nil {
			return nil, true, err
		}
		items = append(items, workflow.NewItem(map[string]interface{}{
			"success": false, "pending": true, "status": "queued for approval", "state": string(q.State),
			"hil_id": q.ID, "action": q.Action, "channel": q.Channel, "recipient": q.Recipient.Name,
			"message": "Prepared but NOT sent: it waits for the operator's approval in the Human in Loop queue. Do not retry or send it another way.",
		}))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, true, nil
}

type prepared struct {
	payload Payload
	replay  replay
}

// prepare turns one node call into the items it would send.
func prepare(st Store, nodeType string, t target, who PreparedBy, config map[string]interface{}, input workflow.NodeInput) []prepared {
	in := make([]map[string]interface{}, 0, len(input.Items))
	for _, it := range input.Items {
		in = append(in, it.JSON)
	}
	ones := split(t, config, in)
	if len(t.perItem) > 0 {
		ones = splitPerItem(t, config, input)
	}
	var out []prepared
	for _, o := range ones {
		textField, text := firstKey(o.config, t.text)
		if len(t.show) > 0 {
			// No message to show or edit: what the step would send that
			// its op doesn't declare.
			textField, text = "", undeclaredText(t.op, t.show, t.exprs, o.config)
		}
		subjectField, subject := firstKey(o.config, t.subject)
		rec := o.recipient
		if rec.PersonID == "" && !o.labelled {
			rec = st.person(t, nodeType, rec)
		}
		p := Payload{
			PreparedBy: who, Action: t.action, Channel: t.channelOf(nodeType), Account: t.accountOf(nodeType, o.config),
			Recipient: rec, Text: text, Subject: subject, Attachments: attachments(o.config), NodeType: nodeType,
		}
		out = append(out, prepared{payload: p, replay: replay{
			NodeType: nodeType, Config: stripSecrets(o.config), Input: o.input, TextField: textField, SubjectKey: subjectField,
		}})
	}
	return out
}

// person fills the recipient from the people table when it knows them.
func (s Store) person(t target, nodeType string, r Recipient) Recipient {
	if r.Name == "" && r.ProfileURL == "" {
		return r
	}
	platform := t.channelOf(nodeType)
	var id, name string
	var err error
	if t.action == ActionEmail {
		err = s.DB.QueryRow(`SELECT id, COALESCE(full_name,'') FROM people WHERE contact_details LIKE ? ESCAPE '\' LIMIT 1`,
			"%"+escapeLike(r.Name)+"%").Scan(&id, &name)
	} else {
		user := lastSegment(firstNonEmpty(r.ProfileURL, r.Name))
		err = s.DB.QueryRow(`SELECT id, COALESCE(full_name,'') FROM people WHERE LOWER(platform) = LOWER(?) AND LOWER(platform_username) = LOWER(?) LIMIT 1`,
			platform, user).Scan(&id, &name)
	}
	if err != nil {
		return r
	}
	r.PersonID = id
	if name != "" {
		r.Name = name
	}
	return r
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
