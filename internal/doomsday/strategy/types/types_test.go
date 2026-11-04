package types

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func dir() Direction {
	return Direction{Profile: "p", Generation: 1, ID: "d1", KeyHash: KeyHash("k"), Title: "t", State: DirProposed, ProposerSession: "s", CreatedEpoch: 2}
}

func epoch() Epoch {
	return Epoch{Profile: "p", Generation: 1, Seq: 3, Trigger: TriggerTimer, StartedAt: 9, ModelPin: "a/b/1", State: EpochOpen}
}

// Every field of every record is in its canonical bytes: changing any one changes the encoding (so the MAC).
func TestCanonicalBindsEveryField(t *testing.T) {
	base := dir().Canonical()
	for name, mut := range map[string]func(*Direction){
		"profile":    func(d *Direction) { d.Profile = "q" },
		"generation": func(d *Direction) { d.Generation = 2 },
		"id":         func(d *Direction) { d.ID = "d2" },
		"key":        func(d *Direction) { d.KeyHash = KeyHash("other") },
		"title":      func(d *Direction) { d.Title = "u" },
		"state":      func(d *Direction) { d.State = DirActive },
		"proposer":   func(d *Direction) { d.ProposerSession = "x" },
		"session":    func(d *Direction) { d.SessionID = "x" },
		"created":    func(d *Direction) { d.CreatedEpoch = 3 },
	} {
		d := dir()
		mut(&d)
		if bytes.Equal(base, d.Canonical()) {
			t.Errorf("direction %s is not bound", name)
		}
	}
	eb := epoch().Canonical()
	for name, mut := range map[string]func(*Epoch){
		"profile": func(e *Epoch) { e.Profile = "q" }, "generation": func(e *Epoch) { e.Generation = 2 },
		"seq": func(e *Epoch) { e.Seq = 4 }, "trigger": func(e *Epoch) { e.Trigger = TriggerNewEvidence },
		"started": func(e *Epoch) { e.StartedAt = 10 }, "has": func(e *Epoch) { e.HasJudgment = true },
		"hash": func(e *Epoch) { e.JudgmentHash = KeyHash("j") }, "pin": func(e *Epoch) { e.ModelPin = "c" },
		"state": func(e *Epoch) { e.State = EpochJudged },
	} {
		e := epoch()
		mut(&e)
		if bytes.Equal(eb, e.Canonical()) {
			t.Errorf("epoch %s is not bound", name)
		}
	}
	jr := JudgmentRecord{Profile: "p", Generation: 1, EpochSeq: 1, Body: []byte("{}"), BodyHash: BodyHash([]byte("{}"))}
	jb := jr.Canonical()
	for name, mut := range map[string]func(*JudgmentRecord){
		"profile": func(j *JudgmentRecord) { j.Profile = "q" }, "generation": func(j *JudgmentRecord) { j.Generation = 2 },
		"epoch": func(j *JudgmentRecord) { j.EpochSeq = 2 }, "body": func(j *JudgmentRecord) { j.Body = []byte("{ }") },
		"hash": func(j *JudgmentRecord) { j.BodyHash = BodyHash(nil) },
	} {
		j := jr
		mut(&j)
		if bytes.Equal(jb, j.Canonical()) {
			t.Errorf("judgment record %s is not bound", name)
		}
	}
	nr := NoveltyRow{Profile: "p", Generation: 1, EpochSeq: 1, KeyHash: KeyHash("k"), DirectionID: "d", MatchedID: "m", Outcome: NoveltyAccepted}
	nb := nr.Canonical()
	for name, mut := range map[string]func(*NoveltyRow){
		"profile": func(n *NoveltyRow) { n.Profile = "q" }, "generation": func(n *NoveltyRow) { n.Generation = 2 },
		"epoch": func(n *NoveltyRow) { n.EpochSeq = 2 }, "key": func(n *NoveltyRow) { n.KeyHash = KeyHash("z") },
		"dir": func(n *NoveltyRow) { n.DirectionID = "e" }, "matched": func(n *NoveltyRow) { n.MatchedID = "n" },
		"outcome": func(n *NoveltyRow) { n.Outcome = NoveltyRefusedLedger },
	} {
		n := nr
		mut(&n)
		if bytes.Equal(nb, n.Canonical()) {
			t.Errorf("novelty row %s is not bound", name)
		}
	}
}

