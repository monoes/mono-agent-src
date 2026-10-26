package catalog

// The `task` entries of `ref commands`, kept apart from
// commands.go (the `ref tasks` topic is internal/cli/ref's). The CLI's ref_tasks_test.go holds
// them to the real commands: every flag, every subcommand and every limit they state.
var taskCommandDocs = []cmdDoc{
	{
		Name:  "task add",
		Short: "Add a task to the profile's Inbox (straight to Ready with --ready: the operator only)",
		Usage: "monoagentcli [--profile P] task add [TITLE...] [--stdin] [--notes TEXT] [--ready] [--source cli|app|os] [--url U] [--source-title T] [--app A] [--client-id ID] [--session N --acceptance TEXT [--depends-on IDS] [--parent ID] [--kind K] [--executor E]] [--as NAME]",
		Flags: `  --stdin                Read the text from standard input (at most 1 MiB): with no title its first line is the title, with a title the text is the notes
  --notes string         Notes (needs a title; not together with --stdin)
  --ready                Add straight to Ready (the operator only: refused for an AI agent)
  --source string        cli (default), app (the desktop app) or os (the macOS menu); an AI agent's tasks are always agent tasks
  --url string           The page the task came from (http or https; another link is dropped)
  --source-title string  The title of that page
  --app string           The application the text was selected in
  --client-id string     Idempotency key: adding the same key twice adds one task
  --session string       Add to this task session instead (a draft until the operator approves its plan, Ready at once after; needs --acceptance)
  --acceptance string    With --session: how anyone will know the task is done (required)
  --depends-on string    With --session: ids of tasks it waits for, comma separated; they must be done first
  --parent string        With --session: the task it belongs under
  --kind string          With --session: task (default), epic (a group, never handed out) or check
  --executor string      With --session: the executor it suits (lead, worker or subagent)`,
		Examples: []string{
			"monoagentcli --profile work task add Fix the flaky test",
			`monoagentcli --profile work task add "Review the invoice" --notes "From Sam, due Friday"`,
			"pbpaste | monoagentcli --profile work task add --stdin",
			`monoagentcli --profile work task add "Timeout in the deploy log" --as claude-7f3a`,
			`monoagentcli --profile work task add "Parse the flag" --session 3 --acceptance "go test ./cmd -run Flag passes" --depends-on 11`,
		},
	},
	{
		Name:  "task list",
		Short: "List the profile's tasks (the operator: the five columns; an AI agent: ready, in progress and review, and the Inbox too if the operator allowed it with task agents allow)",
		Usage: "monoagentcli [--profile P] task list [--status S[,S...]] [--source K] [--claimed-by NAME] [--stale] [--limit N] [--session N] [--as NAME]",
		Flags: `  --status string      Only these columns, comma separated: inbox, ready, in_progress, review, done, archived (the archive, and for an agent the Inbox and Done, only when named; the Inbox is listed to an agent without being named when the operator allowed viewing with task agents allow)
  --source string      Only tasks from this source: cli, app, chrome, os, agent
  --claimed-by string  Only tasks held by this agent
  --stale              Only claims whose lease has run out
  --limit int          At most this many tasks (default 500, at most 2,000); in text, a list that is cut says how many more there are
  --session string     List this task session's tasks instead of the personal board's`,
		Examples: []string{
			"monoagentcli --profile work --json task list --status ready",
			"monoagentcli --profile work task list --stale --as claude-7f3a",
		},
	},
	{
		Name:  "task board",
		Short: "The whole board: the five columns, the counts and the revision (the operator; an AI agent uses task list, unless the operator allowed viewing with task agents allow)",
		Usage: "monoagentcli [--profile P] task board [--done-limit N] [--session N]",
		Flags: `  --done-limit int  Show at most this many Done cards (default 50; 0 shows them all)
  --session string  Show this task session's board instead of the personal one`,
		Examples: []string{"monoagentcli --profile work task board", "monoagentcli --profile work --json task board --done-limit 10"},
	},
	{
		Name:     "task show",
		Short:    "One task with its history, whatever its column",
		Usage:    "monoagentcli [--profile P] task show ID [--as NAME]",
		Examples: []string{"monoagentcli --profile work task show 12", "monoagentcli --profile work --json task show 12"},
	},
	{
		Name:  "task edit",
		Short: "Change a task's title or notes (the operator only)",
		Usage: "monoagentcli [--profile P] task edit ID [--title T] [--notes TEXT]",
		Flags: `  --title string  New title (not empty)
  --notes string  New notes (an empty value clears them)`,
		Examples: []string{`monoagentcli --profile work task edit 12 --title "Fix the flaky login test"`},
	},
	{
		Name:  "task move",
		Short: "Move a task to a column and a place in it (the operator only; a task already in that column stays where it is unless you give a place)",
		Usage: "monoagentcli [--profile P] task move ID STATUS [--before ID | --after ID | --top | --bottom]",
		Flags: `  STATUS           inbox, ready, in_progress, review or done (archiving has its own command)
  --before string  Put it just above this task (an id in that column)
  --after string   Put it just below this task (an id in that column)
  --top            Put it at the top of the column
  --bottom         Put it at the bottom of the column`,
		Examples: []string{
			"monoagentcli --profile work task move 12 in_progress",
			"monoagentcli --profile work task move 12 ready --top",
		},
	},
	{
		Name:     "task approve",
		Short:    "Move Inbox tasks to Ready, where an AI agent may take them (the operator; a named AI agent only if the operator allowed approving with task agents allow; all or none)",
		Usage:    "monoagentcli [--profile P] task approve ID... [--top] [--as NAME]",
		Flags:    `  --top  Put the tasks at the top of Ready, the first id on top, instead of at the bottom`,
		Examples: []string{"monoagentcli --profile work task approve 12 13", "monoagentcli --profile work task approve 12 13 --top"},
	},
	{
		Name:  "task agents",
		Short: "What AI agents may do with the Inbox of a profile: show, allow, deny (off by default; allow and deny are the operator's)",
		Usage: "monoagentcli [--profile P] task agents show|allow|deny",
		Flags: `  show     What is delegated now (an agent may run it)
  allow    Delegate: name what with a flag (the operator only)
  deny     Take back, at once: no flag takes back both (the operator only)`,
		Examples: []string{
			"monoagentcli --profile Work task agents show",
			"monoagentcli --profile Work task agents deny",
		},
	},
	{
		Name:     "task agents show",
		Short:    "Show whether AI agents may see the board and the Inbox, and approve Inbox tasks, on this profile (an agent may run it)",
		Usage:    "monoagentcli [--profile P] task agents show",
		Examples: []string{"monoagentcli --profile work --json task agents show"},
	},
	{
		Name:  "task agents allow",
		Short: "Let AI agents see the board and the Inbox, or approve Inbox tasks, on this profile (the operator only; off by default)",
		Usage: "monoagentcli [--profile P] task agents allow [--view] [--approve]",
		Flags: `  --view     Agents see the whole board (task board) and the Inbox in task list and the task_list tool
  --approve  A named agent (--as NAME) approves Inbox tasks the operator or a capture created, at most 10 a call, recorded as delegated; needs view too; task_approve joins the tasks-only MCP server`,
		Examples: []string{"monoagentcli --profile Work task agents allow --view"},
	},
	{
		Name:  "task agents deny",
		Short: "Take back what task agents allow delegated, at once (the operator only; no flag takes back both)",
		Usage: "monoagentcli [--profile P] task agents deny [--view] [--approve]",
		Flags: `  --view     Agents no longer see the board and the Inbox unless they name it
  --approve  Agents no longer approve`,
		Examples: []string{"monoagentcli --profile Work task agents deny --approve"},
	},
	{
		Name:  "task archive",
		Short: "Hide tasks from the board, keeping them (the operator only)",
		Usage: "monoagentcli [--profile P] task archive ID... | --status STATUS",
		Flags: `  --status string  Archive every task of this column (inbox, ready, in_progress, review or done) instead of naming ids`,
		Examples: []string{
			"monoagentcli --profile work task archive 12",
			"monoagentcli --profile work task archive --status done",
		},
	},
	{
		Name:     "task unarchive",
		Short:    "Bring archived tasks back to the column they came from (the operator only)",
		Usage:    "monoagentcli [--profile P] task unarchive ID...",
		Examples: []string{"monoagentcli --profile work task unarchive 12"},
	},
	{
		Name:  "task next",
		Short: "The task an AI agent should work next, or take it with --claim (the task is null in --json when nothing is ready)",
		Usage: "monoagentcli --profile P task next [--claim --as NAME [--lease 30m]] [--session N]",
		Flags: `  --claim           Take the task for yourself in one step (needs --as NAME)
  --lease duration  How long you hold it (default 30m, at most 24h; above zero, and only with --claim); a comment extends it only to 30 minutes from the comment
  --session string  Work this session instead: a Ready task whose dependencies are done, of a running session (nothing from a draft or a paused one)`,
		Examples: []string{
			"monoagentcli --profile work task next",
			"monoagentcli --profile work task next --claim --as claude-7f3a",
		},
	},
	{
		Name:     "task claim",
		Short:    "Take a ready task (an AI agent), renew your hold on one, or take over a claim whose lease has run out",
		Usage:    "monoagentcli --profile P task claim ID --as NAME [--lease 30m]",
		Flags:    `  --lease duration  How long you hold it (default 30m, at most 24h; above zero)`,
		Examples: []string{"monoagentcli --profile work task claim 12 --as claude-7f3a"},
	},
	{
		Name:     "task comment",
		Short:    "Add a note to a task: the operator's own, or an AI agent's progress report on a task it holds (extends its lease to 30 minutes from the comment, if that is later)",
		Usage:    "monoagentcli --profile P task comment ID TEXT... [--as NAME]",
		Examples: []string{`monoagentcli --profile work task comment 12 --as claude-7f3a "reproduced it locally"`},
	},
	{
		Name:  "task finish",
		Short: "Hand a task you hold back to the operator for review, with a result or a question (exactly one of them)",
		Usage: "monoagentcli --profile P task finish ID --as NAME (--result TEXT | --question TEXT)",
		Flags: `  --result string    What you did
  --question string  What you need to know before you can go on`,
		Examples: []string{
			`monoagentcli --profile work task finish 12 --as claude-7f3a --result "opened PR 41"`,
			`monoagentcli --profile work task finish 12 --as claude-7f3a --question "which database?"`,
		},
	},
	{
		Name:     "task release",
		Short:    "Give a task you hold back to Ready, behind the others",
		Usage:    "monoagentcli --profile P task release ID --as NAME [--note TEXT]",
		Flags:    `  --note string  Why you are giving it back`,
		Examples: []string{`monoagentcli --profile work task release 12 --as claude-7f3a --note "needs the VPN"`},
	},
	{
		Name:     "task digest",
		Short:    "A short summary of the ready tasks for a session-start hook: in text nothing when none is ready, and exit 0 whatever goes wrong at run time",
		Usage:    "monoagentcli --profile P task digest",
		Flags:    `  (no flags of its own: with --json it prints {"profile","ready","next"} on success, next null when nothing is ready; a digest that fails prints one line on standard error and exits 0)`,
		Examples: []string{"monoagentcli --profile work task digest"},
	},
}
