package orgbridge

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestReceiverIgnoresAskReplyFromWrongRole(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWaitingExecution(t, db, "exec-ask")
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf")
	if err != nil {
		t.Fatal(err)
	}
	ask := Ask{ID: NewAskID(), ProfileID: "p", OrgName: "growth", RoleID: "cfo", EndpointRoleID: "bot", ExecutionID: "exec-ask", NodeID: "n", DeadlineAt: time.Now().Add(time.Hour)}
	if err := NewAskStore(db).Create(ctx, ask); err != nil {
		t.Fatal(err)
	}
	var resumed string
	rcv := &Receiver{DB: db, Store: workflow.NewSQLiteWorkflowStore(db), Resume: func(id string) error { resumed = id; return nil }}
	rcv.dispatch(ctx, *ep, EndpointDelivery{OrgName: "growth", From: "intern", To: "bot", Subject: "re: ask:" + ask.ID, Body: "fake", MessageID: "m1"})
	if got, _ := NewAskStore(db).Get(ctx, ask.ID); got.Status != AskWaiting || resumed != "" {
		t.Fatalf("a reply from a role that was not asked answered it: %+v resumed=%q", got, resumed)
	}
	rcv.dispatch(ctx, *ep, EndpointDelivery{OrgName: "growth", From: "growth:cfo", To: "bot", Subject: "re: ask:" + ask.ID, Body: "real", MessageID: "m2"})
	if got, _ := NewAskStore(db).Get(ctx, ask.ID); got.Status != AskReplied || resumed != "exec-ask" {
		t.Fatalf("the asked role's reply was not accepted: %+v resumed=%q", got, resumed)
	}
}
