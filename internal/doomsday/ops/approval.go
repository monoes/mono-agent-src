package ops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Typed approvals bound to the exact effect (note 13.4 sections 3, 4 and 6). The approval record itself is the
// store's insert-only row (MAC checked when it is read); this package re-derives the hash of the actual effect,
// compares it, and spends the approval once through the Ledger.

var (
	ErrOutsideConstraints = errors.New("doomsday ops: the request is outside the charter's constraints")
	ErrArgsMismatch       = errors.New("doomsday ops: the effect is not the approved one")
	ErrOpMismatch         = errors.New("doomsday ops: the approval is for another operation")
	ErrRevoked            = errors.New("doomsday ops: the approval was rescinded")
	ErrExpired            = errors.New("doomsday ops: the approval expired")
	ErrOverLimit          = errors.New("doomsday ops: a counter of the run is used up")
	ErrUnknownApproval    = errors.New("doomsday ops: no such approval")
)

// ApprovalRecord is the part of the store's approval row the enforcement function compares.
type ApprovalRecord struct {
	ID          string
	Profile     string
	CharterID   string
	CharterHash string
	Generation  int64
	Op          string
	ArgsHash    string
	Caller      int64 // the session the approval is addressed to
	Revoked     bool
	ExpiresSeq  int64
}

// Approvals reads approval rows (the store verifies the MAC and returns an error for one that does not verify) and
// the current head of the audit chain, in transitions, which is what expires_seq is compared with.
type Approvals interface {
	Get(ctx context.Context, id string) (ApprovalRecord, error)
	Seq(ctx context.Context, profile string) (int64, error)
}

// SendArgs are the resolved arguments of send.third_party: the destination and the hash of the canonical content.
type SendArgs struct {
	Platform    string `json:"platform"`
	Account     string `json:"account"`
	Destination string `json:"destination"`
	ContentHash string `json:"content_hash"`
}

// VaultArgs are the resolved request of vault.use as it will be sent, with the injection point of the secret.
type VaultArgs struct {
	Secret   string      `json:"secret"`
	Method   string      `json:"method"`
	URL      string      `json:"url"`
	Headers  [][2]string `json:"headers"` // every header that will be sent, secret excluded
	BodyHash string      `json:"body_hash"`
	Inject   string      `json:"inject"` // the header name or body field that receives the secret
}

// LocalArgs are the exact resolved target set of local.destructive, each relative to the root's directory identity.
type LocalArgs struct {
	Root        string   `json:"root"`
	Verb        string   `json:"verb"`
	Targets     []string `json:"targets"`
	ContentHash string   `json:"content_hash"` // overwrite only
	Bytes       int64    `json:"bytes"`
}

// SettingArgs are the key and exact value of host.setting.
type SettingArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// TypedRequest asks to perform one typed effect. Exactly the field of Op is set. Caller comes from the verified token.
type TypedRequest struct {
	Profile    string
	CharterID  string
	Hash       string
	Generation int64
	Op         string
	ApprovalID string
	Caller     Caller
	Send       *SendArgs
	Vault      *VaultArgs
	Local      *LocalArgs
	Setting    *SettingArgs
}

// Authorization is the proof that Authorize spent an approval for exactly this effect. Its fields are unexported and only
// Authorize sets them; the functions that perform an effect take one. (A zero value can still be written inside this
// package, so an inventory test, not the type, is the guarantee.)
type Authorization struct {
	op, hash, id string
	vault        *VaultUse
}

func (a Authorization) Op() string       { return a.op }
func (a Authorization) ArgsHash() string { return a.hash }
func (a Authorization) ID() string       { return a.id }

// refusalAuditCap bounds the refusal rows written per generation, so refusals cannot flood the audit stream.
const refusalAuditCap = 200

// TypedEnforcer is the enforcement function of the typed operations of typed.go.
type TypedEnforcer struct {
	Charters  Charters
	Approvals Approvals
	Ledger    Ledger
	Auditor   Auditor
	Bounds    SpendBounds

	mu       sync.Mutex
	refusals map[int64]int
}

