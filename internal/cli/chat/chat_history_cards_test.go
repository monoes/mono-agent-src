package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// The cards of a conversation are read in one command, however many card turns
// it holds: newest first, up to a size, leaving out the turns the caller has.
func TestCardTurnsAndTheirEventsAreReadInOneCommand(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	recordStarted(t, r, c.ID, "t1")
	var ids []string
	write := func(i, size int) {
		card := chatevents.UISurfacePayload{SurfaceID: fmt.Sprintf("hil-%d", i), Provenance: "system",
			Message: json.RawMessage(fmt.Sprintf(`{"version":"1.0","createSurface":{"surfaceId":"hil-%d","dataModel":{"pad":%q}}}`, i, strings.Repeat("x", size)))}
		evs, err := r.store.RecordUISurfaces("default", c.ID, []chatevents.UISurfacePayload{card})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, evs[0].TurnID)
	}
	for i := 0; i < 5; i++ {
		write(i, 10)
	}
	read := func(args ...string) []cardTurnRecord {
		t.Helper()
		out, err := r.history(t, nil, append([]string{"turns", c.ID, "--cards", "--events", "--limit", "200"}, args...)...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		var page chatCardTurnPage
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatalf("%v: %.300s", err, out)
		}
		return page.Items
	}
	items := read()
	if len(items) != 5 {
		t.Fatalf("%d items", len(items))
	}
	for _, it := range items {
		if len(it.Events) != 1 || it.Events[0].Type != chatevents.EventUISurface || it.Events[0].TurnID != it.ID {
			t.Fatalf("%s: %+v", it.ID, it.Events)
		}
	}
	got := read("--skip", ids[0]+","+ids[3])
	if len(got) != 3 {
		t.Fatalf("with two skipped: %d", len(got))
	}
	for _, it := range got {
		if it.ID == ids[0] || it.ID == ids[3] {
			t.Fatalf("%s was not skipped", it.ID)
		}
	}
	// past the size cap the older ones are left out, the newest never
	ids = nil
	n := maxCardEventBytes/3000 + 20
	for i := 5; i < 5+n && i < 190; i++ {
		write(i, 3000)
	}
	old := maxCardEventBytes
	maxCardEventBytes = 10000
	defer func() { maxCardEventBytes = old }()
	capped := read()
	total := 0
	for _, it := range capped {
		for _, e := range it.Events {
			total += len(e.Payload)
		}
	}
	newest := false
	for _, it := range capped {
		newest = newest || it.ID == ids[len(ids)-1]
	}
	if len(capped) == 0 || len(capped) > 6 || total > 14000 || !newest {
		t.Fatalf("%d items, %d bytes, newest %v", len(capped), total, newest)
	}
	for _, bad := range [][]string{{"turns", c.ID, "--events"}, {"turns", c.ID, "--skip", "x"}} {
		if _, err := r.history(t, nil, bad...); exitCodeFor(err) != 3 {
			t.Errorf("%v without --cards: %v", bad, err)
		}
	}
}
