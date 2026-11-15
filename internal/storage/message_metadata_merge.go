package storage

import (
	"encoding/json"
	"strings"
)

// A re-sync or re-import refreshes what the platform provides and keeps what
// the user and the operator's flows stored on the row.

// messageOwnedMetadataKeys are never taken from an incoming row, whatever
// else the sync sends: the send flow's own bookkeeping.
var messageOwnedMetadataKeys = map[string]bool{"workflow": true, "hil_ref": true}

// messageSyncedMetadataKeys are the underscore keys a sync sets itself
// (the Outlook sync's provenance); every other underscore key (_classification,
// _approval, ...) belongs to the operator's flows.
var messageSyncedMetadataKeys = map[string]bool{"_source": true}

// mergeMessageMetadata merges the incoming metadata of a re-sync into the
// stored one. Incoming top-level keys replace stored ones (provenance and
// attachments refresh), except keys the operator's flows own and nulls, which
// would delete a stored key. Stored keys the sync does not provide stay. An
// incoming value that is not a JSON object changes nothing; a stored value
// that is not a JSON object is replaced by the sanitised incoming one.
func mergeMessageMetadata(stored, incoming string) string {
	var in map[string]json.RawMessage
	if strings.TrimSpace(incoming) == "" || json.Unmarshal([]byte(incoming), &in) != nil || in == nil {
		return stored
	}
	var out map[string]json.RawMessage
	if json.Unmarshal([]byte(stored), &out) != nil || out == nil {
		out = map[string]json.RawMessage{}
	}
	for k, v := range in {
		if string(v) == "null" || messageOwnedMetadataKeys[k] || (strings.HasPrefix(k, "_") && !messageSyncedMetadataKeys[k]) {
			continue
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return stored
	}
	return string(b)
}

// messageOperatorStateSQL is true for an existing row the operator or the send
// flow owns: a draft, the approval lifecycle, or a message we sent.
const messageOperatorStateSQL = `(person_messages.status IN ('draft','pending_approval','approved','sending','rejected','failed','expired')
				OR (person_messages.status = 'sent' AND person_messages.direction = 'outbound'))`

// messageKeepIfOperatorSQL keeps the stored column of an operator-state row;
// other rows take the incoming value.
func messageKeepIfOperatorSQL(col string) string {
	return "CASE WHEN " + messageOperatorStateSQL + " THEN person_messages." + col + " ELSE excluded." + col + " END"
}

// messageStatusKeepSQL changes the status of an existing row only when the row
// is not in an operator state and the incoming status is one a sync sets
// (the last bind parameter says whether the incoming row named one at all).
// Anything else, 'approved' or 'sending' included, is ignored.
const messageStatusKeepSQL = `CASE
				WHEN ? = 0 THEN person_messages.status
				WHEN ` + messageOperatorStateSQL + ` THEN person_messages.status
				WHEN excluded.status IN ('sent') THEN excluded.status
				ELSE person_messages.status END`

// isOperatorMessageState is messageOperatorStateSQL for a row read into Go.
func isOperatorMessageState(status, direction string) bool {
	switch status {
	case "draft", "pending_approval", "approved", "sending", "rejected", "failed", "expired":
		return true
	case "sent":
		return direction == "outbound"
	}
	return false
}