// Authorize checks a request for a typed operation and spends its approval, or refuses. Order: allowlist, live
// charter, constraints enabled, request inside the constraints (before the approval is read), then the approval:
// profile, generation, charter hash, operation, caller from the token, not rescinded, not expired, hash of the
// re-canonicalised actual effect; and last the single atomic consume.
func (e *TypedEnforcer) Authorize(ctx context.Context, r TypedRequest) (Authorization, error) {
	if !AllowedTyped(r.Op) {
		return Authorization{}, ErrNotAllowlisted
	}
	act, ok, err := e.Charters.Active(ctx, r.Profile)
	if err != nil {
		return Authorization{}, err
	}
	if !ok {
		return Authorization{}, ErrNoCharter
	}
	if act.Hash == "" || act.Hash != r.Hash || act.ID != r.CharterID || act.Generation != r.Generation || act.Profile != r.Profile {
		return Authorization{}, ErrCharterStale
	}
	if err := act.Constraints.Validate(e.Bounds); err != nil {
		return Authorization{}, fmt.Errorf("%w (%v)", ErrNotEnabled, err)
	}
	if r.Caller.Session == 0 || r.ApprovalID == "" {
		return Authorization{}, ErrCallerMismatch
	}
	canon, spends, vault, err := typedEffect(act.Constraints, r)
	if err != nil {
		e.refuse(ctx, act, r, "outside-constraints", "", err)
		return Authorization{}, err
	}
	hash, err := ArgsHash(r.Op, canon)
	if err != nil {
		return Authorization{}, err
	}
	rec, err := e.Approvals.Get(ctx, r.ApprovalID)
	if err != nil {
		return Authorization{}, err
	}
	if err := e.verify(ctx, act, r, rec, hash); err != nil {
		e.refuse(ctx, act, r, "approval", rec.ArgsHash, err)
		return Authorization{}, err
	}
	res, err := e.Ledger.Consume(ctx, Consume{Profile: r.Profile, CharterID: act.ID, Generation: act.Generation, ApprovalID: r.ApprovalID,
		Spends: spends, Audit: AuditRow{Profile: r.Profile, CharterID: act.ID, Generation: act.Generation, Op: r.Op, ApprovalID: r.ApprovalID, Result: "approved", ArgsHash: hash}})
	switch {
	case err != nil:
		return Authorization{}, err
	case res.Replay:
		return Authorization{}, ErrAlreadyConsumed
	case !res.Done:
		return Authorization{}, fmt.Errorf("%w: %v", ErrOverLimit, res.Over)
	}
	return Authorization{op: r.Op, hash: hash, id: r.ApprovalID, vault: vault}, nil
}

func (e *TypedEnforcer) verify(ctx context.Context, act Active, r TypedRequest, rec ApprovalRecord, hash string) error {
	switch {
	case rec.ID != r.ApprovalID:
		return ErrUnknownApproval
	case rec.Profile != act.Profile || rec.Generation != act.Generation || rec.CharterHash != act.Hash || rec.CharterID != act.ID:
		return ErrCharterStale
	case rec.Op != r.Op:
		return ErrOpMismatch
	case rec.Caller == 0 || rec.Caller != r.Caller.Session:
		return ErrCallerMismatch
	case rec.Revoked:
		return ErrRevoked
	}
	seq, err := e.Approvals.Seq(ctx, r.Profile)
	if err != nil {
		return err
	}
	if seq > rec.ExpiresSeq {
		return ErrExpired
	}
	if rec.ArgsHash != hash {
		return ErrArgsMismatch
	}
	return nil
}

// refuse audits a refusal (hashes only, never values), at a capped rate per generation. A failed audit append does
// not turn the refusal into anything else: the request is refused either way.
func (e *TypedEnforcer) refuse(ctx context.Context, act Active, r TypedRequest, why, approved string, cause error) {
	e.mu.Lock()
	if e.refusals == nil {
		e.refusals = map[int64]int{}
	}
	e.refusals[act.Generation]++
	over := e.refusals[act.Generation] > refusalAuditCap
	e.mu.Unlock()
	if over || e.Auditor == nil {
		return
	}
	_ = e.Auditor.Append(ctx, AuditRow{Profile: r.Profile, CharterID: act.ID, Generation: act.Generation, Op: r.Op, ApprovalID: r.ApprovalID,
		Result: "refused:" + why, Exceeded: []string{cause.Error()}, ArgsHash: approved})
}

