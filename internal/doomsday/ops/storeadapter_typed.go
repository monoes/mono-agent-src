package ops

import (
	"context"
	"errors"
	"slices"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// typedConstraints adds the charter's typed sections to out. A nil section enables nothing; the lists are copied, so
// the constraints never alias the loaded charter.
func typedConstraints(out Constraints, t store.Typed) Constraints {
	if s := t.Send; s != nil {
		out.SendThirdParty = &SendThirdParty{Platform: s.Platform, Account: s.Account, Destinations: slices.Clone(s.Destinations), MaxPerRun: s.MaxPerRun, ContentClass: s.ContentClass}
	}
	if v := t.Vault; v != nil {
		out.VaultUse = &VaultUse{Secrets: slices.Clone(v.Secrets), Hosts: slices.Clone(v.Hosts), MaxAttempts: v.MaxAttempts, BucketSize: v.BucketSize,
			BucketCount: v.BucketCount, MaxCallMS: v.MaxCallMS, SlotMS: v.SlotMS, Predicate: v.Predicate}
	}
	if l := t.Local; l != nil {
		out.LocalDestructive = &LocalDestructive{Roots: slices.Clone(l.Roots), Verbs: slices.Clone(l.Verbs), MaxCount: l.MaxCount, MaxBytes: l.MaxBytes}
	}
	if h := t.Settings; h != nil {
		keys := make(map[string][]string, len(h.Keys))
		for k, vs := range h.Keys {
			keys[k] = slices.Clone(vs)
		}
		out.HostSetting = &HostSetting{Keys: keys}
	}
	return out
}

// EntryCheck is the charter check the entry runs (store.SQLStore.WithEntryCheck): the constraints this package enforces
// must validate under the host's spend bounds, so a charter that would only ever deny is refused whole at entry (note
// 13.4 section 4B). It builds the constraints exactly as Active does.
func EntryCheck(b SpendBounds) func(store.Charter) error {
	return func(c store.Charter) error {
		cons := StoreAdapter{Bounds: b}.constraints(c)
		return cons.Validate(b)
	}
}

// StoreApprovals satisfies Approvals over the store's approval rows.
type StoreApprovals struct {
	Store store.ApprovalStore
}

var _ Approvals = StoreApprovals{}

// Get reads an approval after the store has verified its chain; an unreadable one is an error, never "absent".
func (a StoreApprovals) Get(ctx context.Context, id string) (ApprovalRecord, error) {
	r, err := a.Store.GetApproval(ctx, id)
	if errors.Is(err, store.ErrApprovalUnknown) {
		return ApprovalRecord{}, ErrUnknownApproval
	}
	if err != nil {
		return ApprovalRecord{}, err
	}
	return ApprovalRecord{ID: r.ID, Profile: r.Profile, CharterID: r.CharterID, CharterHash: r.CharterHash, Generation: r.Generation,
		Op: r.Op, ArgsHash: r.ArgsHash, Caller: r.Caller, Revoked: r.Revoked, ExpiresSeq: r.ExpiresSeq}, nil
}

// Seq is the audit head of the profile's latest generation, in transitions.
func (a StoreApprovals) Seq(ctx context.Context, profile string) (int64, error) {
	return a.Store.ApprovalSeq(ctx, profile)
}
