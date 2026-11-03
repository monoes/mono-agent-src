package doomsday

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// ViewReader is an Engine that can give the control room its read-only snapshot of the profile's latest run. An
// Engine without it (Off, a build without the mode) is the mode off. store.ErrNoRun means the profile has no run.
type ViewReader interface {
	ReadView(ctx context.Context, q store.ViewQuery) (store.View, error)
}

// ErrUnreadable is why a read verb could not read at all (the database could not be opened or migrated). It is
// never the mode off: a run may be live while this is returned.
var ErrUnreadable = errors.New("the open-ended mode's database cannot be read")

// Unreadable is the engine of an install whose database cannot be opened: it is Off for every verb that acts (nothing
// starts), and for the control room's read verbs it is an error, not "off" (ErrUnreadable wrapping Err).
type Unreadable struct {
	Off
	Err error
}

// ReadView always fails with ErrUnreadable.
func (u Unreadable) ReadView(context.Context, store.ViewQuery) (store.View, error) {
	return store.View{}, fmt.Errorf("%w: %v", ErrUnreadable, u.Err)
}

// Reasons of an unanchored chain (ChainState.Reason while Verified and not Anchored).
const (
	// AnchorNoWitness: this install wired no witness to compare the head with.
	AnchorNoWitness = "no_witness"
	// AnchorNoRecord: the witness holds no committed record of the generation.
	AnchorNoRecord = "no_witness_record"
	// AnchorUnreadable: the witness file is missing, damaged or being written.
	AnchorUnreadable = "witness_unreadable"
)

// HeadAnchorFunc compares the audit head the store reported with an independent record. anchored true: they agree.
// Otherwise reason is an Anchor* reason (the record could not say) or store.ReasonBehindWitness /
// store.ReasonWitnessMismatch (the record says the chain is not what it was).
type HeadAnchorFunc func(generation int64, headSeq int64, headMAC []byte) (anchored bool, reason string)

// ReadView reads the profile's latest run through the store's read view; a store without one is the mode off. A
// chain that is consistent is "anchored" only when the witness agrees about its head: the run row does not bind the
// head, so a cut tail is consistent in itself.
func (e *StoreEngine) ReadView(ctx context.Context, q store.ViewQuery) (store.View, error) {
	rv, ok := e.Store.(store.ReadViewer)
	if !ok {
		return store.View{}, ErrOff
	}
	v, err := rv.ReadView(ctx, e.Profile, 0, q)
	if err != nil || !v.Chain.Verified {
		return v, err
	}
	if e.HeadAnchor == nil {
		v.Chain.Reason = AnchorNoWitness
		return v, nil
	}
	anchored, reason := e.HeadAnchor(v.Run.Generation, v.Chain.HeadSeq, v.Chain.HeadMAC)
	switch {
	case anchored:
		v.Chain.Anchored, v.Chain.Reason = true, ""
	case reason == store.ReasonBehindWitness || reason == store.ReasonWitnessMismatch:
		// The witness disagrees with the chain: nothing derived from this chain is reported.
		return store.View{Run: v.Run, Limits: v.Limits, Chain: store.ChainState{Reason: reason}}, nil
	default:
		v.Chain.Reason = reason
	}
	return v, nil
}

// CheckHeadAgainstWitness is the pure comparison of an audit head with the witness records of its generation. The
// witness records every chained transition before (pending) and after (committed) it is made, so the head may not be
// behind the last committed record, must carry its MAC when equal, and may be ahead of it only by the one transition
// a pending record announces.
func CheckHeadAgainstWitness(recs []stop.Record, gen, headSeq int64, headMAC []byte) (anchored bool, reason string) {
	lastC := -1
	for i, r := range recs {
		if r.Gen != gen {
			continue
		}
		switch r.T {
		case stop.RecFailed:
			return false, store.ReasonWitnessMismatch // a freshness check of this generation failed closed
		case stop.RecCommitted:
			if lastC < 0 || r.To >= recs[lastC].To {
				lastC = i
			}
		}
	}
	if lastC < 0 {
		return false, AnchorNoRecord
	}
	c := recs[lastC]
	switch {
	case headSeq < c.To:
		return false, store.ReasonBehindWitness
	case headSeq == c.To:
		if c.Head != hex.EncodeToString(headMAC) {
			return false, store.ReasonWitnessMismatch
		}
		return true, ""
	}
	for _, r := range recs[lastC+1:] { // rows past the last committed one: only the announced transition
		if r.T == stop.RecPending && r.Gen == gen && r.To == headSeq {
			return true, ""
		}
	}
	return false, store.ReasonWitnessMismatch
}

// headAnchor is the production HeadAnchorFunc over the witness file in the run directory; nil without a witness.
// It only reads: it takes no lock and writes nothing. A file caught mid-append reads as damaged, so it is read twice.
func (w Wiring) headAnchor() HeadAnchorFunc {
	if w.Witness == nil {
		return nil
	}
	wit := stop.Witness{Dir: w.RunDir, Keys: w.Witness.Keys}
	return func(gen, seq int64, mac []byte) (bool, string) {
		recs, err := wit.Records()
		if err != nil {
			if recs, err = wit.Records(); err != nil {
				return false, AnchorUnreadable
			}
		}
		return CheckHeadAgainstWitness(recs, gen, seq, mac)
	}
}

// ReadEngine is the engine of the read verbs only: the store and the witness comparison. It builds no sequencer, so a
// read opens no pinned connection and reads no secret store; every other verb of it refuses.
func (w Wiring) ReadEngine() *StoreEngine {
	return &StoreEngine{Store: w.Store, Profile: w.Profile, HeadAnchor: w.headAnchor()}
}
