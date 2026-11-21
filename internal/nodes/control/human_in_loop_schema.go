package control

// HumanInLoopNodeSchema documents the config keys HumanInLoopNode.Execute
// reads out of its map[string]interface{} config — see SetNodeSchema's doc
// comment for why this is a companion struct rather than the runtime
// config, and internal/tools/schemagen for the tag grammar.
//
// Matches the hand-written schemas/core.human_in_loop.json this replaces
// field-for-field; no drift found.
type HumanInLoopNodeSchema struct {
	ReadonlyFields string `json:"readonly_fields" schema:"label=Read-Only Fields,type=array,item_type=text,help=Item keys shown to the reviewer as read-only context."`

	EditableFields string `json:"editable_fields" schema:"label=Editable Fields,type=array,item_type=text,help=Item keys the reviewer can edit before approving. If left empty， the whole item is editable."`

	TimeoutMinutes float64 `json:"timeout_minutes" schema:"label=Timeout (minutes),type=number,default=0,help=Max time to wait for a human decision. 0 = unlimited."`

	AutoDecide string `json:"auto_decide" schema:"label=Auto-decide (TypeSafe Jev),type=code,language=json,rows=4,placeholder={\"policy\": \"Approve polite replies without links\"， \"approve_above\": 0.9},help=Optional. Lets TypeSafe Jev settle confident items: {policy (reviewer instructions)， approve_above (default: the profile's hil threshold， 0.9)， reject_above (unset = never auto-reject)， provider (a decision provider added with monoagentcli jev provider add; empty = the default)}. Works only when the profile enabled it (monoagentcli jev enable hil) and a key resolves; otherwise it is ignored and every item waits for a person (the reason is stored on each item). Each item gets one request; an item is approved when Jev picks approve with p ≥ approve_above and rejected only when reject_above is set and Jev picks reject with p ≥ reject_above; all others wait with the suggestion attached. If every item is settled as approved the node does not pause; a single auto-reject fails the node exactly like a human reject. Items of org-started runs are never auto-decided (they only get a suggestion)， and a Jev error leaves every item pending."`
}
