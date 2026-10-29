package procbind

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// The wire format of the binding service (section 4 of the design): one request per connection,
// as length-prefixed JSON frames. The service speaks first (hello), the verifier answers with its
// request (carrying its own fresh nonce), the service replies, the connection closes.

const (
	protoVersion = 1
	// maxFrame bounds a frame body; a message names a token, a profile, a scope and two nonces.
	maxFrame = 4096
)

// wireHello is the service's first message: the host generation and the service's nonce.
type wireHello struct {
	V          int    `json:"v"`
	Generation string `json:"gen"`
	NS         string `json:"ns"` // hex, NonceLen bytes
}

// wireRequest is the verifier's request. It carries no pid, no stamp and never the token secret.
type wireRequest struct {
	V       int    `json:"v"`
	TokenID string `json:"token_id"`
	Profile string `json:"profile"`
	Scope   string `json:"scope"`
	NV      string `json:"nv"` // hex, the verifier's nonce for this request
	NS      string `json:"ns"` // the service's nonce, echoed
}

// wireReply is the service's answer. Class is advisory text and is not authenticated.
type wireReply struct {
	V       int    `json:"v"`
	Verdict string `json:"verdict"`
	Class   string `json:"class,omitempty"`
	MAC     string `json:"mac"`
}

var errFrame = errors.New("procbind: malformed frame")

func writeFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body) > maxFrame {
		return fmt.Errorf("%w: body of %d bytes", errFrame, len(body))
	}
	buf := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(body)), uint32(len(body)))
	_, err = w.Write(append(buf, body...))
	return err
}

// readFrame reads one frame into v. The length is checked before anything is allocated; unknown
// fields and trailing bytes are refused.
func readFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxFrame {
		return fmt.Errorf("%w: length %d", errFrame, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", errFrame, err)
	}
	// Only the exact bytes the writer produces are accepted: this refuses trailing data (including
	// a stray closing brace), duplicate keys, keys in another case, other orderings and spacing,
	// and a null body, none of which the decoder rejects by itself.
	if canon, err := json.Marshal(v); err != nil || !bytes.Equal(canon, body) {
		return fmt.Errorf("%w: not in canonical form", errFrame)
	}
	return nil
}
