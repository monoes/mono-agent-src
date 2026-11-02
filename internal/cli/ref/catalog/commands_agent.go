package catalog

// The `agent purposes` entries of `ref commands`: which validated models each kind of agent
// work may use (AGENTS.md, "Agent purposes").
var agentCommandDocs = []cmdDoc{
	{
		Name:     "agent purposes",
		Short:    "Choose which validated models each kind of agent work may use: list, set, reset",
		Usage:    "monoagentcli --profile P agent purposes list|set|reset",
		Examples: []string{"monoagentcli --profile work agent purposes list --json"},
	},
	{
		Name:     "agent purposes list",
		Short:    "The purposes (coding, review, research, ...) with the pairs chosen for each and their status, and the roster to choose from; open to agents",
		Usage:    "monoagentcli --profile P agent purposes list",
		Examples: []string{"monoagentcli --profile work agent purposes list --json"},
	},
	{
		Name:  "agent purposes set",
		Short: "Replace one purpose's setting: the validated models it may use and how to choose among them (the operator only)",
		Usage: "monoagentcli --profile P agent purposes set PURPOSE [--pair RUNTIME:MODEL]... [--rule any|order|cheapest] [--effort EFFORT] [--different-from-author on|off] [--fallback any|none] [--budget-share PCT]",
		Flags: `  --pair stringArray             A validated RUNTIME:MODEL the purpose may use (repeatable; a pair whose status is not ok is refused)
  --rule string                  How to choose inside the set: any, order or cheapest (default any)
  --effort string                Reasoning effort to ask for when the model offers it
  --different-from-author string on: a worker runs a different model than the one that produced the work it checks (default off)
  --fallback string              When no chosen pair is available: none fails the spawn, any uses every validated model (default none)
  --budget-share int             Share of the budget in percent, recorded only (default 0)`,
		Examples: []string{
			"monoagentcli --profile work agent purposes set review --pair claude:opus --pair codex:gpt-5.5 --different-from-author on --json",
			"monoagentcli --profile work agent purposes set triage --pair claude:haiku --rule cheapest --json",
		},
	},
	{
		Name:     "agent purposes reset",
		Short:    "Put a purpose back to the default: every validated model (the operator only)",
		Usage:    "monoagentcli --profile P agent purposes reset PURPOSE",
		Examples: []string{"monoagentcli --profile work agent purposes reset review --json"},
	},
}
