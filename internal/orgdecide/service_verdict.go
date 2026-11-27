package orgdecide

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

type recordFunc func(resolver, verdict, answer, rationale string, cost *float64, latency *int64) (*Decision, error)

// fail applies the decider-failure rule: mid hands the item to a person;
// full denies it (telling the requester why) unless on_decider_failure is
// human (C-52). An item left to a person waits on them (it isn't retried
// at this level), so a HIL one shows in the org's bubble (toPerson).
func (s *Service) fail(ctx context.Context, root, org, level string, a *Autonomy, it Item, record recordFunc, resolver, reason string, cost *float64, lat *int64) (*Decision, error) {
	if level == orgdesign.LevelFull && a.OnDeciderFailure == "deny" {
		text := ""
		if it.Kind == KindQuestion {
			text = fmt.Sprintf("No decision could be made (%s). Continue without this, or stop.", reason)
		}
		if err := s.apply(ctx, root, org, it, false, text, resolver, "decider failed: "+reason); err != nil {
			return nil, err
		}
		// A question's answer and a gate's resolution already carry the
		// reason; monomind's approve/deny carries nothing, so the requester
		// only sees "DENIED" unless it is also messaged (C-52).
		if it.Kind == KindApproval && it.Requester != "" {
			body := fmt.Sprintf("[decided by %s] %q was denied: no decision could be made (%s). Continue without it, or stop.", resolver, it.Action, reason)
			if err := s.Client.Notify(ctx, root, org, it.Requester, "denied: "+it.Action, body); err != nil {
				s.Logf("orgdecide: telling %s why %s was denied: %v", it.Requester, it.Action, err)
			}
		}
		return record(resolver, VerdictFailed, "", reason, cost, lat)
	}
	return s.toPerson(ctx, a.ProfileID, org, it)(record(resolver, VerdictFailed, "", reason, cost, lat))
}

// apply resolves an item in monomind. Resolution text starts with
// "[decided by <resolver>]" so the requesting role sees who decided even
// without M5; with M5 the resolver is also passed as --by.
func (s *Service) apply(ctx context.Context, root, org string, it Item, approve bool, answer, resolver, rationale string) error {
	return ApplyVerdict(ctx, s.Client, s.DB, root, org, it, approve, answer, resolver, rationale)
}

// noRequestID is why it must not be approved, nil when it may be: an approval
// without a request id would release every pending call of its role and
// action, one queued since the decider looked included. Denying it passes.
func noRequestID(org string, it Item) error {
	if it.Kind != KindApproval || it.RequestID != "" {
		return nil
	}
	return &monomind.NoRequestIDError{Org: org, Role: it.Requester, Action: it.Action}
}

// ApplyVerdict resolves one item for resolver: org items in monomind, HIL
// items in mono-agent's own queue. It never approves an approval without a
// request id (monomind.NoRequestIDError).
func ApplyVerdict(ctx context.Context, c Client, db *sql.DB, root, org string, it Item, approve bool, answer, resolver, rationale string) error {
	opts := monomind.ResolveOptions{By: resolver, RequestID: it.RequestID}
	prefix := "[decided by " + resolver + "] "
	switch it.Kind {
	case KindApproval:
		if err := noRequestID(org, it); approve && err != nil {
			return err
		}
		return c.Approve(ctx, root, org, it.Requester, it.Action, approve, opts)
	case KindGate:
		return c.Gate(ctx, root, org, it.Ref, approve, prefix+rationale, opts)
	case KindQuestion:
		text := answer
		if text == "" {
			text = rationale
		}
		return c.Answer(ctx, root, org, it.Ref, prefix+text, opts)
	case KindHIL:
		if db == nil {
			return fmt.Errorf("orgdecide: no database to resolve HIL item %s", it.Ref)
		}
		return resolveHIL(ctx, db, it.Ref, approve)
	}
	return fmt.Errorf("orgdecide: cannot resolve %s items", it.Kind)
}