// typedEffect re-canonicalises the actual effect, checks it against the charter's constraints for its operation and
// returns the canonical form (to hash), the counters to spend, and, for vault.use, the constraints it ran under.
func typedEffect(c Constraints, r TypedRequest) (any, []Spend, *VaultUse, error) {
	out := func(f string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrOutsideConstraints, fmt.Sprintf(f, a...))
	}
	switch r.Op {
	case OpSendThirdParty:
		s, a := c.SendThirdParty, r.Send
		if s == nil || a == nil {
			return nil, nil, nil, ErrNotEnabled
		}
		dest, err := CanonHandle(a.Platform, a.Destination)
		if err != nil {
			return nil, nil, nil, out("%v", err)
		}
		if a.Platform != s.Platform || a.Account != s.Account || a.ContentHash == "" {
			return nil, nil, nil, out("platform, account or content")
		}
		if !slices.Contains(s.Destinations, Any) && !destListed(s, dest) {
			return nil, nil, nil, out("destination")
		}
		return SendArgs{a.Platform, a.Account, dest, a.ContentHash}, []Spend{{Kind: KindSends, N: 1, Cap: s.MaxPerRun}}, nil, nil
	case OpVaultUse:
		return vaultEffect(c.VaultUse, r.Vault)
	case OpLocalDestructive:
		l, a := c.LocalDestructive, r.Local
		if l == nil || a == nil {
			return nil, nil, nil, ErrNotEnabled
		}
		if !slices.Contains(l.Roots, a.Root) || !slices.Contains(l.Verbs, a.Verb) || len(a.Targets) == 0 || a.Bytes < 0 || (a.Verb == "overwrite") != (a.ContentHash != "") {
			return nil, nil, nil, out("root, verb or target set")
		}
		ts := make([]string, 0, len(a.Targets))
		for _, t := range a.Targets {
			c, err := CanonRelPath(t)
			if err != nil {
				return nil, nil, nil, out("%v", err)
			}
			ts = append(ts, c)
		}
		slices.Sort(ts)
		if len(slices.Compact(slices.Clone(ts))) != len(ts) {
			return nil, nil, nil, out("a target is listed twice")
		}
		return LocalArgs{a.Root, a.Verb, ts, a.ContentHash, a.Bytes},
			[]Spend{{Kind: KindLocalCount, N: int64(len(ts)), Cap: l.MaxCount}, {Kind: KindLocalBytes, N: a.Bytes, Cap: l.MaxBytes}}, nil, nil
	default:
		h, a := c.HostSetting, r.Setting
		if h == nil || a == nil {
			return nil, nil, nil, ErrNotEnabled
		}
		if slices.Contains(NeverLiftableSettings, a.Key) {
			return nil, nil, nil, out("%q is never liftable", a.Key)
		}
		vals, ok := h.Keys[a.Key]
		if !ok || !(slices.Contains(vals, Any) || slices.Contains(vals, a.Value)) {
			return nil, nil, nil, out("setting or value")
		}
		return *a, nil, nil, nil
	}
}

func destListed(s *SendThirdParty, canon string) bool {
	for _, d := range s.Destinations {
		if c, err := CanonHandle(s.Platform, d); err == nil && c == canon {
			return true
		}
	}
	return false
}

// vaultEffect canonicalises the resolved request: https only, no userinfo or port, a host the secret may go to, the
// secret named in the list, headers lowercased and sorted. The host evaluates nothing from the agent.
func vaultEffect(v *VaultUse, a *VaultArgs) (any, []Spend, *VaultUse, error) {
	if v == nil || a == nil {
		return nil, nil, nil, ErrNotEnabled
	}
	out := func(f string, x ...any) error {
		return fmt.Errorf("%w: vault.use %s", ErrOutsideConstraints, fmt.Sprintf(f, x...))
	}
	if !slices.Contains(v.Secrets, a.Secret) {
		return nil, nil, nil, out("secret")
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return nil, nil, nil, out("url")
	}
	host, err := CanonHost(u.Hostname())
	if err != nil || !HostAllowed(v.Hosts, host) {
		return nil, nil, nil, out("host")
	}
	if a.Inject == "" || a.BodyHash == "" || !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}, a.Method) {
		return nil, nil, nil, out("method, body or injection point")
	}
	hs := make([][2]string, 0, len(a.Headers))
	for _, h := range a.Headers {
		hs = append(hs, [2]string{strings.ToLower(h[0]), h[1]})
	}
	slices.SortFunc(hs, func(x, y [2]string) int { return strings.Compare(x[0]+"\x00"+x[1], y[0]+"\x00"+y[1]) })
	u.Host = host
	return VaultArgs{a.Secret, a.Method, u.String(), hs, a.BodyHash, a.Inject}, []Spend{{Kind: KindVaultAttempts, N: 1, Cap: v.MaxAttempts}}, v, nil
}
