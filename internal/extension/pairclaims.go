package extension

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Pending pairing claims (`extension.autoPair` = ask).
//
// A claim from a browser extension is not a credential: its Origin header is
// a string any local process can send. In ask mode the bridge therefore only
// records a PENDING claim here and answers with a claim id and a poll secret;
// a token exists only after the owner approves the claim through a channel a
// browser or a script cannot reach (`monoagentcli extension approve`, or the
// card in the Human in Loop queue, which decides through the same code). The
// extension polls with the secret; the poll that sees "approved" mints the
// token once and deletes the record.
//
// The file is shared by the bridge and the CLI (different processes) and
// holds only a SHA-256 of the poll secret, never a token.

// Claim states.
const (
	ClaimPending  = "pending"
	ClaimApproved = "approved"
	ClaimDenied   = "denied"
)

const (
	// PendingClaimTTL is how long a claim waits for the owner.
	PendingClaimTTL = 2 * time.Minute
	// maxPendingClaims bounds the file and the owner's queue. A full queue
	// refuses new claims; it never drops a waiting one. (The origin is
	// whatever the claimant sends, so there is no per-origin share; the limit
	// is one waiting claim per instance id, and `extension deny --all` clears
	// the queue.)
	maxPendingClaims = 5
	// maxClaimRecords bounds the file: decided tombstones do not count toward
	// maxPendingClaims, but the oldest are dropped past this many records.
	maxClaimRecords = 3 * maxPendingClaims
	// maxCodeStrikes wrong confirmation codes destroy a pending claim.
	maxCodeStrikes = 5
	// PairCardKind marks a HIL item that is a pairing claim.
	PairCardKind = "extension.pair"
	// PairClaimWording is what the card tells the owner they are granting.
	PairClaimWording = "claims access to your documents, vault, tasks and page control"
)

var (
	// ErrClaimsFull: no free slot for a new pending claim.
	ErrClaimsFull = errors.New("too many pairing requests are waiting for approval; approve or deny them, or wait for them to expire")
	// ErrNoSuchClaim: no pending claim by that id (or it expired).
	ErrNoSuchClaim = errors.New("no such pending pairing claim")
	// ErrClaimExpired: the claim waited longer than PendingClaimTTL.
	ErrClaimExpired = errors.New("this pairing claim expired")
)

// PendingClaim is one browser waiting for the owner.
type PendingClaim struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Origin     string `json:"origin"`
	Agent      string `json:"agent,omitempty"`
	Instance   string `json:"instance,omitempty"`
	SecretHash string `json:"secretHash"`
	// CodeHash is the hash of the confirmation code the browser shows (see
	// PairCode); the owner types the code they SEE in their browser to approve.
	CodeHash string `json:"codeHash,omitempty"`
	Status   string `json:"status"`
	// Strikes counts wrong confirmation codes; at maxCodeStrikes the claim is
	// destroyed (denied).
	Strikes int `json:"strikes,omitempty"`
	// DecidedAt is the tombstone: a claim the owner decided stays in the file,
	// decided, until its browser collects the outcome or it expires, and a
	// decided claim is never decided again or turned back to pending.
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	// Restricted and AllowedProfiles are what the owner limited the browser to
	// when approving (allowed_profiles.go); they become its paired record.
	Restricted      bool     `json:"restricted,omitempty"`
	AllowedProfiles []string `json:"allowedProfiles,omitempty"`
	// Unrestricted is the owner's explicit choice (`approve --any`). Without
	// it an approved claim mints a limited record, whatever else the file says.
	Unrestricted bool `json:"unrestricted,omitempty"`
}

// codeAlphabet has no 0/O/1/I/L: the code is read off a screen and typed.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// PairCode is the short code a browser shows while its request waits, derived
// from the claim id and the poll secret (so only the browser that holds the
// secret can show it): the first six characters of SHA-256(id || secret),
// as ABC-123. The owner approves with the code their own browser displays, so
// an approval cannot land on a forged claim whose code they never saw.
func PairCode(claimID, secret string) string {
	sum := sha256.Sum256([]byte(claimID + secret))
	out := make([]byte, 0, 7)
	for i := 0; i < 6; i++ {
		if i == 3 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(sum[i])%len(codeAlphabet)])
	}
	return string(out)
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

func codeDigest(claimID, code string) string { return hashToken(claimID + ":" + normalizeCode(code)) }

func pendingPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".monoagent", "extension.pending.json"), nil
}

func readPendingLocked() ([]PendingClaim, error) {
	path, err := pendingPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pending claims: %w", err)
	}
	var list []PendingClaim
	if err := json.Unmarshal(data, &list); err != nil {
		// A damaged file holds nothing the owner approved: start over.
		return nil, nil
	}
	return list, nil
}

