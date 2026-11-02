package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// capLead is a session of chat conv-lead whose operator-set mission_directions is k (0: not set), and the actor a
// lead works as: an agent bound to that session, the shape the coder host's session_spawn creates sessions as.
func capLead(t *testing.T, s *Store, db *sql.DB, k int) (Session, Actor) {
	t.Helper()
	lead, err := s.CreateSession(bg, "default", SessionInput{Title: "lead", ConversationID: "conv-lead"}, human)
	if err != nil {
		t.Fatal(err)
	}
	settings := "{}"
	if k > 0 {
		settings = fmt.Sprintf(`{"mission_directions":%d}`, k)
	}
	if _, err := db.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, settings, created_at, updated_at) VALUES ('default', 'conv-lead', ?, '', '')`, settings); err != nil {
		t.Fatal(err)
	}
	return lead, Actor{Kind: Agent, Name: "lead-x", Session: lead.ID}
}

func newDirectionBy(s *Store, lead Session, n int, actor Actor) (Session, error) {
	return s.CreateSession(bg, "default", SessionInput{Title: fmt.Sprintf("dir%d", n), Kind: SessionKindDirection, ParentSessionID: lead.ID}, actor)
}

func openDirections(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE kind = 'direction' AND ` + openSessionSQL).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The cap sits in CreateSession, so every caller that makes a direction below a parent as an agent meets it: at K=1
// the second draft is refused, and filling the count cannot go past K whatever the 12-child fence would allow.
func TestCreateSessionHoldsMissionDirectionsForAgents(t *testing.T) {
	s, db, _ := newTestStore(t)
	lead, lead1 := capLead(t, s, db, 1)
	if _, err := newDirectionBy(s, lead, 0, lead1); err != nil {
		t.Fatal(err)
	}
	_, err := newDirectionBy(s, lead, 1, lead1)
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "mission_directions is 1") {
		t.Fatalf("a second direction at K=1: %v", err)
	}
	if n := openDirections(t, db); n != 1 {
		t.Fatalf("%d open directions, want 1", n)
	}
	// other kinds are not counted, and the operator's own hand-made direction is not held to the agent's cap
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "r", Kind: SessionKindResearch, ParentSessionID: lead.ID}, lead1); err != nil {
		t.Fatalf("a research session is not a direction: %v", err)
	}
	if _, err := newDirectionBy(s, lead, 2, human); err != nil {
		t.Fatalf("the operator is not held to the agent's cap: %v", err)
	}
}

func TestAgentCannotFillTheDirectionCountPastK(t *testing.T) {
	s, db, _ := newTestStore(t)
	lead, lead1 := capLead(t, s, db, 0) // not set: the default of 3
	made := 0
	for i := 0; i < 10; i++ {
		if _, err := newDirectionBy(s, lead, i, lead1); err == nil {
			made++
		}
	}
	if made != DefaultMissionDirections || openDirections(t, db) != DefaultMissionDirections {
		t.Fatalf("made %d, open %d, want %d", made, openDirections(t, db), DefaultMissionDirections)
	}
}

// An archived draft and a closed direction hold no place; a running one does.
func TestOnlyOpenDirectionsHoldAPlace(t *testing.T) {
	s, db, _ := newTestStore(t)
	lead, lead1 := capLead(t, s, db, 1)
	d, err := newDirectionBy(s, lead, 0, lead1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveSession(bg, "default", d.ID, human); err != nil {
		t.Fatal(err)
	}
	d2, err := newDirectionBy(s, lead, 1, lead1)
	if err != nil {
		t.Fatalf("an archived draft must free its place: %v", err)
	}
	mustTask(t, s, "default", d2.ID, "t")
	d2 = mustRun(t, s, "default", d2)
	if _, err := newDirectionBy(s, lead, 2, lead1); !errors.Is(err, ErrLimit) {
		t.Fatalf("a running direction holds its place: %v", err)
	}
	if _, err := s.StopSession(bg, "default", d2.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := newDirectionBy(s, lead, 3, lead1); err != nil {
		t.Fatalf("a stopped direction must free its place: %v", err)
	}
}

// K is read in the insert transaction: a value the operator stored is held by the very next creation.
func TestDirectionCapReadsTheStoredSettingEachTime(t *testing.T) {
	s, db, _ := newTestStore(t)
	lead, lead1 := capLead(t, s, db, 2)
	for i := 0; i < 2; i++ {
		if _, err := newDirectionBy(s, lead, i, lead1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE coder_sessions SET settings = '{"mission_directions":3}' WHERE conversation_id = 'conv-lead'`); err != nil {
		t.Fatal(err)
	}
	if _, err := newDirectionBy(s, lead, 2, lead1); err != nil {
		t.Fatalf("K raised to 3: %v", err)
	}
	if _, err := db.Exec(`UPDATE coder_sessions SET settings = '{"mission_directions":99}' WHERE conversation_id = 'conv-lead'`); err != nil {
		t.Fatal(err)
	}
	if _, err := newDirectionBy(s, lead, 3, lead1); err != nil {
		t.Fatalf("under the hard cap of 6: %v", err)
	}
	if _, err := db.Exec(`UPDATE coder_sessions SET settings = '{not json' WHERE conversation_id = 'conv-lead'`); err != nil {
		t.Fatal(err)
	}
	if _, err := newDirectionBy(s, lead, 4, lead1); err == nil {
		t.Fatal("unreadable settings must fail closed")
	}
}

// A stored document that cannot be read refuses the creation even though the parent has FREE capacity (no open
// direction, so the default K of 3 would allow one): the parse failure is what refuses, not the limit.
func TestUnreadableSettingsRefuseWithFreeCapacity(t *testing.T) {
	s, db, _ := newTestStore(t)
	lead, lead1 := capLead(t, s, db, 3)
	if _, err := db.Exec(`UPDATE coder_sessions SET settings = '{not json' WHERE conversation_id = 'conv-lead'`); err != nil {
		t.Fatal(err)
	}
	if n := openDirections(t, db); n != 0 {
		t.Fatalf("%d open directions, want free capacity", n)
	}
	_, err := newDirectionBy(s, lead, 0, lead1)
	if err == nil {
		t.Fatal("unreadable settings must fail closed")
	}
	if errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "are not readable") {
		t.Fatalf("want the settings parse error, got: %v", err)
	}
	if n := openDirections(t, db); n != 0 {
		t.Fatalf("%d directions were created", n)
	}
}

// Creators racing for the last place: exactly one keeps it.
func TestRacingDirectionCreatorsKeepExactlyK(t *testing.T) {
	s, db, _ := newTestStore(t)
	lead, lead1 := capLead(t, s, db, 1)
	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	won := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := newDirectionBy(s, lead, i, lead1)
			won <- err == nil
		}(i)
	}
	close(start)
	wg.Wait()
	close(won)
	w := 0
	for ok := range won {
		if ok {
			w++
		}
	}
	if w != 1 || openDirections(t, db) != 1 {
		t.Fatalf("%d creators won, %d directions open; want exactly 1", w, openDirections(t, db))
	}
}
