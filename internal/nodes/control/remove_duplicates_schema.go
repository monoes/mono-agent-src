package control

// RemoveDuplicatesNodeSchema documents the config keys
// RemoveDuplicatesNode.Execute reads out of its map[string]interface{}
// config — see SetNodeSchema's doc comment for why this is a companion
// struct rather than the runtime config, and internal/tools/schemagen for
// the tag grammar.
//
// Two drift findings vs. the hand-written schemas/core.remove_duplicates.json
// this replaces:
//
//   - The hand-written schema marked "field" as required. Execute treats it
//     as optional: an empty "field" falls back to deduplicating by the
//     entire JSON-serialized item, which is a valid, intentional mode, not
//     an error. Marked optional here.
//   - "keep" ("first" default, or "last") is a real config key Execute
//     reads and validates, with no field in the hand-written schema at all.
//
// The operation names follow n8n's Remove Duplicates v2 node. Cross-run
// history lives in SQLite per workflow (and per node for scope "node"), is
// bounded by historySize and is removed with the workflow; the incremental
// and date logics keep one stored maximum per scope instead.
type RemoveDuplicatesNodeSchema struct {
	Operation string `json:"operation" schema:"label=Operation,type=select,options=removeDuplicateInputItems|removeItemsRepeatedWithinPreviousExecutions|clearDeduplicationHistory,default=removeDuplicateInputItems,help=removeDuplicateInputItems: drop repeats within this run. removeItemsRepeatedWithinPreviousExecutions: keep only items that are new compared with earlier runs of this workflow (see Keep Items Where; e.g. new feed entries on a schedule). clearDeduplicationHistory: forget the stored values and maximums and pass items through."`

	Field string `json:"field" schema:"label=Deduplicate By Field,type=text,placeholder=id,help=Items with the same value in this field are deduplicated. Leave empty to deduplicate by the entire item."`

	Keep string `json:"keep" schema:"label=Keep,type=select,options=first|last,default=first,help=Which occurrence to keep when duplicates are found.,depends_on_key=operation,depends_on_values=removeDuplicateInputItems"`

	Logic string `json:"logic" schema:"label=Keep Items Where,type=select,options=removeItemsWithAlreadySeenKeyValues|removeItemsUpToStoredIncrementalKey|removeItemsUpToStoredDate,default=removeItemsWithAlreadySeenKeyValues,help=removeItemsWithAlreadySeenKeyValues: the value is new (not in the history). removeItemsUpToStoredIncrementalKey: the value is a number higher than any earlier run's. removeItemsUpToStoredDate: the value is a date later than any earlier run's. The last two store only the maximum.,depends_on_key=operation,depends_on_values=removeItemsRepeatedWithinPreviousExecutions"`

	DedupeValue string `json:"dedupeValue" schema:"label=Dedupe Value,type=text,placeholder={{ $json.link }},help=Value compared with earlier runs: an expression evaluated per item or a dot-path field. Empty falls back to the field above，then to the whole item. Items with an empty value fail the node.,depends_on_key=operation,depends_on_values=removeItemsRepeatedWithinPreviousExecutions"`

	Scope string `json:"scope" schema:"label=History Scope,type=select,options=node|workflow,default=node,help=node: this node keeps its own history. workflow: every remove-duplicates node of the workflow shares one history.,depends_on_key=operation,depends_on_values=removeItemsRepeatedWithinPreviousExecutions|clearDeduplicationHistory"`

	HistorySize float64 `json:"historySize" schema:"label=History Size,type=number,default=10000,min=1,help=Most values remembered (removeItemsWithAlreadySeenKeyValues only); the least recently seen are forgotten first (and could pass again).,depends_on_key=operation,depends_on_values=removeItemsRepeatedWithinPreviousExecutions"`
}
