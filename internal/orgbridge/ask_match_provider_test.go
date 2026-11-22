package orgbridge

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// With a provider set for the asks surface, the match comes from it and the
// reply's _jev model names "<provider>/<model>"; a provider with no
// probability never links a reply (plan D7).
func TestReceiverJevMatchFromAProvider(t *testing.T) {
	db, ep, rcv, resumed := receiverFixture(t)
	enableAsks(t, db, "p")
	t.Setenv("TYPESAFE_API_KEY", "")
	fb := jevtest.NewFakeBackend(nil)
	t.Cleanup(jevconf.RegisterKind("orgbridge-fake", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	ctx := context.Background()
	if err := jevconf.SaveProvider(ctx, db, jevconf.Provider{Name: "local", Kind: "orgbridge-fake", BaseURL: "http://localhost", Model: "openai/gpt-4.1-mini"}); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetSurfaceProvider(ctx, db, "p", jevconf.Asks, "local"); err != nil {
		t.Fatal(err)
	}
	ask := createAsk(t, db, Ask{ProfileID: "p", OrgName: "growth", EndpointRoleID: "bot", ExecutionID: "exec-ask", NodeID: "n", Question: "Which region?"})
	fb.SetHandler(jevtest.Fixed(map[string]string{"ask": ask.ID}))

	fb.Caps.Probability = jev.ProbNone
	rcv.dispatch(ctx, *ep, EndpointDelivery{OrgName: "growth", From: "lead", To: "bot", Subject: "re: region", Body: "EU", MessageID: newMsgID()})
	if got := askStatus(t, db, ask.ID); got.Status != AskWaiting || len(*resumed) != 0 || len(fb.Requests()) != 1 {
		t.Fatalf("no-probability: ask = %+v resumed=%v requests=%d", got, *resumed, len(fb.Requests()))
	}

	fb.Caps.Probability = jev.ProbLogprob
	rcv.dispatch(ctx, *ep, EndpointDelivery{OrgName: "growth", From: "lead", To: "bot", Subject: "re: region", Body: "EU", MessageID: newMsgID()})
	got := askStatus(t, db, ask.ID)
	meta, _ := got.Reply["_jev"].(map[string]interface{})
	if got.Status != AskReplied || meta["model"] != "local/openai/gpt-4.1-mini" || len(*resumed) != 1 {
		t.Fatalf("ask = %+v resumed=%v", got, *resumed)
	}
}
