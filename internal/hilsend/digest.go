package hilsend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Digest fingerprints a pending item from its row's readonly_data,
// editable_data and node_config: what its card shows and, for a send, what
// approving it performs. A decision that names the digest of the card the
// person saw (Edit.IfDigest, `hil approve --if-digest`) is refused when the
// item changed since. What is written beside the content (a suggestion, the
// proof and outcome of a claim) is not part of it.
func Digest(readonly, editable, nodeConfig string) string {
	var rp replay
	performs := ""
	if json.Unmarshal([]byte(nodeConfig), &rp) == nil && rp.Kind == "send" {
		rp.ProofHash, rp.ResolvedAt, rp.Error, rp.Result = "", "", "", nil
		raw, _ := json.Marshal(rp)
		performs = string(raw)
	}
	raw, _ := json.Marshal([]string{readonly, editable, performs})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
