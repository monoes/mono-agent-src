package coderhost

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
)

// A session token is the caller identity of a coder turn the host started: it
// names one profile, one conversation and one turn, and tells the master tools
// (CLI verbs and MCP tools) who is calling. The tools take no "caller" argument,
// so a session can only act as the session whose token it holds.
//
// Why this is not an internal/agenttokens token: those authenticate against the
// profile's HTTP API (and may be issued with `agent-mode token create`), so a
// session token stored there would also open API routes, and its binding names a
// task, not a session. Same shape, separate table: a random secret, only its
// SHA-256 stored (agenttokens.Hash's scheme), checked by lookup.
//
// Binding:
//   - Only the host mints one (IssueSessionToken, called from the launcher right
//     before it starts a turn); no CLI verb or tool mints or returns a token.
//   - The row is signed (HMAC-SHA-256 under a key derived from the profile's vault
//     data key, as the mission confirmations are, verified with hmac.Equal) over
//     profile, conversation, turn, token hash, expiry, pid and stamp: a row written
//     without that key, or edited, identifies no one.
//   - It is valid only while the turn it was minted for is the session's current
//     turn (coder_sessions.turn_id), so a token read from an old log or process
//     listing is dead once the turn ends, and a new turn replaces the old token.
//   - It names the turn's process (BindSessionToken, called by the launcher right
//     after the turn starts): the caller must be that process or one of its
//     descendants, so a sibling that read /proc/<pid>/environ is refused. Workers
//     the turn starts are its descendants and keep working. Until the process is
//     bound the token identifies no one. Where ancestry cannot be read (not Linux)
//     a token is checked by its signature and its turn only; on Linux an unreadable
//     chain fails closed.
//   - It also expires (SessionTokenTTL) in case the host died before the turn row
//     was cleared.
//
// What it does not stop: a process of the same user that is a descendant of the
// turn (any tool the agent runs, which is intended), that ptraces the turn or
// reads the vault key, or, off Linux, one that reads the turn's environment. As
// with the agent-kind tokens (internal/browserctl/kindtoken.go) this is a courtesy
// layer between agents of one user, not a sandbox against that user.

// SessionTokenEnv is the variable the host hands a turn its session token in. The
// turn keeps it in its environment (see internal/cli/chat/chat_run.go): the shell
// and the MCP server its agent runtime starts need it to call the master tools.
const SessionTokenEnv = "MONOAGENT_SESSION_TOKEN"

// SessionTokenPrefix starts every session token, so a secret scanner can tell one.
const SessionTokenPrefix = "mst-"

// SessionTokenTTL is the longest a token lives.
const SessionTokenTTL = 24 * time.Hour

// ErrSessionToken is every failure to identify a caller: absent, unknown, forged,
// expired, belonging to a turn that is no longer the session's current one, or
// presented by a process that is not the turn's.
var ErrSessionToken = errors.New("invalid session token: this call is not from a running coder session")

// ErrNotInTurnTree is ErrSessionToken for a token that is genuine but presented by a process outside the
// turn's process tree: a daemonised or `nohup`/`setsid`-detached tool, a caller in another pid namespace
// (a container), or an unreadable chain. It is told apart so a refusal can be logged and explained;
// nothing is loosened.
var ErrNotInTurnTree = fmt.Errorf("%w (the caller is not inside the session's turn process tree: detached, or in another pid namespace)", ErrSessionToken)

// ErrVaultKey is ErrSessionToken for a session token that could not be checked because the profile's
// vault key was unavailable (the check fails closed).
var ErrVaultKey = fmt.Errorf("%w (the vault key is unavailable)", ErrSessionToken)

// ErrHostInsideTurn: the host itself runs inside a session's turn (it was started from a master's
// shell), so it must not bind session tokens: every turn it started would be a descendant of that one.
var ErrHostInsideTurn = errors.New("coder host: the host runs inside a coder session's turn; start it from a terminal")

// HostInsideTurn says whether this process is a descendant of a bound session turn.
func (s *Store) HostInsideTurn() bool {
	chain, _ := callerChain()
	in := map[int]bool{}
	for _, p := range chain {
		in[p] = true
	}
	rows, err := s.db.Query(`SELECT pid, stamp FROM coder_session_tokens WHERE pid > 1 AND expires_at >= ?`, s.now().UTC().Format(timeLayout))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var stamp string
		if rows.Scan(&pid, &stamp) == nil && in[pid] && (stamp == "" || ProcStamp(pid) == stamp) {
			return true
		}
	}
	return false
}

