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

// heldByRunToken is one clause for a send held under a workflow run's mark: it shows no approved run token (the
// run is unapproved, an older engine started it, no token could be issued, or the child dropped it).
func heldByRunToken() string {
	if env(workflow.EnvWorkflowRun) == "" {
		return ""
	}
	return " (This command runs under a workflow run that shows no approved run token.)"
}

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
	// The card and its approved run name the type that runs the node, as
	// the engine asks with, whatever name `node run` was given.
	nodeType = workflow.CanonicalNodeType(nodeType)
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
	prepared := prepare(ctx, st, nodeType, t, who, config, in)
	allowed := true
	for _, p := range prepared {
		// A label ("the ticket's requester") names no destination a rule
		// could stand for, and a rule stands for one recipient only, never
		// for a cc, a bcc, a second address or a different reply-to: every
		// such send gets its card.
		allowed = allowed && !p.payload.Recipient.Label && p.sole &&
			st.Allowed(ctx, p.payload.Action, p.payload.Account.Platform, ruleAccount(p.payload.Account.Handle, p.replay.Connection), p.replay.Address)
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
			"message": "Prepared but NOT sent: it waits for the operator's approval in the Human in Loop queue. Do not retry or send it another way." + heldByRunToken(),
		}))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, true, nil
}

type prepared struct {
	payload Payload
	replay  replay
	sole    bool // it goes to its one recipient alone (soleRecipient)
}

// prepare turns one node call into the items it would send.
func prepare(ctx context.Context, st Store, nodeType string, t target, who PreparedBy, config map[string]interface{}, input workflow.NodeInput) []prepared {
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
		unreviewable := ""
		if len(t.show) > 0 {
			// No message to show or edit: what the step would send, read-only
			// (keys its op doesn't declare, or the params its annotation
			// shows).
			textField = ""
			text, unreviewable = showCard(t.op, t.show, t.exprs, t.unsent, t.undeclared, o.config)
		}
		if unreviewable == "" && sendsDraft(t, o.config) {
			unreviewable = draftUnreviewable
		}
		subjectField, subject := firstKey(o.config, t.subject)
		rec := o.recipient
		// A rule matches the recipient as the send addresses it, never the
		// person a lookup found for it (keyOf would prefer the person's id
		// once People knows them, and an email lookup matches by substring).
		addr := keyOf(rec)
		if rec.PersonID == "" && !o.labelled {
			rec = st.person(t, nodeType, rec)
		}
		p := Payload{
			PreparedBy: who, Action: t.action, Channel: t.channelOf(nodeType), Account: t.accountOf(nodeType, o.config),
			Recipient: rec, Text: text, Subject: subject, Attachments: attachments(o.config), NodeType: nodeType,
			Details: detailsOf(ctx, st, t, o.config), Unreviewable: unreviewable,
			Host: hostOf(nodeType, o.config), FetchURL: fetchURLOf(nodeType, o.config),
		}
		saved, connection := savedConnection(ctx, st, nodeType, o.config), ""
		if saved != nil {
			connection = saved.ID
		}
		out = append(out, prepared{payload: p, sole: soleRecipient(saved, t, nodeType, o.config), replay: replay{
			NodeType: nodeType, Config: stripSecrets(o.config), Input: o.input, TextField: textField, SubjectKey: subjectField, Address: addr,
			Connection: connection,
		}})
	}
	return out
}

// person tags the recipient with the People row it is, when there is one:
// the card keeps the addressed value as its name and shows the person
// beside it (PersonName), so a card never names someone the send doesn't
// address. An email matches a contact's address whole (case aside), never
// a part of one: da@example.test is not ada@example.test, and ceo@acme.co
// is not ceo@acme.co.uk.
func (s Store) person(t target, nodeType string, r Recipient) Recipient {
	if r.Name == "" && r.ProfileURL == "" {
		return r
	}
	var id, name string
	if t.action == ActionEmail {
		id, name = s.personByEmail(r.Name)
	} else {
		user := lastSegment(firstNonEmpty(r.ProfileURL, r.Name))
		_ = s.DB.QueryRow(`SELECT id, COALESCE(full_name,'') FROM people WHERE LOWER(platform) = LOWER(?) AND LOWER(platform_username) = LOWER(?) LIMIT 1`,
			t.channelOf(nodeType), user).Scan(&id, &name)
	}
	if id != "" {
		r.PersonID, r.PersonName = id, name
	}
	return r
}

// personByEmail is the People row whose contact details hold the address
// in to whole, or "".
func (s Store) personByEmail(to string) (id, name string) {
	addr := addressRE.FindString(to)
	if addr == "" || !strings.Contains(addr, "@") {
		return "", ""
	}
	rows, err := s.DB.Query(`SELECT id, COALESCE(full_name,''), COALESCE(contact_details,'') FROM people WHERE contact_details LIKE ? ESCAPE '\' LIMIT 50`,
		"%"+escapeLike(addr)+"%")
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	for rows.Next() {
		var pid, pname, details string
		if rows.Scan(&pid, &pname, &details) != nil {
			continue
		}
		for _, a := range addressRE.FindAllString(details, -1) {
			if strings.EqualFold(a, addr) {
				return pid, pname
			}
		}
	}
	return "", ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
