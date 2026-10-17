package noderegistry

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func sendsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "sends.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func sendsCtx(db *sql.DB) context.Context {
	return vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
}

func asOperator(t *testing.T) {
	t.Helper()
	for _, k := range append(operator.MarkerEnv(), hilsend.EnvAgentRole, hilsend.EnvAgentID, hilsend.EnvConversation) {
		t.Setenv(k, "")
	}
}

// Every op an API package annotates as a send waits for approval when an
// agent runs it, through the guard Build installs; every "sends": false op
// goes through (#832).
func TestEveryAnnotatedSendQueues(t *testing.T) {
	asOperator(t)
	t.Setenv(hilsend.EnvAgentContext, "coder")
	Build(nil)
	db := sendsDB(t)
	st := hilsend.Store{DB: db, Profile: "default"}
	pkgs, err := apipkg.Load()
	if err != nil {
		t.Fatal(err)
	}
	queued, passed := 0, 0
	for _, p := range pkgs {
		nodeType := p.NodeTypeOf()
		if p.Extends != "" {
			nodeType = p.Extends
		}
		for _, o := range p.Operations {
			if o.Sends == nil {
				continue
			}
			key := p.ID + " " + o.Resource + "/" + o.Operation
			config := map[string]interface{}{"resource": o.Resource, "operation": o.Operation}
			if o.Sends.None {
				if _, handled, _ := hilsend.Guard(sendsCtx(db), nodeType, workflow.NodeInput{}, config); handled {
					t.Errorf("%s says sends:false (%s) but was held", key, o.SendsReason)
				}
				passed++
				continue
			}
			s := o.Sends
			if len(s.If) > 0 {
				// "param=value" is set to its value, a bare param to true;
				// "param!=value" sends while the param is unset, and "param
				// not in a|b" once it is set to something else.
				if name, _, ok := strings.Cut(s.If[0], " not in "); ok {
					setParam(config, name, "other")
				} else if name, value, eq := strings.Cut(s.If[0], "="); eq {
					if !strings.HasSuffix(name, "!") {
						setParam(config, name, value)
					}
				} else {
					setParam(config, name, true)
				}
			}
			to := s.ToUnset // the card's recipient
			if len(s.To) > 0 {
				setParam(config, s.To[0], "ada@example.test")
				to = strings.TrimSpace(s.ToLabel[s.To[0]] + " ada@example.test")
			}
			if len(s.Text) > 0 {
				setParam(config, s.Text[0], "Hi Ada")
			}
			out, handled, err := hilsend.Guard(sendsCtx(db), nodeType, workflow.NodeInput{}, config)
			if !handled || err != nil {
				t.Errorf("%s (%s): handled=%v err=%v, want it queued", key, nodeType, handled, err)
				continue
			}
			item, err := st.Get(context.Background(), out[0].Items[0].JSON["hil_id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if item.Action != s.Action {
				t.Errorf("%s: queued as %q, annotated %q", key, item.Action, s.Action)
			}
			if p.Extends == "" && (item.Channel != p.ID || item.Recipient.Name != to ||
				(len(s.Text) > 0 && item.Text != "Hi Ada")) {
				t.Errorf("%s: card %+v", key, item)
			}
			queued++
		}
	}
	if queued < 150 || passed < 30 {
		t.Errorf("queued %d annotated sends and let %d sends:false ops through: the catalog lost its annotations?", queued, passed)
	}
}

// setParam sets a sends param: for "a.b", option b of collection a.
func setParam(config map[string]interface{}, name string, v interface{}) {
	head, rest, dotted := strings.Cut(name, ".")
	if !dotted {
		config[name] = v
		return
	}
	sub, _ := config[head].(map[string]interface{})
	if sub == nil {
		sub = map[string]interface{}{}
		config[head] = sub
	}
	setParam(sub, rest, v)
}

// A workflow the user never approved sends through an app node no more than
// an agent does; the user's own process does. An app node resolves its
// recipient per item, so each item is a card of its own (#832).
func TestAppSendsOfAnUnapprovedWorkflowQueuePerItem(t *testing.T) {
	asOperator(t)
	Build(nil)
	db := sendsDB(t)
	config := map[string]interface{}{"operation": "send", "fromEmail": "me@example.test", "toEmail": "{{ $json.email }}",
		"subject": "Hello", "text": "Hi {{ $json.name }}"}
	in := workflow.NodeInput{Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"email": "ada@example.test", "name": "Ada"}),
		workflow.NewItem(map[string]interface{}{"email": "bob@example.test", "name": "Bob"}),
	}}
	if _, handled, err := hilsend.Guard(sendsCtx(db), "app.mailgun", in, config); handled || err != nil {
		t.Fatalf("the user's own run: handled=%v err=%v", handled, err)
	}
	ctx := vault.ContextWithExecIDs(workflow.WithAIAuthored(sendsCtx(db)), "wf1", "ex1")
	out, handled, err := hilsend.Guard(ctx, "app.mailgun", in, config)
	if !handled || err != nil || len(out[0].Items) != 2 {
		t.Fatalf("an unapproved workflow: handled=%v err=%v out=%+v", handled, err, out)
	}
	st := hilsend.Store{DB: db, Profile: "default"}
	var got []string
	for _, it := range out[0].Items {
		p, err := st.Get(context.Background(), it.JSON["hil_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, p.Recipient.Name+": "+p.Text+" ("+p.PreparedBy.Role+")")
	}
	if want := "ada@example.test: Hi Ada (unapproved workflow wf1)|bob@example.test: Hi Bob (unapproved workflow wf1)"; strings.Join(got, "|") != want {
		t.Fatalf("cards = %v, want %s", got, want)
	}
}

// An op that sends only with some param waits only when one is set: a
// calendar event that invites no one goes through, one inviting Ada waits
// with her on the card, and so does an invoice that is emailed as it is
// created.
func TestAConditionalSendWaitsOnlyWhenItSends(t *testing.T) {
	asOperator(t)
	t.Setenv(hilsend.EnvAgentContext, "coder")
	Build(nil)
	db := sendsDB(t)
	held := func(nodeType string, config map[string]interface{}) (string, bool) {
		t.Helper()
		out, handled, err := hilsend.Guard(sendsCtx(db), nodeType, workflow.NodeInput{}, config)
		if err != nil {
			t.Fatal(err)
		}
		if !handled {
			return "", false
		}
		p, err := (hilsend.Store{DB: db, Profile: "default"}).Get(context.Background(), out[0].Items[0].JSON["hil_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		return p.Recipient.Name, true
	}
	event := map[string]interface{}{"resource": "event", "operation": "create", "calendar": "primary",
		"additionalFields": map[string]interface{}{"summary": "Focus time"}}
	if _, ok := held("app.google_calendar", event); ok {
		t.Error("an event that invites no one was held")
	}
	event["additionalFields"].(map[string]interface{})["attendees"] = "ada@example.test"
	if to, ok := held("app.google_calendar", event); !ok || to != "ada@example.test" {
		t.Errorf("an event inviting Ada: held=%v to=%q", ok, to)
	}
	invoice := map[string]interface{}{"resource": "invoice", "operation": "create", "apiVersion": "v5",
		"additionalFields": map[string]interface{}{"client": "c1", "emailInvoice": false}}
	if _, ok := held("app.invoice_ninja", invoice); ok {
		t.Error("an invoice created without emailing it was held")
	}
	invoice["additionalFields"].(map[string]interface{})["emailInvoice"] = true
	if _, ok := held("app.invoice_ninja", invoice); !ok {
		t.Error("an invoice emailed as it is created went out")
	}
}

// The sends the reviews decided stay decided: each waits, with the
// recipient its card shows, or goes through, as decided. Stripping an
// annotation the catalog lint can't see (Calendly's cancellation) fails
// here.
func TestTheReviewedSendsStayDecided(t *testing.T) {
	asOperator(t)
	t.Setenv(hilsend.EnvAgentContext, "coder")
	Build(nil)
	db := sendsDB(t)
	st := hilsend.Store{DB: db, Profile: "default"}
	type cfg = map[string]interface{}
	for _, c := range []struct {
		nodeType string
		config   cfg
		to       string // the card's recipient; "-" when it goes through
	}{
		{"app.calendly", cfg{"resource": "scheduledEvent", "operation": "cancel", "eventUuid": "ev1", "reason": "Ill"}, "ev1"},
		{"app.msg91", cfg{"resource": "sms", "operation": "send", "to": "+15550100", "message": "Hi"}, "+15550100"},
		{"app.invoice_ninja", cfg{"resource": "invoice", "operation": "email", "invoiceId": "inv9"}, "the client of invoice inv9"},
		// Creating or updating a ticket is a work item, by the user's
		// decision; a public reply or a note on one is a comment, and waits,
		// and so does a create or update that emails someone beyond the
		// ticket's requester (an email article, CCs, Zendesk's JSON or
		// recipient).
		{"app.zendesk", cfg{"resource": "ticket", "operation": "create", "description": "We fixed it"}, "-"},
		{"app.zendesk", cfg{"resource": "ticket", "operation": "create", "description": "Hi", "jsonParameters": true,
			"additionalFieldsJson": `{"email_ccs": [{"user_email": "bob@example.test"}]}`}, "the ticket's requester"},
		{"app.zendesk", cfg{"resource": "ticket", "operation": "create", "description": "Hi",
			"additionalFields": cfg{"recipient": "support@example.test"}}, "the ticket's requester"},
		{"app.zendesk", cfg{"resource": "ticket", "operation": "update", "id": "T1", "updateFields": cfg{"status": "solved"}}, "-"},
		{"app.zendesk", cfg{"resource": "ticket", "operation": "update", "id": "T1", "updateFields": cfg{"publicReply": "Fixed"}}, "T1"},
		// Assigning a ticket notifies a colleague, as assigning an issue
		// does: a work item, open.
		{"app.zendesk", cfg{"resource": "ticket", "operation": "update", "id": "T1", "updateFields": cfg{"assigneeEmail": "bob@example.test"}}, "-"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test"}, "-"},
		// A Zammad article goes out unless it is a note, web or phone one,
		// and one with a "to" goes out whatever its type.
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "note", "body": "Called her"}}}, "-"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "web", "body": "Called her"}}}, "-"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "phone", "body": "Called her"}}}, "-"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "email", "to": "bob@example.test", "body": "Fixed"}}}, "bob@example.test"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "sms", "to": "+15550001", "body": "Fixed"}}}, "+15550001"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "twitter status", "body": "Fixed"}}}, "ada@example.test"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "twitter direct-message", "body": "Fixed"}}}, "ada@example.test"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "facebook feed post", "body": "Fixed"}}}, "ada@example.test"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "telegram personal-message", "body": "Fixed"}}}, "ada@example.test"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "whatsapp message", "body": "Fixed"}}}, "ada@example.test"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"type": "note", "to": "bob@example.test", "body": "Fixed"}}}, "bob@example.test"},
		{"app.zammad", cfg{"resource": "ticket", "operation": "update", "id": "T2", "updateFields": cfg{"note": "Fixed"}}, "T2"},
		{"app.freshdesk", cfg{"resource": "ticket", "operation": "create", "requesterIdentificationValue": "ada@example.test"}, "-"},
		{"app.freshdesk", cfg{"resource": "ticket", "operation": "create", "requesterIdentificationValue": "ada@example.test",
			"options": cfg{"ccEmails": "bob@example.test"}}, "bob@example.test"},
		{"app.freshdesk", cfg{"resource": "ticket", "operation": "update", "ticketId": "T3"}, "-"},
		{"app.freshdesk", cfg{"resource": "ticket", "operation": "update", "ticketId": "T3", "updateFields": cfg{"ccEmails": "bob@example.test"}}, "bob@example.test"},
		{"app.freshservice", cfg{"resource": "ticket", "operation": "create", "email": "ada@example.test", "subject": "Login"}, "-"},
		{"app.freshservice", cfg{"resource": "ticket", "operation": "create", "email": "ada@example.test", "subject": "Login",
			"additionalFields": cfg{"cc_emails": "bob@example.test"}}, "bob@example.test"},
		{"app.okta", cfg{"resource": "user", "operation": "create", "email": "ada@example.test", "login": "ada"}, "ada@example.test"},
		{"app.okta", cfg{"resource": "user", "operation": "create", "email": "ada@example.test", "activate": false}, "-"},
		{"app.okta", cfg{"resource": "user", "operation": "delete", "userId": "u1"}, "-"},
		{"app.okta", cfg{"resource": "user", "operation": "delete", "userId": "u1", "sendEmail": true}, "the admins, about user u1"},
		{"app.home_assistant", cfg{"resource": "service", "operation": "call", "domain": "notify", "service": "mobile_app_ada"}, "service mobile_app_ada"},
		{"app.home_assistant", cfg{"resource": "service", "operation": "call", "domain": "light", "service": "turn_on"}, "-"},
		{"app.gotify", cfg{"resource": "message", "operation": "create", "message": "Done"}, "the Gotify app's subscribers"},
		{"app.brevo", cfg{"resource": "sender", "operation": "create", "name": "Ada", "email": "ada@example.test"}, "ada@example.test"},
		{"comm.telegram", cfg{"resource": "message", "operation": "pinChatMessage", "chatId": "42", "messageId": "7"}, "42"},
		// Onfleet texts a task's recipient unless told to skip it; the card
		// names the phone the text goes to, so an always-allow rule is one
		// phone's, not every phone's under one name.
		{"app.onfleet", cfg{"resource": "task", "operation": "create", "additionalFields": cfg{"recipient": cfg{"recipientProperties": cfg{
			"recipientName": "Ada", "recipientPhone": "+15550100", "recipientSkipSMSNotifications": true}}}}, "-"},
		{"app.onfleet", cfg{"resource": "task", "operation": "create", "additionalFields": cfg{"recipient": cfg{"recipientProperties": cfg{
			"recipientName": "Ada", "recipientPhone": "+15550100", "recipientSkipSMSNotifications": false}}}}, "+15550100"},
		{"app.onfleet", cfg{"resource": "task", "operation": "create", "additionalFields": cfg{"recipient": cfg{"recipientProperties": cfg{
			"recipientPhone": "+15550100"}}}}, "+15550100"},
		{"app.onfleet", cfg{"resource": "task", "operation": "create", "additionalFields": cfg{"recipient": cfg{"recipientProperties": cfg{
			"recipientName": "Ada"}}}}, "Ada"},
		{"app.onfleet", cfg{"resource": "task", "operation": "create"}, "the task's recipient"},
		// A custom field Zammad reads as a send of its own (an article on a
		// ticket update, an invite on a new user) is left out of the request,
		// so it holds nothing; another custom field is data.
		{"app.zammad", cfg{"resource": "ticket", "operation": "update", "id": "5", "updateFields": cfg{"customFieldsUi": cfg{
			"customFieldPairs": []interface{}{cfg{"name": "article", "value": cfg{"type": "email", "to": "v@example.test"}}}}}}, "-"},
		{"app.zammad", cfg{"resource": "user", "operation": "create", "firstname": "Ada", "lastname": "L", "additionalFields": cfg{
			"email": "ada@example.test", "customFieldsUi": cfg{"customFieldPairs": []interface{}{cfg{"name": "invite", "value": true}}}}}, "-"},
		{"app.zammad", cfg{"resource": "user", "operation": "create", "firstname": "Ada", "lastname": "L", "additionalFields": cfg{
			"email": "ada@example.test", "customFieldsUi": cfg{"customFieldPairs": []interface{}{cfg{"name": "shoe", "value": "42"}}}}}, "-"},
		// Zammad files an article with no type as a note (its API's default),
		// so one with only a body runs.
		{"app.zammad", cfg{"resource": "ticket", "operation": "create", "title": "Login", "group": "Users", "customer": "ada@example.test",
			"article": cfg{"articleDetails": cfg{"body": "Called her"}}}, "-"},
		// A PayPal payout is money movement, open; its email waits when it
		// carries words of the sender's: a subject, a message, a note on the
		// payout or on an item, or raw items.
		{"app.pay_pal", cfg{"resource": "payout", "operation": "create", "senderBatchId": "b1",
			"itemsUi": cfg{"itemsValues": []interface{}{cfg{"receiverValue": "ada@example.test", "amount": "5"}}}}, "-"},
		{"app.pay_pal", cfg{"resource": "payout", "operation": "create", "senderBatchId": "b1",
			"itemsUi": cfg{"itemsValues": []interface{}{cfg{"receiverValue": "ada@example.test", "amount": "5", "note": "Thanks"}}}},
			"the payout's receivers"},
		{"app.pay_pal", cfg{"resource": "payout", "operation": "create", "senderBatchId": "b1", "additionalFields": cfg{"note": "Thanks"}},
			"the payout's receivers"},
		{"app.pay_pal", cfg{"resource": "payout", "operation": "create", "senderBatchId": "b1", "jsonParameters": true,
			"itemsJson": `[{"receiver": "ada@example.test", "note": "Thanks"}]`}, "the payout's receivers"},
		{"service.github", cfg{"resource": "issue", "operation": "create", "owner": "o", "repository": "r", "title": "Bug"}, "-"},
	} {
		out, handled, err := hilsend.Guard(sendsCtx(db), c.nodeType, workflow.NodeInput{}, c.config)
		if err != nil {
			t.Fatalf("%s %v: %v", c.nodeType, c.config, err)
		}
		if c.to == "-" {
			if handled {
				t.Errorf("%s %v: held, want it to go through", c.nodeType, c.config)
			}
			continue
		}
		if !handled {
			t.Errorf("%s %v: went through, want it to wait", c.nodeType, c.config)
			continue
		}
		p, err := st.Get(context.Background(), out[0].Items[0].JSON["hil_id"].(string))
		if err != nil || p.Recipient.Name != c.to {
			t.Errorf("%s %v: recipient %q, %v; want %q", c.nodeType, c.config, p.Recipient.Name, err, c.to)
		}
	}
}

