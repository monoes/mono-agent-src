package procbind

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"time"
)

// A v2 token is accepted by a verifier only after every check below, in this order, and a secret that fails any of
// them is a deny that never falls through to the legacy tables (design section 4, step 4).

// ErrDenied is every refusal of a v2 secret. It carries no hint of which check failed; the reason goes to Logf.
var ErrDenied = errors.New("procbind: the token is not accepted")

// ErrServiceDown is a refusal because the binding service gave no authentic answer. It is a deny with its own
// reason: the service being absent, slow or not genuine never lets a token through.
var ErrServiceDown = errors.New("procbind: the binding service did not answer")

// Dialer opens a connection to the binding service of a profile.
type Dialer func(profile string) (net.Conn, error)

// V2Check verifies v2 secrets. It keeps no key and no generation between calls: the row key is read from the vault on
// every call (RowKeyIfExists, never RowKey) and the host generation from the newest signed host row, because the
// service restarts with a new generation and a profile can be deleted at any time.
type V2Check struct {
	DB      *sql.DB
	Dial    Dialer // nil: DialProfile
	Now     func() time.Time
	Timeout time.Duration // the service's answer; 5 s by default
	// Logf receives the reason of each refusal, for the operator. It is never part of an error.
	Logf func(format string, args ...any)
}

// V2Result is what a verified secret names.
type V2Result struct {
	Kind    Kind
	Mission MissionTokenRow
	Session SessionTokenRow
	Binding BindingRow
}

// maxV2Secret bounds what is hashed and looked up.
const maxV2Secret = 128

func (c V2Check) deny(format string, args ...any) error {
	if c.Logf != nil {
		c.Logf("procbind verify: "+format, args...)
	}
	return ErrDenied
}

// Verify checks a v2 secret of the given kind: the row exists and carries a genuine signature, is the token the
// secret hashes to, has not expired and is not revoked; its binding row is genuine and unexpired; a host has
// started for the profile; and the binding service, asked about the binding row's own scope, authentically allows
// the caller. Any failure is ErrDenied (or ErrServiceDown); an unexpected database error is returned wrapped and
// is also a deny.
func (c V2Check) Verify(ctx context.Context, kind Kind, secret string) (V2Result, error) {
	var res V2Result
	res.Kind = kind
	if secret == "" || len(secret) > maxV2Secret || (kind != KindMission && kind != KindSession) {
		return res, c.deny("not a v2 secret of kind %q", kind)
	}
	hash := HashSecret(secret)
	table := "agent_tokens_v2"
	if kind == KindSession {
		table = "coder_session_tokens_v2"
	}
	var profile, id string
	err := c.DB.QueryRowContext(ctx, `SELECT profile_id, id FROM `+table+` WHERE token_hash = ?`, hash).Scan(&profile, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return res, c.deny("no such token")
	}
	if err != nil {
		return res, fmt.Errorf("procbind: verifying: %w", err)
	}
	key, err := RowKeyIfExists(ctx, c.DB, profile)
	if err != nil {
		return res, c.deny("no row key for the profile: %v", err)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	var storedHash string
	var expires time.Time
	if kind == KindMission {
		if res.Mission, err = GetMissionToken(ctx, c.DB, key, profile, id); err != nil {
			return res, c.deny("token row: %v", err)
		}
		storedHash, expires = res.Mission.TokenHash, res.Mission.ExpiresAt
	} else {
		if res.Session, err = GetSessionToken(ctx, c.DB, key, profile, id); err != nil {
			return res, c.deny("token row: %v", err)
		}
		storedHash, expires = res.Session.TokenHash, res.Session.ExpiresAt
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hash)) != 1 {
		return res, c.deny("hash mismatch")
	}
	if !now().Before(expires) {
		return res, c.deny("token expired")
	}
	if revoked, err := IsRevoked(ctx, c.DB, kind, id); err != nil {
		return res, fmt.Errorf("procbind: verifying: %w", err)
	} else if revoked {
		return res, c.deny("token revoked")
	}
	if res.Binding, err = GetBinding(ctx, c.DB, key, kind, profile, id); err != nil {
		return res, c.deny("binding row: %v", err)
	}
	if !now().Before(res.Binding.ExpiresAt) {
		return res, c.deny("binding expired")
	}
	host, err := CurrentHost(ctx, c.DB, key, profile)
	if err != nil {
		return res, c.deny("no genuine host row: %v", err)
	}
	ansKey, err := AnswerKeyIfExists(ctx, c.DB, profile, host.Generation)
	if err != nil {
		return res, c.deny("answer key: %v", err)
	}
	dial := c.Dial
	if dial == nil {
		dial = func(p string) (net.Conn, error) { return DialProfile(p) }
	}
	conn, err := dial(profile)
	if err != nil {
		c.deny("service not reachable: %v", err)
		return res, ErrServiceDown
	}
	defer conn.Close()
	verdict, err := Client{Key: ansKey, Profile: profile, Generation: host.Generation, Timeout: c.Timeout}.Verify(conn, id, res.Binding.Scope)
	if err != nil {
		c.deny("no authentic answer: %v", err)
		return res, ErrServiceDown
	}
	if verdict != VerdictAllow {
		return res, c.deny("the service denied the caller")
	}
	// What is NOT read again, and why. Token and binding rows cannot be deleted or changed while they matter: the v2
	// tables are insert-only (an UPDATE aborts) and a DELETE aborts inside the row's retention window, 7 days past its
	// own expiry, which the expiry check below already covers; no production code deletes them. So the only way a live
	// token stops being valid between the checks and here is a revocation row (read below) or its expiry (read below).
	// A host start that supersedes the generation after the answer does not undo it: the answer was authentic for the
	// generation this call read, and the next verification reads the newest host row again.
	//
	// The answer is the authorization boundary. A revocation commits before the service drops its live entry, so one
	// that landed after the checks above and before this answer still gets an authentic allow: read the revocation and
	// the expiry again now that the answer is in.
	if revoked, err := IsRevoked(ctx, c.DB, kind, id); err != nil {
		return res, fmt.Errorf("procbind: verifying: %w", err)
	} else if revoked {
		return res, c.deny("token revoked while it was being verified")
	}
	if at := now(); !at.Before(expires) || !at.Before(res.Binding.ExpiresAt) {
		return res, c.deny("token expired while it was being verified")
	}
	return res, nil
}
