package types

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBadReasonIsAFixedVocabularyNeverModelText(t *testing.T) {
	hostile := `‮evil\u001b[31m\u0085​KEY` // JSON escapes: the decoder hands the model's own characters on
	bodies := map[string]string{
		"unknown field":         `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[],"` + hostile + `":true}`,
		"unknown field nested":  `{"criteria":[{"id":"c1","status":"progressing","` + hostile + `":1},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"malformed json":        `{"criteria":`,
		"unknown criterion":     `{"criteria":[{"id":"` + hostile + `","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"bad status":            `{"criteria":[{"id":"c1","status":"` + hostile + `"},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[]}`,
		"bad verdict":           `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[{"id":"d1","verdict":"` + hostile + `"}],"next_actions":[]}`,
		"unknown direction":     `{"criteria":[{"id":"c1","status":"progressing"},{"id":"c2","status":"progressing"}],"directions":[{"id":"` + hostile + `","verdict":"keep"}],"next_actions":[]}`,
		"wrong criterion count": `{"criteria":[{"id":"c1","status":"progressing"}],"directions":[],"next_actions":[]}`,
	}
	for want, body := range bodies {
		_, err := decode(t, body)
		got := BadReason(err)
		if got == "" || strings.ContainsAny(got, "‮\x1b\u0085​") || strings.Contains(got, "KEY") || strings.Contains(got, "c1") {
			t.Errorf("%s: reason %q carries model text", want, got)
		}
		if strings.HasPrefix(want, "unknown field") {
			want = "unknown field"
		}
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	// Anything that is not one of this package's own refusals says nothing of its text.
	if got := BadReason(fmt.Errorf("%w: model said KEY", ErrBadJudgment)); got != "judgment refused" {
		t.Errorf("a foreign refusal leaked: %q", got)
	}
	if BadReason(errors.New("a secret model body")) != "" || BadReason(nil) != "" {
		t.Error("an error that is not a refusal must carry no text")
	}
}

func TestCleanReasonStripsControlsAndFormatCharsAndBounds(t *testing.T) {
	got := CleanReason("a‮b\u0085c\x1b[d​e" + strings.Repeat("x", 500))
	if len([]rune(got)) != MaxBadReasonLen || !strings.HasPrefix(got, "abc[dexx") {
		t.Errorf("got %q", got)
	}
}

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
	id, again := EpochID("p", 2, 7), EpochID("p", 2, 7)
	if id != again || id == EpochID("p", 3, 7) || id == EpochID("p", 2, 8) {
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

// The judge may report how many of a counted criterion are done; it is optional, and a zero is a real reading.
func TestJudgmentCarriesAnOptionalCriterionCount(t *testing.T) {
	j, err := decode(t, `{"criteria":[{"id":"c1","status":"progressing","count":12},{"id":"c2","status":"progressing","count":0}],"directions":[],"next_actions":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if j.Criteria[0].Count == nil || *j.Criteria[0].Count != 12 || j.Criteria[1].Count == nil || *j.Criteria[1].Count != 0 {
		t.Fatalf("%+v", j.Criteria)
	}
	a, _ := decode(t, goodBody)
	if a.Criteria[0].Count != nil {
		t.Fatal("a body without a count decoded one")
	}
	b := a
	b.Criteria = append([]CriterionProgress(nil), a.Criteria...)
	n := 3
	b.Criteria[0].Count = &n
	if bytes.Equal(a.Canonical(), b.Canonical()) {
		t.Fatal("a reported count did not change the canonical form")
	}
}

// The count is advisory: one that is out of range or oddly typed is dropped (no reading), never a refusal of the
// judgment, and the rest of the judgment is still strictly checked.
func TestAnAdvisoryCountNeverVoidsAJudgment(t *testing.T) {
	for _, c := range []string{`-1`, `1000000001`, `12.0`, `1e2`, `1.5`, `"12"`, `null`, `true`, `[1]`, `{"a":1}`, `99999999999999999999`} {
		j, err := decode(t, `{"criteria":[{"id":"c1","status":"progressing","count":`+c+`},{"id":"c2","status":"progressing","count":4}],"directions":[],"next_actions":[]}`)
		if err != nil {
			t.Fatalf("count %s voided the judgment: %v", c, err)
		}
		if j.Criteria[0].Count != nil {
			t.Errorf("count %s was kept: %d", c, *j.Criteria[0].Count)
		}
		if j.Criteria[1].Count == nil || *j.Criteria[1].Count != 4 {
			t.Errorf("count %s disturbed its sibling: %+v", c, j.Criteria[1])
		}
	}
	// Everything else stays strict, with or without a count.
	if _, err := decode(t, `{"criteria":[{"id":"c1","status":"progressing","count":"x","bogus":1},{"id":"c2","status":"progressing"}],"directions":[],"next_actions":[]}`); !errors.Is(err, ErrBadJudgment) {
		t.Errorf("an unknown field beside a bad count was accepted: %v", err)
	}
	// The bounds are inclusive.
	j, err := decode(t, `{"criteria":[{"id":"c1","status":"progressing","count":1000000000},{"id":"c2","status":"progressing","count":0}],"directions":[],"next_actions":[]}`)
	if err != nil || j.Criteria[0].Count == nil || *j.Criteria[0].Count != MaxCount || j.Criteria[1].Count == nil {
		t.Errorf("in-range counts dropped: %+v %v", j, err)
	}
}

// The canonical bytes of a judgment with no count are frozen: stored judgments and their MACs must keep verifying.
func TestCanonicalOfAJudgmentWithoutACountIsFrozen(t *testing.T) {
	j, err := decode(t, goodBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(j.Canonical()); got != goldenNoCountCanonical {
		t.Fatalf("canonical form changed:\n got %s\nwant %s", got, goldenNoCountCanonical)
	}
}

const goldenNoCountCanonical = "2f6d6f6e6f6167656e742f646f6f6d736461792f73747261746567792f6a7564676d656e742f76312f6465636f646564000000000000000100000000000000020263310d706c61757369626c792d6d6574000263320b70726f6772657373696e67016e0000000000000002026431046b65657000026432057072756e6500000000000000000102643104646f2078"

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
		"fmt": true, "io": true, "sort": true, "strconv": true, "strings": true, "time": true, "unicode": true}
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
