package coderhost

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func setMD(s *Settings, k int)  { s.MissionDirections = &k }
func clearMD(s *Settings)       { s.MissionDirections = nil }
func mdIsUnset(s Settings) bool { return s.MissionDirections == nil }
func effMD(s Settings) int      { return s.EffectiveMissionDirections() }
func mdOf(s Settings) (int, bool) {
	if s.MissionDirections == nil {
		return 0, false
	}
	return *s.MissionDirections, true
}

// storedMD reads mission_directions from the raw settings document of a conversation: whether the key is there, and its value.
func storedMD(t *testing.T, st *Store, conv string) (int, bool) {
	t.Helper()
	var raw string
	if err := st.db.QueryRow(`SELECT settings FROM coder_sessions WHERE profile_id = 'p' AND conversation_id = ?`, conv).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	v, ok := doc["mission_directions"]
	if !ok {
		return 0, false
	}
	var k int
	if err := json.Unmarshal(v, &k); err != nil {
		t.Fatal(err)
	}
	return k, true
}

// spawnK spawns a child of a master whose K is parentK (nil: not set) and runs write in the window between SetRole
// and the spawn's settings initialization. It returns the store and the child.
func spawnK(t *testing.T, parentK *int, write func(st *Store, c Caller, child string)) (*Store, string) {
	t.Helper()
	st := newStoreT(t)
	c, _ := master(t, st, "coding")
	if parentK != nil {
		if _, err := st.UpdateSettings("p", c.ConversationID, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
			setMD(&cur, *parentK)
			return cur, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := st.spawnWith(c, SpawnRequest{}, spawnHooks{onCreated: func(child string) error {
		if write != nil {
			write(st, c, child)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return st, out.ConversationID
}

func intp(k int) *int { return &k }

func toggleMission(st *Store, _ Caller, child string) {
	if _, err := st.UpdateSettings("p", child, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		cur.Mission = true
		return cur, nil
	}); err != nil {
		panic(err)
	}
}

func agentPatch(st *Store, c Caller, child string) {
	if _, _, err := st.SetChild(c, child, []byte(`{"checkin_minutes":10}`)); err != nil {
		panic(err)
	}
}

func TestMissionToggleBetweenSetRoleAndInitKeepsInheritance(t *testing.T) {
	for _, k := range []int{1, 5} {
		st, child := spawnK(t, intp(k), toggleMission)
		if got, ok := storedMD(t, st, child); !ok || got != k {
			t.Fatalf("parent K=%d: child stored (%d, present=%v), want %d", k, got, ok, k)
		}
		s, _ := st.GetSettings("p", child)
		if !s.Mission {
			t.Fatal("the toggle was lost")
		}
	}
}

func TestAgentSetChildPatchBetweenSetRoleAndInitKeepsInheritance(t *testing.T) {
	for _, k := range []int{1, 5} {
		st, child := spawnK(t, intp(k), agentPatch)
		if got, ok := storedMD(t, st, child); !ok || got != k {
			t.Fatalf("parent K=%d: child stored (%d, present=%v), want %d", k, got, ok, k)
		}
	}
}

func TestExplicitOperatorZeroStaysZeroThroughOtherWriters(t *testing.T) {
	st, child := spawnK(t, intp(6), func(st *Store, c Caller, child string) {
		if _, err := st.UpdateSettings("p", child, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
			setMD(&cur, 0)
			return cur, nil
		}); err != nil {
			panic(err)
		}
	})
	if got, ok := storedMD(t, st, child); !ok || got != 0 {
		t.Fatalf("explicit 0 stored as (%d, present=%v)", got, ok)
	}
	toggleMission(st, Caller{}, child)
	if got, ok := storedMD(t, st, child); !ok || got != 0 {
		t.Fatalf("after another writer: (%d, present=%v), want an explicit 0", got, ok)
	}
	s, _ := st.GetSettings("p", child)
	if effMD(s) != 3 {
		t.Fatalf("effective K of an explicit 0 = %d, want 3", effMD(s))
	}
}

func TestOldRowsWithoutTheKeyDecodeAsUnsetAndInherit(t *testing.T) {
	s, err := ParseSettings("c", `{"max_depth":3}`, nil)
	if err != nil || !mdIsUnset(s) {
		t.Fatalf("a row without the key: %v, unset=%v", err, mdIsUnset(s))
	}
	st, child := spawnK(t, intp(5), func(st *Store, _ Caller, child string) {
		if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = '{"max_depth":3}' WHERE profile_id = 'p' AND conversation_id = ?`, child); err != nil {
			panic(err)
		}
	})
	if got, ok := storedMD(t, st, child); !ok || got != 5 {
		t.Fatalf("child of a row without the key stored (%d, present=%v), want 5", got, ok)
	}
}

func TestMissionDirectionsMarshalling(t *testing.T) {
	def := DefaultSettings()
	clearMD(&def)
	out, _ := json.Marshal(def)
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(out, &doc)
	if _, ok := doc["mission_directions"]; ok {
		t.Fatalf("an unset K must be omitted: %s", out)
	}
	for _, k := range []int{0, 5} {
		s := DefaultSettings()
		setMD(&s, k)
		out, _ := json.Marshal(s)
		doc = nil
		_ = json.Unmarshal(out, &doc)
		if string(doc["mission_directions"]) != map[int]string{0: "0", 5: "5"}[k] {
			t.Fatalf("K=%d marshalled as %q", k, doc["mission_directions"])
		}
	}
}

func TestApplyOfMissionDirectionsIsExplicitAndDoesNotAliasTheSource(t *testing.T) {
	base := DefaultSettings()
	setMD(&base, 4)
	got, _, err := base.Apply([]byte(`{"mission_directions":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := mdOf(got); !ok || k != 0 {
		t.Fatalf("a patched 0 = (%d, set=%v), want an explicit 0", k, ok)
	}
	if k, _ := mdOf(base); k != 4 {
		t.Fatalf("Apply wrote through the source's pointer: %d", k)
	}
	if got, _, err = base.Apply([]byte(`{"checkin_minutes":10}`)); err != nil {
		t.Fatal(err)
	} else if k, ok := mdOf(got); !ok || k != 4 {
		t.Fatalf("an unrelated patch changed K: (%d, %v)", k, ok)
	}
	// null resets K to not set.
	if got, _, err = base.Apply([]byte(`{"mission_directions":null}`)); err != nil || !mdIsUnset(got) {
		t.Fatalf("null: %v unset=%v", err, mdIsUnset(got))
	}
	if _, _, err = base.Apply([]byte(`{"mission_directions":7}`)); err == nil {
		t.Fatal("7 accepted")
	}
}
