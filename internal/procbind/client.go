package procbind

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrNotAnswered is every way a verification can fail to produce an authentic answer: the service
// is not there, is slow, speaks wrongly, or cannot prove it is the genuine service. A caller treats
// it as a deny (fail closed) and never falls back to its own /proc.
var ErrNotAnswered = errors.New("procbind: the binding service did not give an authentic answer")

// Client asks the binding service whether the calling process may use a token. Everything it
// trusts comes from its own side: the generation it read from the authenticated host row, the key
// it derived, and the nonce it generates for the request.
type Client struct {
	Key        []byte // the answer key (K_ans) for this profile and generation
	Profile    string
	Generation string // the host generation the verifier authenticated itself
	Timeout    time.Duration
}

// DialUnix connects to the service's socket. The directory checks of section 8 are not here yet;
// until they are, the authentication is the keyed answer alone, which is what the design relies on.
func DialUnix(path string) (*net.UnixConn, error) {
	return net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
}

func (c Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 5 * time.Second
}

// Verify runs one request on conn and returns the service's authenticated verdict. On any error the
// verdict is VerdictDeny and the error is ErrNotAnswered.
func (c Client) Verify(conn net.Conn, tokenID, scope string) (Verdict, error) {
	v, err := c.verify(conn, tokenID, scope)
	if err != nil {
		return VerdictDeny, fmt.Errorf("%w: %v", ErrNotAnswered, err)
	}
	return v, nil
}

func (c Client) verify(conn net.Conn, tokenID, scope string) (Verdict, error) {
	_ = conn.SetDeadline(time.Now().Add(c.timeout()))
	nv, err := NewNonce()
	if err != nil {
		return "", err
	}
	var h wireHello
	if err := readFrame(conn, &h); err != nil {
		return "", err
	}
	// the generation is the verifier's own authenticated one: a service of any other is refused here
	if h.V != protoVersion || subtle.ConstantTimeCompare([]byte(h.Generation), []byte(c.Generation)) != 1 {
		return "", errors.New("unexpected hello")
	}
	ns, err := hex.DecodeString(h.NS)
	if err != nil || len(ns) != NonceLen {
		return "", errors.New("bad service nonce")
	}
	req := wireRequest{V: protoVersion, TokenID: tokenID, Profile: c.Profile, Scope: scope, NV: hex.EncodeToString(nv), NS: h.NS}
	if err := writeFrame(conn, req); err != nil {
		return "", err
	}
	var rep wireReply
	if err := readFrame(conn, &rep); err != nil {
		return "", err
	}
	if rep.V != protoVersion {
		return "", errors.New("unexpected reply version")
	}
	// the answer is checked against what THIS verifier holds: its own nonce and generation
	a := Answer{NV: nv, NS: ns, Generation: c.Generation, TokenID: tokenID, Profile: c.Profile, Scope: scope, Verdict: Verdict(rep.Verdict)}
	if err := a.Verify(c.Key, rep.MAC); err != nil {
		return "", err
	}
	return a.Verdict, nil
}