// Domain separation: the encodings of different record kinds never collide on similar content, and a length-delimited
// field cannot be shifted into its neighbour.
func TestCanonicalIsDomainSeparatedAndLengthDelimited(t *testing.T) {
	if KeyHash("x") == BodyHash([]byte("x")) {
		t.Fatal("a key hash and a body hash of the same text are equal")
	}
	a, b := dir(), dir()
	a.Title, a.ProposerSession = "ab", "c"
	b.Title, b.ProposerSession = "a", "bc"
	if bytes.Equal(a.Canonical(), b.Canonical()) {
		t.Fatal("field boundaries are not delimited")
	}
}

func TestEpochIDIsIdempotent(t *testing.T) {
	if EpochID("p", 2, 7) != EpochID("p", 2, 7) || EpochID("p", 2, 7) == EpochID("p", 3, 7) || EpochID("p", 2, 7) == EpochID("p", 2, 8) {
		t.Fatal("epoch id must be a pure function of (profile, generation, seq) and differ across them")
	}
}

// The Go move tables agree with the closed state sets: nothing moves out of a terminal state or backwards.
func TestMoveTables(t *testing.T) {
	ds := []DirectionState{DirProposed, DirActive, DirStalled, DirFinished, DirPruned}
	rank := map[DirectionState]int{DirProposed: 0, DirActive: 1, DirStalled: 2, DirFinished: 3, DirPruned: 3}
	for _, from := range ds {
		for _, to := range ds {
			if CanMoveDirection(from, to) && rank[to] <= rank[from] {
				t.Errorf("direction %s -> %s moves backwards or sideways", from, to)
			}
		}
	}
	for _, term := range []DirectionState{DirFinished, DirPruned} {
		for _, to := range ds {
			if CanMoveDirection(term, to) {
				t.Errorf("a terminal direction state %s moves to %s", term, to)
			}
		}
	}
	if !CanMoveDirection(DirProposed, DirActive) || CanMoveDirection(DirStalled, DirActive) {
		t.Fatal("expected moves wrong")
	}
	es := []EpochState{EpochOpen, EpochJudged, EpochApplied, EpochBadJudgment, EpochAbandoned}
	for _, from := range []EpochState{EpochApplied, EpochBadJudgment, EpochAbandoned} {
		for _, to := range es {
			if CanMoveEpoch(from, to) {
				t.Errorf("terminal epoch state %s moves to %s", from, to)
			}
		}
	}
	if !CanMoveEpoch(EpochOpen, EpochJudged) || CanMoveEpoch(EpochOpen, EpochApplied) || CanMoveEpoch(EpochJudged, EpochOpen) {
		t.Fatal("expected epoch moves wrong")
	}
}

