package hilsend

import "encoding/json"

// replay is what approving needs to run the node again.
type replay struct {
	Kind       string                   `json:"kind"`
	NodeType   string                   `json:"nodeType"`
	Config     map[string]interface{}   `json:"config"`
	Input      []map[string]interface{} `json:"input"`
	TextField  string                   `json:"textField"`
	SubjectKey string                   `json:"subjectField,omitempty"`
	ProofHash  string                   `json:"proofHash,omitempty"`
	Error      string                   `json:"error,omitempty"`
	Result     json.RawMessage          `json:"result,omitempty"`
	ResolvedAt string                   `json:"resolvedAt,omitempty"`
	// Publication is set once a registration claimed this send's approval
	// (ClaimPublicationApproval): one approval registers one publication.
	Publication string `json:"publication,omitempty"`
	// Address is the recipient as the send addresses it, what an
	// always-allow rule made from this item is keyed on.
	Address string `json:"address,omitempty"`
	// Connection is the id of the saved connection a send with host keys
	// goes through (savedConnection), which an always-allow rule made from
	// this item is bound to (ruleAccount).
	Connection string `json:"connection,omitempty"`
	// RefHashes are the hashes of the tags of the references to granted
	// secrets the card's text holds, by secret name (heldsecret.go). A hash
	// recognises a tag and writes none.
	RefHashes map[string]string `json:"refHashes,omitempty"`
}

type editable struct {
	Text    string `json:"text"`
	Subject string `json:"subject,omitempty"`
}
