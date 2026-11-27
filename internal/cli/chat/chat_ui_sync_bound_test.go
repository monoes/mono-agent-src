package chat

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// sync-hil runs every 10 seconds for as long as a chat is open: whatever the
// queue holds, once the cards show it a sync writes nothing more.
func TestRepeatedSyncStopsWritingWhateverTheQueueHolds(t *testing.T) {
	send := func(id, state, extra string) string {
		return fmt.Sprintf(`{"id":%q,"digest":"d","kind":"send"%s,"send":{"kind":"send","id":%q,"state":%q,"action":"dm","recipient":{"name":"Ada"},"text":"Hi"}}`, id, extra, id, state)
	}
	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, fmt.Sprintf(`{"id":"i%d","digest":"d","node_name":"n","readonly_data":{},"editable_data":{}}`, i))
	}
	for name, queue := range map[string]string{
		"more items than slots":       "[" + strings.Join(many, ",") + "]",
		"a hidden reply link":         `[{"id":"1","digest":"d","redacted":true},` + send("2", "pending", `,"redacted":true`) + `]`,
		"an item no card can show":    `[{"id":"1","digest":"d","readonly_data":{"x":"` + strings.Repeat("x", 5000) + `"}}]`,
		"a bad id":                    `[{"id":"a b","digest":"d"}]`,
		"a decided send still listed": "[" + send("1", "approved", "") + "," + send("2", "sent", "") + "]",
		"no digest":                   `[{"id":"1","node_name":"n"}]`,
		"the same item twice":         `[{"id":"1","digest":"d"},{"id":"1","digest":"e"}]`,
		"an empty queue":              `[]`,
	} {
		r := newSysRig(t)
		startChat(t, r)
		// Notes standing where cards of listed and unlisted items would be,
		// one of them an in-progress note whose item cannot be read.
		for _, id := range []string{"hil-i11", "hil-1", "send-1", "send-9"} {
			msgs, err := surfaces.ResolvedNotice(id, sendingNote, "info")
			ref := chatevents.UIRef{Kind: surfaces.RefSend, ID: id[strings.Index(id, "-")+1:]}
			if id == "send-9" {
				ref.State = refSending
			}
			r.show(t, ref, msgs, err)
		}
		r.cli.errs["hil show"] = errors.New("cannot read")
		r.cli.outputs["hil list"] = queue
		count := func() int {
			evs, err := r.store.UIEvents("default", r.conv.ID)
			if err != nil {
				t.Fatal(err)
			}
			return len(evs)
		}
		syncHIL(t, r)
		syncHIL(t, r)
		settled := count()
		for i := 0; i < 5; i++ {
			syncHIL(t, r)
		}
		if got := count(); got != settled {
			t.Errorf("%s: the journal grew from %d to %d events in five more syncs", name, settled, got)
		}
	}
}