func TestValidateRecords(t *testing.T) {
	if err := dir().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Direction){
		"no profile": func(d *Direction) { d.Profile = "" }, "gen 0": func(d *Direction) { d.Generation = 0 },
		"empty id": func(d *Direction) { d.ID = "" }, "long title": func(d *Direction) { d.Title = strings.Repeat("a", MaxTitleLen+1) },
		"control char": func(d *Direction) { d.Title = "a\x00b" }, "state": func(d *Direction) { d.State = "weird" },
		"neg epoch": func(d *Direction) { d.CreatedEpoch = -1 },
	} {
		d := dir()
		mut(&d)
		if d.Validate() == nil {
			t.Errorf("direction accepted: %s", name)
		}
	}
	if err := epoch().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Epoch){
		"trigger": func(e *Epoch) { e.Trigger = "x" }, "seq 0": func(e *Epoch) { e.Seq = 0 },
		"state": func(e *Epoch) { e.State = "x" }, "gen": func(e *Epoch) { e.Generation = 0 },
	} {
		e := epoch()
		mut(&e)
		if e.Validate() == nil {
			t.Errorf("epoch accepted: %s", name)
		}
	}
	p := Proposal{Title: "t", ProposerSession: "s"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Proposal){
		"empty title": func(p *Proposal) { p.Title = "" }, "no proposer": func(p *Proposal) { p.ProposerSession = "" },
		"long rationale": func(p *Proposal) { p.Rationale = strings.Repeat("a", MaxRationaleLen+1) },
		"many criteria":  func(p *Proposal) { p.CriterionIDs = make([]string, MaxProposalCrit+1) },
		"bad criterion":  func(p *Proposal) { p.CriterionIDs = []string{""} },
	} {
		q := p
		mut(&q)
		if q.Validate() == nil {
			t.Errorf("proposal accepted: %s", name)
		}
	}
}

var exp = Expect{CriterionIDs: []string{"c1", "c2"}, DirectionIDs: []string{"d1", "d2"}}

const goodBody = `{"criteria":[{"id":"c1","status":"plausibly-met"},{"id":"c2","status":"progressing","note":"n"}],` +
	`"directions":[{"id":"d1","verdict":"keep"},{"id":"d2","verdict":"prune"}],"next_actions":[{"direction_id":"d1","text":"do x"}]}`

func decode(t *testing.T, body string) (Judgment, error) {
	t.Helper()
	j, err := DecodeJudgment([]byte(body))
	if err != nil {
		return j, err
	}
	return j, j.Validate(exp)
}

func TestJudgmentAcceptsAWellFormedBody(t *testing.T) {
	j, err := decode(t, goodBody)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Criteria) != 2 || len(j.Directions) != 2 || len(j.NextActions) != 1 {
		t.Fatalf("decoded %+v", j)
	}
}

