package catalog

// The task-session entries of `ref commands` (docs/mastermind/plans/2026-10-25-coder-missions-task-sessions.md),
// beside commands_tasks.go's. A session is one goal with its own tasks; the operator opens it.
var taskSessionCommandDocs = []cmdDoc{
	{
		Name:  "task session",
		Short: "Task sessions: one goal with a board of its own, planned as a draft and opened by the operator",
		Usage: "monoagentcli [--profile P] task session new|list|show|plan|approve|pause|resume|stop|archive|budget",
		Flags: `  new      Start a session, as a draft (an agent may; the budget, limits and access level are the operator's)
  list     The profile's sessions
  show     One session: state, limits, progress, goal and history
  plan     Its tasks with their dependencies and acceptance criteria
  approve  Open it: the draft's tasks become Ready (the operator only)
  pause    Stop handing out tasks (the operator or an agent)
  resume   Run a paused session again (the operator only)
  stop     End it and release every claim (the operator or an agent)
  archive  Hide a session that is not running (the operator only)
  budget   Budget, limits, access level, auto-close, spend (the operator only)`,
		Examples: []string{
			"monoagentcli --profile work task session list",
			"monoagentcli --profile work task session plan 3",
		},
	},
	{
		Name:  "task session new",
		Short: "Start a session, as a draft: its tasks cannot be taken until the operator approves the plan (an agent may start one; only the operator sets the budget, limits and access level)",
		Usage: "monoagentcli [--profile P] task session new TITLE... [--goal TEXT] [--conversation ID] [--client-id KEY] [--budget-usd N] [--max-tasks N] [--max-depth N] [--access LEVEL] [--as NAME]",
		Flags: `  --goal string          What the session is for
  --conversation string  The chat conversation it belongs to
  --client-id string     An idempotency key (up to 64 letters, digits, - or _): a repeat in the profile returns the same session with deduped true, and creates no second one
  --budget-usd float     Budget in dollars (the operator only; 0 for none)
  --max-tasks int        Most tasks it holds (the operator only; default 200)
  --max-depth int        Deepest nesting of tasks (the operator only; default 3)
  --access string        scoped (default), coding, workspace or full: what its workers may do (the operator only)`,
		Examples: []string{`monoagentcli --profile work task session new "Migrate the logger" --goal "one logger everywhere" --as lead-1`},
	},
	{
		Name:  "task session list",
		Short: "The profile's sessions, newest first, with progress and spend",
		Usage: "monoagentcli [--profile P] task session list [--state S] [--all]",
		Flags: `  --state string  Only sessions in this state: draft, running, paused, done, stopped, failed
  --all           Include archived sessions`,
		Examples: []string{"monoagentcli --profile work --json task session list --state running"},
	},
	{
		Name:     "task session show",
		Short:    "One session with its limits, progress, goal and history",
		Usage:    "monoagentcli [--profile P] task session show ID [--events N]",
		Flags:    `  --events int  The newest N events of its history (default 50; 0 for all)`,
		Examples: []string{"monoagentcli --profile work task session show 3"},
	},
	{
		Name:     "task session plan",
		Short:    "A session's tasks, with what each waits for and how it is judged",
		Usage:    "monoagentcli [--profile P] task session plan ID",
		Examples: []string{"monoagentcli --profile work --json task session plan 3"},
	},
	{
		Name:     "task session approve",
		Short:    "Approve a draft's plan: its tasks become Ready and the session runs (the operator only; an empty draft is refused)",
		Usage:    "monoagentcli [--profile P] task session approve ID",
		Examples: []string{"monoagentcli --profile work task session approve 3"},
	},
	{
		Name:     "task session pause",
		Short:    "Stop a running session handing out tasks; claims stay (the operator or an agent)",
		Usage:    "monoagentcli [--profile P] task session pause ID [--as NAME]",
		Examples: []string{"monoagentcli --profile work task session pause 3"},
	},
	{
		Name:     "task session resume",
		Short:    "Run a paused session again (the operator only: an agent could undo the operator's pause)",
		Usage:    "monoagentcli [--profile P] task session resume ID",
		Examples: []string{"monoagentcli --profile work task session resume 3"},
	},
	{
		Name:     "task session stop",
		Short:    "End a session for good: every claim is released and its tasks stay where they are (the operator or an agent)",
		Usage:    "monoagentcli [--profile P] task session stop ID [--as NAME]",
		Examples: []string{"monoagentcli --profile work task session stop 3"},
	},
	{
		Name:     "task session archive",
		Short:    "Hide a session that is not running or paused; its tasks stay (the operator only)",
		Usage:    "monoagentcli [--profile P] task session archive ID",
		Examples: []string{"monoagentcli --profile work task session archive 3"},
	},
	{
		Name:  "task session budget",
		Short: "Set a session's budget, limits, access level or auto-close setting, or record spend (the operator only)",
		Usage: "monoagentcli [--profile P] task session budget ID [--usd N] [--max-tasks N] [--max-depth N] [--max-retries N] [--access LEVEL] [--auto-close on|off] [--spend N] [--note TEXT]",
		Flags: `  --usd float        Budget in dollars (0 for none); a session that has spent it hands out no tasks
  --max-tasks int    Most tasks the session holds (the only bound on additions after approval)
  --max-depth int    Deepest nesting of tasks
  --max-retries int  Failed verifications a task may have before it is blocked (default 2)
  --access string    scoped, coding, workspace or full
  --auto-close string  on: a verifier's pass closes the task to Done; off (the default): only the operator closes it
  --spend float      Record this much more spent, in dollars
  --note string      What the spend was for`,
		Examples: []string{"monoagentcli --profile work task session budget 3 --usd 20 --auto-close off"},
	},
	{
		Name:  "task split",
		Short: "Replace a session task that has not begun by two or more; they keep its dependencies and what waited for it waits for all of them (the operator or an agent)",
		Usage: "monoagentcli [--profile P] task split ID --into \"TITLE|ACCEPTANCE\" --into \"TITLE|ACCEPTANCE\"... [--as NAME]",
		Flags: `  --into string  One part, as TITLE|ACCEPTANCE; give it two or more times (at most 20)`,
		Examples: []string{
			`monoagentcli --profile work task split 12 --into "Parse the flag|go test ./cmd -run Flag passes" --into "Use it|the flag changes the output" --as lead-1`,
		},
	},
	{
		Name:  "task verify",
		Short: "A verdict on a session task in Review: --pass marks it verified (closing it stays the operator's unless auto-close is on); --fail sends it back to Ready with the note, up to max retries, then blocks it",
		Usage: "monoagentcli [--profile P] task verify ID (--pass | --fail) [--note TEXT] [--as NAME]",
		Flags: `  --pass         The task meets its acceptance criteria
  --fail         It does not (needs --note)
  --note string  What you checked, or what is wrong`,
		Examples: []string{
			`monoagentcli --profile work task verify 12 --pass --note "go test passes" --as checker-1`,
			`monoagentcli --profile work task verify 12 --fail --note "the flag is ignored" --as checker-1`,
		},
	},
	{
		Name:  "task artifact",
		Short: "Attach evidence to a session task: a commit, a file, a report, test output (the operator, the agent that holds the task, or a verifier of a task in Review)",
		Usage: "monoagentcli [--profile P] task artifact ID --kind KIND --ref REF [--note TEXT] [--as NAME]",
		Flags: `  --kind string  commit, file, report, test, diff, log, url or other
  --ref string   A commit hash, a path, a URL
  --note string  What it shows`,
		Examples: []string{`monoagentcli --profile work task artifact 12 --kind commit --ref 4f129c2 --as worker-1`},
	},
	{
		Name:     "task depend",
		Short:    "Make a session task wait for others, which must be done first; a cycle is refused (the operator or an agent)",
		Usage:    "monoagentcli [--profile P] task depend ID --on ID[,ID...] [--as NAME]",
		Flags:    `  --on string  The ids it must wait for, comma separated`,
		Examples: []string{"monoagentcli --profile work task depend 14 --on 12,13"},
	},
	{
		Name:     "task undepend",
		Short:    "Stop a session task waiting for others (the operator only)",
		Usage:    "monoagentcli [--profile P] task undepend ID DEPENDENCY_ID...",
		Examples: []string{"monoagentcli --profile work task undepend 14 12"},
	},
}
