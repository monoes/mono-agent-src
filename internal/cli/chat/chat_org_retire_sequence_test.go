package chat

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

// The retirement identity rules, checked against a model over sequences of claim, release, retire, save (a
// hand-off, possibly late) and load, with more stored workers than a turn loads:
//
//   - a retired worker never runs again: its stored row has no session and is marked retired, whenever and in
//     whatever order it was saved, and the next turn does not get it back;
//   - a new worker is never retired: a retirement covers only the workers that existed when it was made;
//   - the claim number the store hands out with a claim is the claim's own;
//   - every stored worker id is reserved, however many rows a turn loads.
//
// The table rows are the interleavings that earlier reviews found one at a time; the seeds cover the rest.

type seqWorker struct {
	id    string
	idx   int // its claim number in the model: the n-th claim of the task
	saved bool
}

type seqOp string

const (
	opClaim   seqOp = "claim"   // a new worker takes the task
	opRelease seqOp = "release" // the holder gives the task back
	opRetire  seqOp = "retire"  // the operator retires (needs the task idle and taken at least once)
	opSave    seqOp = "save"    // some worker's hand-off is stored (the oldest unsaved first, else a random one: a late save)
	opLoad    seqOp = "load"    // the next turn loads the veterans and the ids
	opFill    seqOp = "fill"    // unrelated stored workers, so that more rows exist than a turn loads
	opRenew   seqOp = "renew"   // the holder renews its lease: the store writes one more claim event
)

var seqTable = map[string][]seqOp{
	"claim, release, retire, claim (codex: the number is the claim's own)":     {opClaim, opRelease, opRetire, opClaim, opSave, opLoad},
	"retire, then the old worker's late save, then a new claim":                {opClaim, opRelease, opRetire, opSave, opClaim, opSave, opLoad},
	"a delayed older worker after a later retirement":                          {opClaim, opRelease, opClaim, opRelease, opRetire, opSave, opSave, opLoad},
	"more rows than a turn loads, the retired worker the oldest":               {opClaim, opRelease, opRetire, opSave, opFill, opLoad, opClaim, opSave, opLoad},
	"renewals between the claims, a retirement after them":                     {opClaim, opRenew, opRenew, opRelease, opRetire, opClaim, opRenew, opRelease, opSave, opSave, opLoad},
	"retire before any claim is refused, then the normal run":                  {opRetire, opClaim, opRelease, opRetire, opSave, opLoad},
	"two retirements, a worker between them":                                   {opClaim, opRelease, opRetire, opClaim, opRelease, opRetire, opSave, opSave, opLoad},
	"a worker stopped before its first hand-off, the task retired and retaken": {opClaim, opRelease, opRetire, opClaim, opLoad, opSave, opLoad},
}

func TestRetirementIdentitySequences(t *testing.T) {
	for name, ops := range seqTable {
		t.Run(name, func(t *testing.T) { runRetirementSequence(t, ops) })
	}
	for seed := int64(1); seed <= 40; seed++ {
		t.Run("seed "+strconv.FormatInt(seed, 10), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			all := []seqOp{opClaim, opRelease, opRetire, opSave, opSave, opLoad, opFill, opRenew}
			ops := make([]seqOp, 0, 24)
			for i := 0; i < 24; i++ {
				ops = append(ops, all[rng.Intn(len(all))])
			}
			runRetirementSequence(t, ops)
		})
	}
}

