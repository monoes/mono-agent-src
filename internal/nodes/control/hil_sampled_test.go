package control

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/workflow"
)

// auto_decide is never lowered: a unanimous sample below approve_above or
// reject_above settles nothing.
func TestHILAuto_AUnanimousSampleKeepsTheConfiguredThreshold(t *testing.T) {
	const kind = "fake-hil-sampled"
	for _, choice := range []string{"approve", "reject"} {
		t.Run(choice, func(t *testing.T) {
			db := newAutoTestDB(t)
			enable(t, db)
			fb := jevtest.NewFakeBackend(nil)
			fb.SetSampled(jevtest.Fixed(map[string]string{"decision": choice, "risk": "0"}), 5) // reads 0.75
			t.Cleanup(jevconf.RegisterKind(kind, func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
			if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{Name: "local", Kind: kind, BaseURL: "http://localhost:11434/v1"}); err != nil {
				t.Fatal(err)
			}
			if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
				t.Fatal(err)
			}
			_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(),
				autoConfig(map[string]interface{}{"policy": "ok", "approve_above": 0.9, "reject_above": 0.9}))
			if !errors.Is(err, workflow.ErrNodePaused) {
				t.Fatalf("err = %v", err)
			}
			for _, r := range rowsOf(t, db) {
				sug, _ := cfgOf(t, r)["suggestion"].(map[string]interface{})
				if r.Status != "pending" || sug["choice"] != choice {
					t.Fatalf("row = %s %v", r.Status, sug)
				}
			}
		})
	}
}
