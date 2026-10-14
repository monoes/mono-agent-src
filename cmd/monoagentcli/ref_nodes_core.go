package main

// Node reference entries (`monoagentcli ref node <type>`), one variable per
// category; ref.go joins them into nodeDocs.

// nodeDocsTrigger is the "trigger" category of the node catalogue.
var nodeDocsTrigger = []nodeDoc{
	{
		Type:     "trigger.schedule",
		Category: "trigger",
		Short:    "Run workflow on a cron schedule",
		Description: `Starts the workflow at a recurring time. Uses a 6-field cron expression
(seconds minute hour day month weekday) as required by robfig/cron v3.`,
		Config: `{
  "cron": "0 0 9 * * *"   // every day at 09:00:00
}`,
		Inputs:  "none",
		Outputs: "one item: trigger_type (\"schedule\"), timestamp (RFC 3339 UTC), catch_up (true on a catch-up run)",
		Notes:   `6-field cron: sec min hour day month weekday. The workflow must be "activate"d and "monoagentcli daemon" must be running for this trigger to actually fire over time.`,
	},
}

// nodeDocsSystem is the "system" category of the node catalogue.
var nodeDocsSystem = []nodeDoc{
	{
		Type:     "system.rss_read",
		Category: "system",
		Short:    "Fetch items from an RSS / Atom feed",
		Description: `Downloads and parses an RSS or Atom feed, emitting one item per entry.
Commonly used to pull news, blog posts, or podcast episodes.`,
		Config: `{
  "url":            "https://rss.dw.com/rdf/rss-en-all",
  "limit":          15,
  "since_last_run": false,  // true: emit only entries newer than the previous run saw
  "baseline":       false   // with since_last_run: the first run records the feed and emits nothing
}`,
		Inputs:  "none (URL comes from config)",
		Outputs: "title, description, link, pubDate, guid, author, categories",
	},
	{
		Type:     "system.execute_command",
		Category: "system",
		Short:    "Run a shell command and capture output",
		Config: `{
  "command": "echo hello",
  "timeout": 30
}`,
		Inputs:  "none",
		Outputs: "stdout, stderr, exit_code",
	},
}

