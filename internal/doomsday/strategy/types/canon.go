package types

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
)

// CanonVersion is part of every hashed byte string, so a change of the rules changes every hash.
const CanonVersion = 1

// Domains separate the encodings: bytes meant for one record kind never hash or MAC equal to another's.
const (
	domainKey       = "monoagent/doomsday/strategy/key/v1"
	domainDirection = "monoagent/doomsday/strategy/direction/v1"
	domainEpoch     = "monoagent/doomsday/strategy/epoch/v1"
	domainJudgment  = "monoagent/doomsday/strategy/judgment/v1"
	domainNovelty   = "monoagent/doomsday/strategy/novelty/v1"
)

type enc struct{ b []byte }

func newEnc(domain string) *enc {
	e := &enc{}
	e.str(domain)
	e.num(CanonVersion)
	return e
}
func (e *enc) str(s string)   { e.b = binary.AppendUvarint(e.b, uint64(len(s))); e.b = append(e.b, s...) }
func (e *enc) num(n int64)    { e.b = binary.BigEndian.AppendUint64(e.b, uint64(n)) }
func (e *enc) raw(h [32]byte) { e.b = append(e.b, h[:]...) }
func (e *enc) flag(v bool) {
	if v {
		e.b = append(e.b, 1)
	} else {
		e.b = append(e.b, 0)
	}
}

// KeyHash hashes an already normalised proposal key (the novelty package owns the normalisation).
func KeyHash(normalised string) [32]byte {
	e := newEnc(domainKey)
	e.str(normalised)
	return sha256.Sum256(e.b)
}

// BodyHash hashes the raw judgment body bytes.
func BodyHash(body []byte) [32]byte {
	e := newEnc(domainJudgment + "/body")
	e.str(string(body))
	return sha256.Sum256(e.b)
}

// EpochID is the idempotent identity of an epoch: the same run generation and sequence always give the same id.
func EpochID(profile string, generation, seq int64) string {
	return profile + "/" + strconv.FormatInt(generation, 10) + "/e" + strconv.FormatInt(seq, 10)
}

// Canonical is the encoding the store MACs. Every field is in it, so changing any field changes the MAC.
func (d Direction) Canonical() []byte {
	e := newEnc(domainDirection)
	e.str(d.Profile)
	e.num(d.Generation)
	e.str(d.ID)
	e.raw(d.KeyHash)
	e.str(d.Title)
	e.str(string(d.State))
	e.str(d.ProposerSession)
	e.str(d.SessionID)
	e.num(d.CreatedEpoch)
	return e.b
}

// Canonical is the encoding the store MACs.
func (x Epoch) Canonical() []byte {
	e := newEnc(domainEpoch)
	e.str(x.Profile)
	e.num(x.Generation)
	e.num(x.Seq)
	e.str(string(x.Trigger))
	e.num(x.StartedAt)
	e.flag(x.HasJudgment)
	e.raw(x.JudgmentHash)
	e.str(x.ModelPin)
	e.str(string(x.State))
	return e.b
}

// Canonical is the encoding the store MACs. The body is bound by its hash and its bytes.
func (j JudgmentRecord) Canonical() []byte {
	e := newEnc(domainJudgment)
	e.str(j.Profile)
	e.num(j.Generation)
	e.num(j.EpochSeq)
	e.raw(j.BodyHash)
	e.str(string(j.Body))
	return e.b
}

// Canonical is the encoding of a ledger row.
func (n NoveltyRow) Canonical() []byte {
	e := newEnc(domainNovelty)
	e.str(n.Profile)
	e.num(n.Generation)
	e.num(n.EpochSeq)
	e.raw(n.KeyHash)
	e.str(n.DirectionID)
	e.str(n.MatchedID)
	e.str(string(n.Outcome))
	return e.b
}

// Canonical is the order-independent encoding of a decoded judgment (lists are sorted by id), used to compare two
// judgments that differ only in order.
func (j Judgment) Canonical() []byte {
	e := newEnc(domainJudgment + "/decoded")
	cs := append([]CriterionProgress(nil), j.Criteria...)
	sort.Slice(cs, func(a, b int) bool { return cs[a].ID < cs[b].ID })
	e.num(int64(len(cs)))
	for _, c := range cs {
		e.str(c.ID)
		e.str(string(c.Status))
		e.str(c.Note)
	}
	ds := append([]DirectionAssessment(nil), j.Directions...)
	sort.Slice(ds, func(a, b int) bool { return ds[a].ID < ds[b].ID })
	e.num(int64(len(ds)))
	for _, d := range ds {
		e.str(d.ID)
		e.str(string(d.Verdict))
		e.str(d.Note)
	}
	e.num(int64(len(j.NextActions)))
	for _, a := range j.NextActions {
		e.str(a.DirectionID)
		e.str(a.Text)
	}
	return e.b
}

// Validate checks a record before it is stored. It refuses what the database would also refuse, so the error is
// readable instead of a trigger abort.
func (d Direction) Validate() error {
	switch {
	case d.Profile == "" || d.Generation < 1:
		return fmt.Errorf("strategy: direction needs a profile and a generation")
	case !validID(d.ID):
		return fmt.Errorf("strategy: bad direction id")
	case !validText(d.Title, MaxTitleLen, true):
		return fmt.Errorf("strategy: bad direction title")
	case !validDirState(d.State):
		return fmt.Errorf("strategy: bad direction state %q", d.State)
	case d.CreatedEpoch < 0:
		return fmt.Errorf("strategy: bad created epoch")
	}
	return nil
}

// Validate checks an epoch before it is stored.
func (x Epoch) Validate() error {
	switch x.Trigger {
	case TriggerTimer, TriggerDirectionFinished, TriggerDirectionStalled, TriggerBudgetShare, TriggerNewEvidence, TriggerFinishParked:
	default:
		return fmt.Errorf("strategy: bad epoch trigger %q", x.Trigger)
	}
	switch {
	case x.Profile == "" || x.Generation < 1 || x.Seq < 1:
		return fmt.Errorf("strategy: epoch needs a profile, a generation and a sequence from 1")
	case x.State != EpochOpen && x.State != EpochJudged && x.State != EpochApplied && x.State != EpochBadJudgment && x.State != EpochAbandoned:
		return fmt.Errorf("strategy: bad epoch state %q", x.State)
	case !validText(x.ModelPin, MaxIDLen*2, true):
		return fmt.Errorf("strategy: bad model pin")
	}
	return nil
}
