package chat

import (
	"context"
	"errors"
	"sort"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// SyncHILResult is what `chat ui sync-hil` prints. Surfaced and Resolved are
// surface ids ("hil-<id>", "send-<id>"). Skipped lists pending items that got
// no surface and why (too large to show whole, or the conversation already
// holds as many live surfaces as it may); they stay on the queue, and a later
// sync tries them again.
type SyncHILResult struct {
	Surfaced []string      `json:"surfaced"`
	Resolved []string      `json:"resolved"`
	Skipped  []SyncSkipped `json:"skipped,omitempty"`
}

// SyncSkipped is one pending item that was not surfaced.
type SyncSkipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func newChatUISyncHILCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "sync-hil <conversation-id> --profile <profile>",
		Short: "Show the profile's pending approvals as surfaces in a conversation",
		Long: "Lists the profile's pending Human-in-Loop items and send approvals (what `hil list --json` lists) and journals one " +
			"system surface into the conversation for each one it has not surfaced yet (surface id hil-<id> or send-<id>), and " +
			"a notice in place of each surface whose item is no longer pending. Running it again surfaces nothing new. " +
			"Prints {\"surfaced\":[surface ids],\"resolved\":[surface ids]} (plus \"skipped\" when an item could not be shown) " +
			"and exits 0. A conversation with no turn of its own yet gets no surface.",
		Example: `  monoagentcli chat ui sync-hil c1 --profile work`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runSyncHIL(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			return clicore.PrintJSON(res)
		},
	}
}

func runSyncHIL(ctx context.Context, cfg *Deps, conversationID string) (*SyncHILResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	if profileID == "" {
		return nil, clicore.InvalidInput("name the profile with --profile")
	}
	if _, err := store.GetConversation(conversationID, profileID); err != nil {
		return nil, chatStoreErr(err)
	}
	// A card written here is a turn of its own, and any turn locks the chat's mode and marks the conversation as
	// started. So a conversation with no work turn yet gets none; a later sync shows its cards.
	started, err := store.HasWorkTurn(profileID, conversationID)
	if err != nil {
		return nil, err
	}
	if !started {
		return &SyncHILResult{Surfaced: []string{}, Resolved: []string{}}, nil
	}
	evs, err := store.UIEvents(profileID, conversationID)
	if err != nil {
		return nil, err
	}
	replay := dynorg.RebuildRegistry(evs)
	e := &sysEnv{cfg: cfg}
	items, err := e.listHIL(ctx)
	if err != nil {
		return nil, err
	}
	res := &SyncHILResult{Surfaced: []string{}, Resolved: []string{}}
	var payloads []chatevents.UISurfacePayload
	add := func(ref chatevents.UIRef, msgs [][]byte) bool {
		for _, raw := range msgs {
			if _, err := replay.Registry.Handle(raw, a2ui.FromSystem); err != nil {
				return false
			}
			m, _ := a2ui.ParseMessage(raw, a2ui.DefaultLimits)
			r := ref
			payloads = append(payloads, chatevents.UISurfacePayload{SurfaceID: m.SurfaceID, Provenance: string(a2ui.FromSystem), Ref: &r, Message: raw})
		}
		return true
	}

	pending := map[string]bool{}
	for _, it := range items {
		pending[surfaceOfItem(it)] = true
	}
	for _, id := range liveCardIDs(replay) {
		if pending[id] || !hasButton(replay.Registry.Surface(id)) {
			continue
		}
		if msgs, err := surfaces.ResolvedNotice(id, "No longer pending", "neutral"); err == nil && add(refOfSurface(evs, id), msgs) {
			res.Resolved = append(res.Resolved, id)
		}
	}
	for _, it := range items {
		id := surfaceOfItem(it)
		old := replay.Registry.Surface(id)
		if old != nil && old.Prov == a2ui.FromSystem && hasButton(old) {
			continue
		}
		// Its card was never drawn, is gone, or stands as a note although the
		// item still waits: draw it (again).
		ref, msgs, err := buildItemSurface(it)
		if err == nil && old != nil {
			var del [][]byte
			if del, err = surfaces.Resolved(id); err == nil {
				msgs = append(del, msgs...)
			}
		}
		if err == nil && !add(ref, msgs) {
			err = errors.New("the conversation holds as many live surfaces as it may")
		}
		if err != nil {
			res.Skipped = append(res.Skipped, SyncSkipped{ID: it.ID, Reason: err.Error()})
			continue
		}
		res.Surfaced = append(res.Surfaced, id)
	}
	if _, err := store.RecordUISurfaces(profileID, conversationID, payloads); err != nil {
		return nil, err
	}
	return res, nil
}

func surfaceOfItem(it listedHIL) string {
	if it.Kind == "send" && it.Send != nil {
		return surfaces.SendSurfaceID(it.Send.ID)
	}
	return surfaces.HILSurfaceID(it.ID)
}

func buildItemSurface(it listedHIL) (chatevents.UIRef, [][]byte, error) {
	if it.Kind == "send" && it.Send != nil {
		msgs, err := surfaces.Send(*it.Send)
		return chatevents.UIRef{Kind: surfaces.RefSend, ID: it.Send.ID}, msgs, err
	}
	msgs, err := surfaces.HIL(it.HILItem)
	return chatevents.UIRef{Kind: surfaces.RefHIL, ID: it.ID}, msgs, err
}

// liveCardIDs are the live hil-/send- surfaces of the replay, sorted.
func liveCardIDs(r *dynorg.UIReplay) []string {
	var ids []string
	for id := range r.Authors {
		if len(id) > 4 && (id[:4] == "hil-" || (len(id) > 5 && id[:5] == "send-")) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func hasButton(s *a2ui.Surface) bool {
	if s == nil {
		return false
	}
	for _, c := range s.Components {
		if c["component"] == "Button" {
			return true
		}
	}
	return false
}
