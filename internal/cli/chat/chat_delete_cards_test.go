package chat

import (
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// A chat that holds card turns (a press, approvals shown by sync-hil), which
// the turn list leaves out, is deleted like any other, with its files.
func TestDeleteWorksForAChatWithCardTurns(t *testing.T) {
	for name, write := range map[string]func(t *testing.T, r *accessRig, conv string){
		"a press": func(t *testing.T, r *accessRig, conv string) {
			if _, err := r.store.RecordUIAction("default", conv, chatevents.UIActionPayload{SurfaceID: "s", Name: "n", Outcome: "rejected"}); err != nil {
				t.Fatal(err)
			}
		},
		"a synced approval": func(t *testing.T, r *accessRig, conv string) {
			card := chatevents.UISurfacePayload{SurfaceID: "hil-1", Provenance: "system",
				Message: []byte(`{"version":"1.0","createSurface":{"surfaceId":"hil-1"}}`)}
			if _, err := r.store.RecordUISurfaces("default", conv, []chatevents.UISurfacePayload{card}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newAccessRig(t, true)
			c := r.conv(t, ai.AccessFull)
			cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
			log := filepath.Join(hostDir(cfg), "turns", "t1.log")
			touchFile(t, log)
			recordStarted(t, r, c.ID, "t1")
			write(t, r, c.ID)
			if cards, _, err := r.store.ListCardTurns(c.ID, "default", "", 10); err != nil || len(cards) != 1 {
				t.Fatalf("card turns %+v %v", cards, err)
			}
			if out, err := r.history(t, nil, "delete", c.ID); err != nil {
				t.Fatalf("delete: %v\n%s", err, out)
			}
			if _, err := r.store.GetConversation(c.ID, "default"); err == nil {
				t.Fatal("the conversation is still there")
			}
			if exists(log) {
				t.Error("the turn's log survived")
			}
		})
	}
}
