package org

import (
	"encoding/json"

	"github.com/monoes/mono-agent/internal/orgchat"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// roleFolders answers the folder each role of org starts in
// (orgdesign.RoleWorkdir), "" for a role the org file does not have or when
// the file cannot be read.
func roleFolders(root, org string) func(role string) string {
	doc, err := orgdesign.Load(root, org)
	return func(role string) string {
		if err != nil {
			return ""
		}
		if r, _ := doc.FindRole(role); r == nil {
			return ""
		}
		return orgdesign.RoleWorkdir(root, doc, role)
	}
}

// withApprovalCommands adds to each approval of a shell call in an `org
// approvals` payload what a person needs to decide it: `command`,
// `command_truncated` when only its start is known, and `cwd`, the role's
// folder. Everything monomind sent is kept, and a payload this does not
// understand passes through unchanged.
func withApprovalCommands(root, org string, payload json.RawMessage) json.RawMessage {
	var doc map[string]json.RawMessage
	var items []map[string]json.RawMessage
	if json.Unmarshal(payload, &doc) != nil || json.Unmarshal(doc["items"], &items) != nil {
		return payload
	}
	folder := roleFolders(root, org)
	for _, item := range items {
		var role string
		var input map[string]interface{}
		_ = json.Unmarshal(item["roleId"], &role)
		_ = json.Unmarshal(item["input"], &input)
		command, truncated := orgchat.Command(input)
		if command == "" {
			continue
		}
		item["command"] = rawJSON(command)
		if truncated {
			item["command_truncated"] = rawJSON(true)
		}
		if cwd := folder(role); cwd != "" {
			item["cwd"] = rawJSON(cwd)
		}
	}
	doc["items"] = rawJSON(items)
	return rawJSON(doc)
}

// rawJSON marshals a value made of strings, booleans and parsed JSON, which
// cannot fail.
func rawJSON(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