// nodeDocsCore is the "core" category of the node catalogue.
var nodeDocsCore = []nodeDoc{
	{
		Type:     "core.merge",
		Category: "core",
		Short:    "Merge items from multiple upstream nodes",
		Description: `Collects all items from every connected input and passes them on together.
Use this after branching (e.g. three parallel RSS reads) to rejoin the stream.`,
		Config:  `{}   // no config required`,
		Inputs:  "any number of upstream items",
		Outputs: "all items combined into one stream",
	},
	{
		Type:     "core.aggregate",
		Category: "core",
		Short:    "Collect items into arrays (group by)",
		Description: `Collects a field from every incoming item into a single array output.
Useful for building a list of titles or descriptions for a batch prompt.`,
		Config: `{
  "operations": [
    { "field": "title",       "operation": "array", "output_field": "titles" },
    { "field": "description", "operation": "array", "output_field": "descriptions" }
  ]
}`,
		Inputs:  "many items with the aggregated fields",
		Outputs: "one item containing the collected arrays",
	},
	{
		Type:     "core.set",
		Category: "core",
		Short:    "Compute / set fields on each item",
		Description: `Evaluates template expressions and assigns their results as new fields.
Use to build prompts, format strings, or reshape data before the next node.`,
		Config: `{
  "assignments": [
    {
      "field": "my_prompt",
      "value": "Summarise: {{ index $json.titles 0 }}",
      "type":  "string"
    },
    {
      "field": "count",
      "value": "{{ len $json.items }}",
      "type":  "string"
    }
  ]
}`,
		Inputs:  "any item",
		Outputs: "same item with new fields added / overwritten",
	},
	{
		Type:     "core.filter",
		Category: "core",
		Short:    "Pass only items that satisfy a condition",
		Config: `{
  "condition": "{{ gt (len $json.title) 5 }}"
}`,
		Inputs:  "any items",
		Outputs: "items where condition evaluates to true",
	},
	{
		Type:     "core.if",
		Category: "core",
		Short:    "Branch on a boolean condition (true / false outputs)",
		Config: `{
  "condition": "{{ eq $json.status \"active\" }}"
}`,
		Inputs:  "any item",
		Outputs: "true branch or false branch",
	},
	{
		Type:     "core.switch",
		Category: "core",
		Short:    "Multi-branch switch on a value",
		Config: `{
  "field":  "$json.platform",
  "cases":  ["instagram", "linkedin"],
  "default": "other"
}`,
		Inputs:  "any item",
		Outputs: "one output per case + default",
	},
	{
		Type:     "core.split_in_batches",
		Category: "core",
		Short:    "Split a large stream into fixed-size batches",
		Config:   `{ "batch_size": 10 }`,
		Inputs:   "stream of items",
		Outputs:  "items in groups of batch_size",
	},
	{
		Type:     "core.remove_duplicates",
		Category: "core",
		Short:    "Deduplicate items by a field, within a run or across runs",
		Description: `operation "removeDuplicateInputItems" (default) drops duplicates within this
run's input. "removeItemsRepeatedWithinPreviousExecutions" compares with earlier
runs of this workflow (state stored per node, or per workflow with scope
"workflow"), by logic: "removeItemsWithAlreadySeenKeyValues" (default) keeps
items whose dedupeValue no earlier run saw (historySize caps the stored values);
"removeItemsUpToStoredIncrementalKey" keeps items whose dedupeValue is a number
higher than any earlier run's; "removeItemsUpToStoredDate" keeps items whose
dedupeValue is a date later than any earlier run's (those two store only the
maximum; the first run keeps everything). "clearDeduplicationHistory" empties
the history and maximums and passes items through.`,
		Config: `{
  "field":       "url",                                          // within-run key
  "operation":   "removeItemsRepeatedWithinPreviousExecutions",  // optional
  "logic":       "removeItemsWithAlreadySeenKeyValues",          // | removeItemsUpToStoredIncrementalKey | removeItemsUpToStoredDate
  "dedupeValue": "{{ $json.url }}",                              // across runs: value to remember / compare
  "scope":       "node",                                         // node | workflow
  "historySize": 10000
}`,
		Inputs:  "items possibly containing duplicates",
		Outputs: "items with duplicates removed",
	},
	{
		Type:     "core.sort",
		Category: "core",
		Short:    "Sort items by a field",
		Config:   `{ "field": "pubDate", "order": "desc" }`,
		Inputs:   "unsorted items",
		Outputs:  "sorted items",
	},
	{
		Type:     "core.limit",
		Category: "core",
		Short:    "Keep only the first N items",
		Config:   `{ "limit": 5 }`,
		Inputs:   "any items",
		Outputs:  "first N items",
	},
	{
		Type:     "core.wait",
		Category: "core",
		Short:    "Pause execution for a fixed duration",
		Config:   `{ "seconds": 5 }`,
		Inputs:   "any items (passed through unchanged)",
		Outputs:  "same items after waiting",
	},
	{
		Type:     "core.stop_error",
		Category: "core",
		Short:    "Abort workflow with an error message",
		Config:   `{ "message": "{{ $json.error_detail }}" }`,
		Inputs:   "any item",
		Outputs:  "never (throws error)",
	},
	{
		Type:     "core.code",
		Category: "core",
		Short:    "Run custom JavaScript logic on items",
		Config: `{
  "code": "(function () { return $input.all().map(item => ({ ...item, extra: 'hello' })); })()"
}`,
		Inputs:  "any items",
		Outputs: "items returned by the code",
	},
	{
		Type:     "core.compare_datasets",
		Category: "core",
		Short:    "Compare two datasets and emit differences",
		Config:   `{ "key": "id" }`,
		Inputs:   "two item streams",
		Outputs:  "added, removed, changed items",
	},
}