func writePendingLocked(list []PendingClaim) error {
	path, err := pendingPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write pending claims: %w", err)
	}
	return os.Rename(tmp, path)
}

// liveOnly drops expired claims, returning them separately.
func liveOnly(list []PendingClaim, now time.Time) (live, gone []PendingClaim) {
	for _, c := range list {
		if now.After(c.ExpiresAt) {
			gone = append(gone, c)
		} else {
			live = append(live, c)
		}
	}
	return live, gone
}

// CreatePendingClaim records a pending claim and returns it with its poll
// secret (32 random bytes, hex), which is not stored. At most one claim per
// instance id waits: a repeat from the same instance replaces its own pending
// claim and no other. It refuses, and drops nothing, when the queue is full.
func CreatePendingClaim(c PendingClaim) (PendingClaim, string, error) {
	c, secret, _, err := createPendingClaim(c)
	return c, secret, err
}

// createPendingClaim is CreatePendingClaim, also naming the claim it replaced
// (its card is closed by the caller).
func createPendingClaim(c PendingClaim) (_ PendingClaim, secret string, replaced []string, _ error) {
	sec := make([]byte, 32)
	idb := make([]byte, 8)
	if _, err := rand.Read(sec); err != nil {
		return PendingClaim{}, "", nil, err
	}
	if _, err := rand.Read(idb); err != nil {
		return PendingClaim{}, "", nil, err
	}
	secret = hex.EncodeToString(sec)
	now := time.Now().UTC()
	c.ID, c.SecretHash, c.Status = hex.EncodeToString(idb), hashToken(secret), ClaimPending
	c.CodeHash = codeDigest(c.ID, PairCode(c.ID, secret))
	c.DecidedAt = nil
	c.Restricted, c.AllowedProfiles, c.Unrestricted = false, nil, false // only the owner's approval decides
	c.Label, c.Origin, c.Agent = clip(c.Label), clip(c.Origin), clip(c.Agent)
	c.CreatedAt, c.ExpiresAt = now, now.Add(PendingClaimTTL)

	unlock, err := lockState()
	if err != nil {
		return PendingClaim{}, "", nil, err
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil {
		return PendingClaim{}, "", nil, err
	}
	list, _ = liveOnly(list, now)
	if c.Instance != "" {
		kept := list[:0:0]
		for _, e := range list {
			if e.Instance == c.Instance && e.Status == ClaimPending {
				replaced = append(replaced, e.ID)
				continue
			}
			kept = append(kept, e)
		}
		list = kept
	}
	pending := 0
	for _, e := range list {
		if e.Status == ClaimPending {
			pending++
		}
	}
	if pending >= maxPendingClaims {
		return PendingClaim{}, "", nil, ErrClaimsFull
	}
	// Keep the file bounded: drop the oldest decided tombstones first.
	for len(list) >= maxClaimRecords {
		drop := -1
		for i, e := range list {
			if e.Status != ClaimPending {
				drop = i
				break
			}
		}
		if drop == -1 {
			break
		}
		list = append(list[:drop:drop], list[drop+1:]...)
	}
	if err := writePendingLocked(append(list, c)); err != nil {
		return PendingClaim{}, "", nil, err
	}
	return c, secret, replaced, nil
}

// ListPendingClaims returns the claims still waiting, oldest first.
func ListPendingClaims() ([]PendingClaim, error) {
	unlock, err := lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil {
		return nil, err
	}
	list, _ = liveOnly(list, time.Now())
	var out []PendingClaim
	for _, c := range list {
		if c.Status == ClaimPending {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ErrAmbiguousClaim: no claim id was given and more than one is waiting.
var ErrAmbiguousClaim = errors.New("several pairing requests are waiting: name one by its id")

// ErrBadCode: the confirmation code does not match the claim.
var ErrBadCode = errors.New("that code does not match this pairing request: approve only the request whose code your own browser shows")

// pickPending finds the pending, unexpired claim named by an id or a unique
// prefix of it (4+ characters). An empty name picks the claim only when it is
// the only one waiting: "the newest" would be the attacker's to choose.
func pickPending(list []PendingClaim, idOrPrefix string, now time.Time) (int, error) {
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if idOrPrefix != "" && len(idOrPrefix) < 4 {
		return -1, fmt.Errorf("%w: give at least 4 characters of the id", ErrNoSuchClaim)
	}
	hit, waiting := -1, 0
	for i, c := range list {
		if c.Status != ClaimPending || now.After(c.ExpiresAt) {
			continue
		}
		waiting++
		switch {
		case idOrPrefix == "":
			hit = i
		case c.ID == idOrPrefix:
			hit = i
		case strings.HasPrefix(c.ID, idOrPrefix):
			if hit != -1 && list[hit].ID != idOrPrefix {
				return -1, fmt.Errorf("%w: %q matches more than one claim", ErrNoSuchClaim, idOrPrefix)
			}
			if hit == -1 {
				hit = i
			}
		}
	}
	if idOrPrefix == "" && waiting > 1 {
		return -1, ErrAmbiguousClaim
	}
	if hit == -1 {
		return -1, fmt.Errorf("%w: %q", ErrNoSuchClaim, idOrPrefix)
	}
	return hit, nil
}

// FindPendingClaim names the waiting claim an id (or unique prefix) refers to,
// or the only waiting claim when id is empty.
func FindPendingClaim(idOrPrefix string) (PendingClaim, error) {
	unlock, err := lockState()
	if err != nil {
		return PendingClaim{}, err
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil {
		return PendingClaim{}, err
	}
	hit, err := pickPending(list, idOrPrefix, time.Now())
	if err != nil {
		return PendingClaim{}, err
	}
	return list[hit], nil
}

// DecidePendingClaim approves or denies one waiting claim, named by its id or
// a unique prefix of it (4+ characters); "" names it only when it is the only
// one waiting. Approving also needs the confirmation code the claim's browser
// shows. It is the only way a claim becomes approved, and runs only from the
// owner's channels.
func DecidePendingClaim(idOrPrefix string, approve bool, code string) (PendingClaim, error) {
	return DecidePendingClaimWith(idOrPrefix, approve, code, nil, false)
}

// DecidePendingClaimWith is DecidePendingClaim that also says what the
// approved browser may run: the profiles in allowed (none when empty), or
// anything when unrestricted. Invalid ids are an error. A browser approved
// without either starts limited to no profile of its own.
func DecidePendingClaimWith(idOrPrefix string, approve bool, code string, allowed []string, unrestricted bool) (PendingClaim, error) {
	if unrestricted && len(allowed) > 0 {
		return PendingClaim{}, errors.New("a browser cannot be both unrestricted and limited to profiles")
	}
	clean := cleanProfiles(allowed)
	if len(clean) != len(dedupe(allowed)) {
		return PendingClaim{}, fmt.Errorf("not a valid profile id in %q", allowed)
	}
	unlock, err := lockState()
	if err != nil {
		return PendingClaim{}, err
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil {
		return PendingClaim{}, err
	}
	now := time.Now()
	list, _ = liveOnly(list, now)
	hit, err := pickPending(list, idOrPrefix, now)
	if err != nil {
		return PendingClaim{}, err
	}
	if approve && subtle.ConstantTimeCompare([]byte(list[hit].CodeHash), []byte(codeDigest(list[hit].ID, code))) != 1 {
		list[hit].Strikes++
		if list[hit].Strikes >= maxCodeStrikes {
			at := now.UTC()
			list[hit].Status, list[hit].DecidedAt = ClaimDenied, &at
		}
		if err := writePendingLocked(list); err != nil {
			return PendingClaim{}, err
		}
		return list[hit], ErrBadCode
	}
	list[hit].Status = ClaimDenied
	if approve {
		list[hit].Status = ClaimApproved
		list[hit].Unrestricted = unrestricted
		list[hit].Restricted, list[hit].AllowedProfiles = !unrestricted, clean
	}
	at := now.UTC()
	list[hit].DecidedAt = &at
	if err := writePendingLocked(list); err != nil {
		return PendingClaim{}, err
	}
	return list[hit], nil
}

// DenyAllPendingClaims denies every waiting claim at once (the owner's way to
// clear a queue a forger filled) and returns them.
func DenyAllPendingClaims() ([]PendingClaim, error) {
	unlock, err := lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	at := now.UTC()
	list, _ = liveOnly(list, now)
	var denied []PendingClaim
	for i := range list {
		if list[i].Status == ClaimPending && !now.After(list[i].ExpiresAt) {
			list[i].Status, list[i].DecidedAt = ClaimDenied, &at
			denied = append(denied, list[i])
		}
	}
	if len(denied) == 0 {
		return nil, nil
	}
	if err := writePendingLocked(list); err != nil {
		return nil, err
	}
	return denied, nil
}

// PollOutcome is what a poll of a claim learns.
type PollOutcome struct {
	State string // pending | approved | denied | expired
	Token string // set once, on approved
	Entry PairedBrowser
	Claim PendingClaim
}

// pollClaim checks the claim named by id with its secret (and, when the request
// carried one, the Origin the claim was made from). A wrong id and a
// wrong secret are the same ErrNoSuchClaim. A denied or expired claim is
// deleted as it is reported; an approved one mints its token here, once, and
// is deleted.
func pollClaim(id, secret, origin string) (PollOutcome, error) {
	now := time.Now()
	unlock, err := lockState()
	if err != nil {
		return PollOutcome{}, err
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil {
		return PollOutcome{}, err
	}
	at := -1
	want := hashToken(secret)
	for i, c := range list {
		if c.ID == id && subtle.ConstantTimeCompare([]byte(c.SecretHash), []byte(want)) == 1 && (origin == "" || c.Origin == origin) {
			at = i
		}
	}
	if secret == "" || at == -1 {
		return PollOutcome{}, ErrNoSuchClaim
	}
	c := list[at]
	drop := func() error { return writePendingLocked(append(list[:at:at], list[at+1:]...)) }
	switch {
	case now.After(c.ExpiresAt):
		return PollOutcome{State: "expired", Claim: c}, drop()
	case c.Status == ClaimDenied:
		return PollOutcome{State: ClaimDenied, Claim: c}, drop()
	case c.Status != ClaimApproved:
		return PollOutcome{State: ClaimPending, Claim: c}, nil
	}
	token, entry, err := addPairedBrowserLocked(PairedBrowser{
		Label: c.Label, Origin: c.Origin, Agent: c.Agent, Instance: c.Instance,
		Restricted: !c.Unrestricted, AllowedProfiles: c.AllowedProfiles,
	}, mintOpts{ID: c.ID, ReplaceInstance: true})
	if err != nil {
		if errors.Is(err, ErrBrowserLimit) {
			// Nothing was minted: keep the claim approved until it expires, so
			// room made by a revoke lets the next poll succeed.
			return PollOutcome{State: ClaimPending, Claim: c}, err
		}
		_ = drop()
		return PollOutcome{}, err
	}
	if err := drop(); err != nil {
		return PollOutcome{}, err
	}
	return PollOutcome{State: ClaimApproved, Token: token, Entry: entry, Claim: c}, nil
}

// expiredClaimIDs removes expired claims and returns their ids.
func expiredClaimIDs() []string {
	unlock, err := lockState()
	if err != nil {
		return nil
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil || len(list) == 0 {
		return nil
	}
	live, gone := liveOnly(list, time.Now())
	if len(gone) == 0 {
		return nil
	}
	_ = writePendingLocked(live)
	ids := make([]string, len(gone))
	for i, c := range gone {
		ids[i] = c.ID
	}
	return ids
}

// PairCards puts a pending claim in front of the owner as a card in the Human
// in Loop queue and closes it again. A nil PairCards means no card (the CLI
// still decides).
type PairCards interface {
	Open(ctx context.Context, c PendingClaim)
	Settle(ctx context.Context, claimID, status string)
}

// IsPairCard reports whether a hil_pending node_config is a pairing card's,
// and which claim it names.
func IsPairCard(nodeConfig string) (claimID string, ok bool) {
	var r struct {
		Kind  string `json:"kind"`
		Claim string `json:"claim"`
	}
	if json.Unmarshal([]byte(nodeConfig), &r) != nil || r.Kind != PairCardKind || r.Claim == "" {
		return "", false
	}
	return r.Claim, true
}

// OpenPairCard inserts the card for c into one profile's queue. The card names
// the request (id, browser, origin) but not its confirmation code: the owner
// types the code their own browser shows into the "code" field before
// approving, so a request they did not start cannot be approved by mistake.
func OpenPairCard(ctx context.Context, db *sql.DB, profileID string, c PendingClaim) error {
	label, origin, instance := clip(c.Label), clip(c.Origin), clip(c.Instance)
	ro, _ := json.Marshal(map[string]string{
		"claim": c.ID, "browser": label, "origin": origin, "instance": instance,
		"expiresAt": c.ExpiresAt.Format(time.RFC3339),
		"summary": fmt.Sprintf("The browser %q (%q) %s. Compare the code shown in the browser panel and type it in the code field to approve; if no browser panel shows a code, deny it.",
			label, origin, PairClaimWording),
	})
	nc, _ := json.Marshal(map[string]string{"kind": PairCardKind, "claim": c.ID})
	_, err := db.ExecContext(ctx,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, '{"code":""}', ?, ?)`,
		uuid.NewString(), "extpair:"+c.ID, PairCardKind, PairCardKind, "Pair browser: "+label, string(ro), string(nc), profileID)
	return err
}

// SettlePairCards closes every card of a claim, in every profile. status is
// "approved", "rejected" or "expired".
func SettlePairCards(ctx context.Context, db *sql.DB, claimID, status string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE hil_pending SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE execution_id = ? AND status = 'pending'`,
		status, "extpair:"+claimID)
	return err
}