// A key an op doesn't declare is refused when the step runs (apipkg
// checkCollections), so the send gate queues no card for it: approving one
// would only fail. An agent's run of these configs goes on to the refusal.
func TestUndeclaredKeysQueueNoCard(t *testing.T) {
	asOperator(t)
	t.Setenv(hilsend.EnvAgentContext, "coder")
	Build(nil)
	db := sendsDB(t)
	type cfg = map[string]interface{}
	for name, c := range map[string]struct {
		nodeType string
		config   cfg
	}{
		"a raw article on a Zammad ticket update": {"app.zammad", cfg{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": cfg{"article": cfg{"type": "email", "to": "v@example.test", "body": "x"}}}},
		"raw articles on a Zammad ticket update": {"app.zammad", cfg{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": cfg{"articles": []interface{}{cfg{"type": "twitter status", "body": "tweet"}}}}},
		"a raw bcc_emails on a Freshservice ticket": {"app.freshservice", cfg{"resource": "ticket", "operation": "create",
			"email": "a@example.test", "subject": "S", "description": "D", "additionalFields": cfg{"bcc_emails": []interface{}{"v@example.test"}}}},
	} {
		if _, handled, err := hilsend.Guard(sendsCtx(db), c.nodeType, workflow.NodeInput{}, c.config); handled || err != nil {
			t.Errorf("%s: handled=%v err=%v; want no card (the run refuses it)", name, handled, err)
		}
	}
}