// Every malformed or hostile body is a bad judgment (ErrBadJudgment): the caller records it and changes nothing.
func TestJudgmentRefusesBadBodies(t *testing.T) {
	cases := map[string]string{
		"empty":              ``,
		"not json":           `nope`,
		"unknown field":      `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[],"finish":true}`,
		"trailing value":     goodBody + `{}`,
		"trailing garbage":   goodBody + ` x`,
		"missing criterion":  `{"criteria":[{"id":"c1","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"unknown criterion":  `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"},{"id":"c9","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"repeated criterion": `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c1","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"bad status":         `{"criteria":[{"id":"c1","status":"done"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"unknown direction":  `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[{"id":"zz","verdict":"keep"}],"next_actions":[]}`,
		"bad verdict":        `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[{"id":"d1","verdict":"kill"}],"next_actions":[]}`,
		"action unknown dir": `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[{"direction_id":"zz","text":"x"}]}`,
		"action empty text":  `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[{"text":""}]}`,
		"control char note":  `{"criteria":[{"id":"c1","status":"progressing","note":"a\u0000b"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"long note":          `{"criteria":[{"id":"c1","status":"progressing","note":"` + strings.Repeat("a", MaxNoteLen+1) + `"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"oversize":           `{"criteria":[],"directions":[],"next_actions":[],"x":"` + strings.Repeat("a", MaxJudgmentBytes) + `"}`,
	}
	for name, body := range cases {
		if _, err := decode(t, body); !errors.Is(err, ErrBadJudgment) {
			t.Errorf("%s: got %v, want ErrBadJudgment", name, err)
		}
	}
	many := Judgment{NextActions: make([]NextAction, MaxNextActions+1)}
	if err := many.Validate(exp); !errors.Is(err, ErrBadJudgment) {
		t.Errorf("too many next actions: %v", err)
	}
}

// AllPlausiblyMet is the reading the finish policy starts from. It is false for no criteria, a missing, repeated or
// unknown id, or any other status; a combination sweep pins it.
func TestAllPlausiblyMetSweep(t *testing.T) {
	statuses := []CriterionStatus{CritNotStarted, CritProgressing, CritPlausiblyMet, CritRegressed}
	ids := []string{"c1", "c2"}
	for _, s1 := range statuses {
		for _, s2 := range statuses {
			j := Judgment{Criteria: []CriterionProgress{{ID: "c1", Status: s1}, {ID: "c2", Status: s2}}}
			want := s1 == CritPlausiblyMet && s2 == CritPlausiblyMet
			if got := j.AllPlausiblyMet(ids); got != want {
				t.Errorf("%s,%s: got %v want %v", s1, s2, got, want)
			}
		}
	}
	if (Judgment{}).AllPlausiblyMet(nil) {
		t.Error("an empty judgment of an empty charter read as met")
	}
	met := Judgment{Criteria: []CriterionProgress{{ID: "c1", Status: CritPlausiblyMet}, {ID: "c2", Status: CritPlausiblyMet}}}
	if met.AllPlausiblyMet(nil) || met.AllPlausiblyMet([]string{}) {
		t.Error("a charter with no criteria read as met")
	}
	if met.AllPlausiblyMet([]string{"c1", "c2", "c3"}) {
		t.Error("an unjudged criterion read as met")
	}
	if met.AllPlausiblyMet([]string{"c1"}) {
		t.Error("a judgment of more criteria than the charter has read as met")
	}
	dup := Judgment{Criteria: []CriterionProgress{{ID: "c1", Status: CritPlausiblyMet}, {ID: "c1", Status: CritPlausiblyMet}}}
	if dup.AllPlausiblyMet([]string{"c1"}) {
		t.Error("a repeated criterion read as met")
	}
}

func TestJudgmentCanonicalIgnoresOrder(t *testing.T) {
	a, _ := decode(t, goodBody)
	b := Judgment{Criteria: []CriterionProgress{a.Criteria[1], a.Criteria[0]}, Directions: []DirectionAssessment{a.Directions[1], a.Directions[0]}, NextActions: a.NextActions}
	if !bytes.Equal(a.Canonical(), b.Canonical()) {
		t.Fatal("order of criteria or directions changed the canonical form")
	}
	b.Criteria[0].Status = CritRegressed
	if bytes.Equal(a.Canonical(), b.Canonical()) {
		t.Fatal("a changed status did not change the canonical form")
	}
}

func TestReservedKinds(t *testing.T) {
	for _, k := range AllKinds() {
		if !IsReserved(k) || !strings.HasPrefix(k, KindPrefix) {
			t.Errorf("%s is not reserved", k)
		}
	}
	if IsReserved("finish.begin") || IsReserved("approval.issue") || IsReserved("strategy") {
		t.Error("a non-strategy kind is reserved")
	}
	seen := map[string]bool{}
	for _, k := range AllKinds() {
		if seen[k] {
			t.Errorf("duplicate kind %s", k)
		}
		seen[k] = true
	}
}

// The owner's decisions are code constants an agent cannot lower.
func TestDecisionConstants(t *testing.T) {
	if MinEpochInterval.Minutes() != 5 || DefaultEpochInterval.Minutes() != 30 || MaxParkedFinishRounds != 5 || MaxUncheckedPerEpoch != 1 {
		t.Fatal("a settled decision changed")
	}
	if DefaultEpochInterval < MinEpochInterval {
		t.Fatal("the default cadence is under the floor")
	}
}

// The package is a plain library with no I/O, no process, no credential: its imports are pinned, and it has no init.
func TestPackageStaysPure(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	allowed := map[string]bool{"bytes": true, "crypto/sha256": true, "encoding/binary": true, "encoding/json": true, "errors": true,
		"fmt": true, "io": true, "sort": true, "strconv": true, "strings": true, "time": true}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if !allowed[p] {
				t.Errorf("%s imports %s", f, p)
			}
		}
		if bytes.Contains(src, []byte("func init(")) || bytes.Contains(src, []byte("go func")) {
			t.Errorf("%s has an init or a goroutine", f)
		}
	}
}