// Caller is who a token names.
type Caller struct {
	ProfileID      string
	ConversationID string
	TurnID         string
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

const sessionTokenPurpose = "coder-session-token"

func (s *Store) tokenKey(profileID string) ([]byte, error) {
	return secrets.DeriveKey(context.Background(), s.db, profileID, sessionTokenPurpose)
}

// tokenMAC authenticates every field of a token row but the MAC itself (length-prefixed fields).
func tokenMAC(key []byte, profile, conv, turn, hash, expires string, pid int, stamp string) string {
	m := hmac.New(sha256.New, key)
	for _, f := range []string{profile, conv, turn, hash, expires, strconv.Itoa(pid), stamp} {
		fmt.Fprintf(m, "%d:%s;", len(f), f)
	}
	return hex.EncodeToString(m.Sum(nil))
}

// IssueSessionToken mints the token of the turn turnID of a session and replaces
// the tokens the session had. Only the host calls it. The token identifies no one
// until BindSessionToken names the turn's process.
func (s *Store) IssueSessionToken(profileID, conversationID, turnID string) (string, error) {
	if profileID == "" || conversationID == "" || turnID == "" {
		return "", errors.New("coder host: a session token needs a profile, a conversation and a turn")
	}
	key, err := s.tokenKey(profileID)
	if err != nil {
		return "", fmt.Errorf("coder host: the session token could not be signed: %w", err)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("coder host: generating a session token: %w", err)
	}
	tok := SessionTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	now := s.now().UTC()
	if _, err := s.db.Exec(`DELETE FROM coder_session_tokens WHERE (profile_id = ? AND conversation_id = ?) OR expires_at < ?`,
		profileID, conversationID, now.Format(timeLayout)); err != nil {
		return "", err
	}
	h, exp := hashToken(tok), now.Add(SessionTokenTTL).Format(timeLayout)
	_, err = s.db.Exec(`INSERT INTO coder_session_tokens (token_hash, profile_id, conversation_id, turn_id, created_at, expires_at, pid, stamp, mac) VALUES (?, ?, ?, ?, ?, ?, 0, '', ?)`,
		h, profileID, conversationID, turnID, now.Format(timeLayout), exp, tokenMAC(key, profileID, conversationID, turnID, h, exp, 0, ""))
	return tok, err
}

// BindSessionToken names the process of the turn the token was minted for and signs
// the row again.
func (s *Store) BindSessionToken(profileID, conversationID, turnID string, pid int, stamp string) error {
	if pid <= 1 {
		return fmt.Errorf("coder host: %d is not a turn's pid", pid)
	}
	if ancestrySupported && stamp == "" {
		return errors.New("coder host: a turn's process needs a start stamp to bind a token to it")
	}
	key, err := s.tokenKey(profileID)
	if err != nil {
		return err
	}
	var h, exp string
	if err := s.db.QueryRow(`SELECT token_hash, expires_at FROM coder_session_tokens WHERE profile_id = ? AND conversation_id = ? AND turn_id = ?`,
		profileID, conversationID, turnID).Scan(&h, &exp); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE coder_session_tokens SET pid = ?, stamp = ?, mac = ? WHERE token_hash = ?`,
		pid, stamp, tokenMAC(key, profileID, conversationID, turnID, h, exp, pid, stamp), h)
	if err == nil {
		s.MarkTurnCapStarted(profileID, conversationID, turnID) // the process exists
	}
	return err
}

// RevokeSessionTokens drops a session's tokens (its turn ended).
func (s *Store) RevokeSessionTokens(profileID, conversationID string) {
	_, _ = s.db.Exec(`DELETE FROM coder_session_tokens WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID)
}

// VerifySessionToken says who the token names. It fails (ErrSessionToken, with no
// hint which check failed) unless the row is genuine (signature), the token is live,
// its turn is the session's current turn, and the caller is the turn's process or a
// descendant of it.
func (s *Store) VerifySessionToken(tok string) (Caller, error) {
	if !strings.HasPrefix(tok, SessionTokenPrefix) || len(tok) > 128 {
		return Caller{}, ErrSessionToken
	}
	var c Caller
	var expires, stamp, mac string
	var pid int
	h := hashToken(tok)
	err := s.db.QueryRow(`SELECT t.profile_id, t.conversation_id, t.turn_id, t.expires_at, t.pid, t.stamp, t.mac FROM coder_session_tokens t
		JOIN coder_sessions x ON x.profile_id = t.profile_id AND x.conversation_id = t.conversation_id AND x.turn_id = t.turn_id
		WHERE t.token_hash = ?`, h).Scan(&c.ProfileID, &c.ConversationID, &c.TurnID, &expires, &pid, &stamp, &mac)
	if err != nil {
		return Caller{}, ErrSessionToken
	}
	key, err := s.tokenKey(c.ProfileID)
	if err != nil {
		return Caller{}, ErrVaultKey
	}
	if !hmac.Equal([]byte(tokenMAC(key, c.ProfileID, c.ConversationID, c.TurnID, h, expires, pid, stamp)), []byte(mac)) {
		return Caller{}, ErrSessionToken
	}
	if exp, perr := time.Parse(timeLayout, expires); perr != nil || !s.now().Before(exp) {
		return Caller{}, ErrSessionToken
	}
	if ancestrySupported && !descendsFrom(pid, stamp) {
		return Caller{}, ErrNotInTurnTree
	}
	return c, nil
}

// CallerFor identifies the caller of a master tool from the token it presents and the
// profile the call names. A token is for one profile: a call that names another is
// refused like a bad token.
func (s *Store) CallerFor(token, profileID string) (Caller, error) {
	c, err := s.VerifySessionToken(token)
	if err != nil {
		return Caller{}, err
	}
	if profileID != "" && profileID != c.ProfileID {
		return Caller{}, ErrSessionToken
	}
	return c, nil
}