func runRetirementSequence(t *testing.T, ops []seqOp) {
	t.Helper()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	aiStore, _ := ai.NewAIStore(db.DB)
	conv, err := aiStore.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, "/w")
	if err != nil {
		t.Fatal(err)
	}
	ts := tasks.NewStore(db.DB)
	ctx := context.Background()
	human := tasks.Actor{Kind: tasks.Human}
	se, _ := ts.CreateSession(ctx, "default", tasks.SessionInput{Title: "m", Goal: "g", ConversationID: conv.ID}, human)
	tk, _ := ts.AddSessionTask(ctx, "default", se.ID, tasks.SessionTaskInput{Title: "t", Acceptance: "done"}, human)
	if _, err := ts.ApproveSession(ctx, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	actor := tasks.Actor{Kind: tasks.Agent, Name: "m", Session: se.ID, Task: tk.ID}
	j := &turnJournal{store: aiStore, profileID: "default", conversationID: conv.ID, turnID: "t1"}

	var (
		workers []*seqWorker
		retires []int // the claim count at each retirement
		claims  int
		held    bool
		fillers int
		nextID  = 1
		trace   []string
	)
	retired := func(w *seqWorker) bool {
		for _, k := range retires {
			if k >= w.idx {
				return true
			}
		}
		return false
	}
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("%s\nafter: %s", fmt.Sprintf(format, a...), strings.Join(trace, ", "))
	}
	check := func() {
		t.Helper()
		rows, err := aiStore.ListOrgWorkers("default", conv.ID, 0)
		if err != nil {
			fail("list: %v", err)
		}
		byID := map[string]ai.OrgWorker{}
		for _, r := range rows {
			byID[r.AgentID] = r
		}
		for _, w := range workers {
			if r, ok := byID[w.id]; ok && w.saved {
				if retired(w) && (r.Outcome != ai.OrgWorkerRetired || r.SessionID != "") {
					fail("worker %s (claim %d) was retired but is stored as %q with session %q", w.id, w.idx, r.Outcome, r.SessionID)
				}
				if !retired(w) && (r.Outcome == ai.OrgWorkerRetired || r.SessionID == "") {
					fail("worker %s (claim %d) was not retired but is stored as %q with session %q", w.id, w.idx, r.Outcome, r.SessionID)
				}
			}
			if got, err := ts.WorkerRetired(ctx, "default", se.ID, tk.ID, w.idx); err != nil || got != retired(w) {
				fail("WorkerRetired(claim %d) = %v (%v), the model says %v", w.idx, got, err, retired(w))
			}
		}
		live := map[string]bool{}
		for _, v := range j.veterans() {
			if !v.Retired {
				live[v.ID] = true
			}
		}
		for _, w := range workers {
			if w.saved && retired(w) && live[w.id] {
				fail("retired worker %s is given back to the lead", w.id)
			}
		}
		ids, err := aiStore.OrgWorkerIDs("default", conv.ID)
		if err != nil {
			fail("ids: %v", err)
		}
		for _, w := range workers {
			if w.saved && !slices.Contains(ids, w.id) {
				fail("stored worker %s is missing from the reserved ids", w.id)
			}
		}
		if got := len(ids); got < len(rows) {
			fail("%d ids reserved for %d stored rows", got, len(rows))
		}
		// The ids are reserved whatever a turn loads: a conductor seeded with them never hands out a stored one.
		seq := dynorg.NextWorkerSeq(ids)
		for _, id := range ids {
			if n, err := strconv.Atoi(strings.TrimPrefix(id, "w")); err == nil && n > seq {
				fail("the worker counter %d does not clear stored id %s", seq, id)
			}
		}
	}

	for _, op := range ops {
		trace = append(trace, string(op))
		switch op {
		case opClaim:
			if held {
				continue
			}
			_, n, err := ts.SessionClaimNumbered(ctx, "default", se.ID, tk.ID, actor, 0)
			if err != nil {
				continue // not claimable now (for instance still in a state that forbids it)
			}
			claims++
			if n != claims {
				fail("claim %d was given the number %d", claims, n)
			}
			held = true
			workers = append(workers, &seqWorker{id: "w" + strconv.Itoa(nextID), idx: n})
			nextID++
		case opRelease:
			if !held {
				continue
			}
			if _, err := ts.SessionRelease(ctx, "default", se.ID, tk.ID, "back", actor); err != nil {
				fail("release: %v", err)
			}
			held = false
		case opRenew:
			if !held {
				continue
			}
			_, n, err := ts.SessionClaimNumbered(ctx, "default", se.ID, tk.ID, actor, 0)
			if err != nil {
				fail("renew: %v", err)
			}
			claims++
			if n != claims {
				fail("the renewal was given the number %d, the model counts %d claim events", n, claims)
			}
		case opRetire:
			if held {
				continue
			}
			if claims == 0 {
				// Nothing has taken the task yet: there is no worker to retire.
				if _, err := ts.RetireWorker(ctx, "default", se.ID, tk.ID, "", human); err == nil {
					fail("a task no worker took was retired")
				}
				continue
			}
			if _, err := ts.RetireWorker(ctx, "default", se.ID, tk.ID, "", human); err != nil {
				fail("retire: %v", err)
			}
			if len(retires) == 0 || retires[len(retires)-1] != claims {
				retires = append(retires, claims)
			}
		case opSave:
			var w *seqWorker
			for _, c := range workers {
				if !c.saved {
					w = c
					break
				}
			}
			if w == nil && len(workers) > 0 {
				w = workers[len(workers)-1] // a repeated hand-off of the newest
			}
			if w == nil {
				continue
			}
			j.rememberWorker(dynorg.Veteran{ID: w.id, Runtime: "claude", Session: "sess-" + w.id, Report: "r", Outcome: "done",
				MissionSession: se.ID, MissionTask: tk.ID, MissionClaims: int64(w.idx)})
			w.saved = true
		case opFill:
			for i := 0; i < 13; i++ {
				fillers++
				j.rememberWorker(dynorg.Veteran{ID: "w" + strconv.Itoa(100+fillers), Runtime: "claude", Session: "s", Report: "f", Outcome: "done"})
			}
		case opLoad:
		}
		check()
	}
}
