// Package procbind holds the pieces of the peer-credential process binding designed in
// docs/mastermind/specs/2026-10-28-peer-credential-binding.md. This part is pure: the canonical
// length-delimited encoding every MAC of the design is computed over, and the keyed answer the
// binding service gives a verifier. Nothing here opens a socket, reads /proc or touches the
// database; it is not wired into any token check yet.
package procbind

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// Encode is the canonical encoding of a MAC input: the domain tag, the version and then each
// field, every one as an 8-byte big-endian length followed by its bytes. The arity is part of
// the message (a field is never optional: an absent one is the empty string), so no two distinct
// (domain, version, fields) tuples encode to the same bytes. Fields are opaque byte strings: a
// caller that passes binary data (a nonce) as a string must pass the same bytes on both sides, not
// a hex or base64 form on one of them. There is no concatenation of bare fields anywhere else.
func Encode(domain string, version uint8, fields ...string) []byte {
	n := 8 + len(domain) + 8 + 1
	for _, f := range fields {
		n += 8 + len(f)
	}
	b := make([]byte, 0, n)
	b = appendField(b, domain)
	b = appendField(b, string([]byte{version}))
	for _, f := range fields {
		b = appendField(b, f)
	}
	return b
}

func appendField(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint64(b, uint64(len(s)))
	return append(b, s...)
}

// NonceLen is the length of the verifier and service nonces: 256 bits.
const NonceLen = 32

// MinKeyLen is the shortest key a MAC is computed under.
const MinKeyLen = 32

// NewNonce returns NonceLen bytes from the operating system's CSPRNG.
func NewNonce() ([]byte, error) {
	n := make([]byte, NonceLen)
	if _, err := rand.Read(n); err != nil {
		return nil, fmt.Errorf("procbind: nonce: %w", err)
	}
	return n, nil
}

// Verdict is the service's decision.
type Verdict string

const (
	VerdictAllow Verdict = "allow"
	VerdictDeny  Verdict = "deny"
)

const (
	answerDomain  = "procbind/answer/v2"
	answerVersion = 2
)

// ErrAnswer is every failure to authenticate an answer; it says nothing about which check failed.
var ErrAnswer = errors.New("procbind: the answer is not authentic")

// Answer is what the service's MAC covers (section 4 of the design): both nonces, the host
// generation the verifier authenticated, and the exact request and verdict.
type Answer struct {
	NV         []byte // the verifier's fresh nonce for this request
	NS         []byte // the service's nonce from its hello
	Generation string // host generation, from the verifier's own authenticated read
	TokenID    string
	Profile    string
	Scope      string
	Verdict    Verdict
}

func (a Answer) check() error {
	switch {
	case len(a.NV) != NonceLen || len(a.NS) != NonceLen:
		return fmt.Errorf("procbind: nonces must be %d bytes", NonceLen)
	case a.Verdict != VerdictAllow && a.Verdict != VerdictDeny:
		return fmt.Errorf("procbind: unknown verdict %q", a.Verdict)
	case a.TokenID == "" || a.Profile == "" || a.Generation == "":
		return errors.New("procbind: an answer names a token, a profile and a host generation")
	case len(a.TokenID) > maxField || len(a.Profile) > maxField || len(a.Generation) > maxField || len(a.Scope) > maxField:
		return fmt.Errorf("procbind: an answer field is longer than %d bytes", maxField)
	}
	return nil
}

func (a Answer) encode() []byte {
	return Encode(answerDomain, answerVersion, string(a.NV), string(a.NS), a.Generation, a.TokenID, a.Profile, a.Scope, string(a.Verdict))
}

// maxField bounds every string field of an answer; ids, profiles and scopes are short.
const maxField = 4096

// MAC is the lowercase-hex HMAC-SHA-256 of the answer under key. The key is the service's answer
// key (K_ans of the design, derived per profile and host generation); never pass the raw vault key.
func (a Answer) MAC(key []byte) (string, error) {
	if len(key) < MinKeyLen {
		return "", fmt.Errorf("procbind: key shorter than %d bytes", MinKeyLen)
	}
	if err := a.check(); err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write(a.encode())
	return hex.EncodeToString(m.Sum(nil)), nil
}

// Verify recomputes the MAC over the verifier's own view of the answer and compares it in
// constant time. The caller builds the Answer from what it holds itself: the nonce it generated
// for this request (NV), the host generation it read from the database, and the request it sent;
// only NS and the verdict come from the service's reply. The MAC must be lowercase hex exactly as
// MAC returns it (an uppercase or padded form is rejected). The deny class of a reply is not part
// of the MAC and is advisory text only: it never decides anything. Any mismatch, malformed or
// truncated MAC, or bad input is ErrAnswer.
func (a Answer) Verify(key []byte, mac string) error {
	want, err := a.MAC(key)
	if err != nil || !hmac.Equal([]byte(want), []byte(mac)) {
		return ErrAnswer
	}
	return nil
}
