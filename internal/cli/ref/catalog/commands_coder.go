package catalog

// The `coder host` and `coder sessions` entries of `ref commands`: persistent coder sessions
// that keep going when the app closes (AGENTS.md, "Persistent coder sessions"). The CLI's
// ref_coder_test.go holds them to the real commands: every flag and every subcommand.
var coderCommandDocs = []cmdDoc{
	{
		Name:     "coder host",
		Short:    "The detached process that keeps coder sessions running after the app closes (run, status, stop)",
		Usage:    "monoagentcli [--profile P] coder host run|status|stop",
		Examples: []string{"monoagentcli --profile work coder host status --json"},
	},
	{
		Name:     "coder host run",
		Short:    "Run the coder host in the foreground (the operator only; `coder sessions send` starts one by itself)",
		Usage:    "monoagentcli [--profile P] coder host run [--poll DURATION]",
		Flags:    `  --poll duration  How often the host looks for commands (default 500ms)`,
		Examples: []string{"monoagentcli --profile work coder host run"},
	},
	{
		Name:     "coder host status",
		Short:    "Whether the coder host is running, its pid and since when",
		Usage:    "monoagentcli [--profile P] coder host status",
		Examples: []string{"monoagentcli --profile work coder host status --json"},
	},
	{
		Name:     "coder host stop",
		Short:    "Stop the host (the operator only); running turns keep going and the next host picks them up",
		Usage:    "monoagentcli [--profile P] coder host stop",
		Examples: []string{"monoagentcli --profile work coder host stop"},
	},
	{
		Name:     "coder sessions",
		Short:    "Coder sessions the host carries on: list, send, stop, pause, resume, spawn, status, command, settings",
		Usage:    "monoagentcli [--profile P] coder sessions list|send|stop|pause|resume|spawn|status|command|settings",
		Examples: []string{"monoagentcli --profile work coder sessions list --json"},
	},
	{
		Name:     "coder sessions list",
		Short:    "List the sessions the host knows, most recently changed first: state, access, waiting commands",
		Usage:    "monoagentcli [--profile P] coder sessions list",
		Examples: []string{"monoagentcli --profile work coder sessions list --json"},
	},
	{
		Name:  "coder sessions send",
		Short: "Queue a message for a coder conversation; the host starts the turn when the session is free (the operator only; with a master's session token: a descendant only, as a master-made turn)",
		Usage: "monoagentcli [--profile P] coder sessions send CONVERSATION MESSAGE [--turn ID] [--attach FILE]... [--mission] [--no-host]",
		Flags: `  --turn string          Turn id (default: a new one)
  --attach stringArray   A file to attach to the turn (repeatable, at most 10)
  --mission              The user confirmed a mission for this send: queues it marked as a mission and writes a signed, single-use confirmation bound to the command (revoked if the command is cancelled, expires or fails; the send fails instead of running plain if it is gone); the operator only
  --no-host              Queue only; do not start the host`,
		Examples: []string{
			`monoagentcli --profile work coder sessions send <conv> "run the tests and fix what fails"`,
			`monoagentcli --profile work coder sessions send <conv> "what is in this log?" --attach ./build.log`,
		},
	},
	{
		Name:     "coder sessions stop",
		Short:    "End the session's running turn and drop the sends queued before the stop (the operator only)",
		Usage:    "monoagentcli [--profile P] coder sessions stop CONVERSATION",
		Examples: []string{"monoagentcli --profile work coder sessions stop <conv>"},
	},
	{
		Name:     "coder sessions pause",
		Short:    "Start no new turn for the session until it is resumed; a running turn finishes (the operator only)",
		Usage:    "monoagentcli [--profile P] coder sessions pause CONVERSATION",
		Examples: []string{"monoagentcli --profile work coder sessions pause <conv>"},
	},
	{
		Name:     "coder sessions resume",
		Short:    "Lift a pause (the operator only)",
		Usage:    "monoagentcli [--profile P] coder sessions resume CONVERSATION",
		Examples: []string{"monoagentcli --profile work coder sessions resume <conv>"},
	},
	{
		Name:  "coder sessions spawn",
		Short: "A master session creates a child session below itself (needs the session token of a master's turn; limits: max_children, max_depth, spawn rate, access never above the master's and never full, budget carved from the master's)",
		Usage: "monoagentcli [--profile P] coder sessions spawn [--message TEXT] [--access coding|workspace] [--cwd DIR] [--budget-usd N]",
		Flags: `  --message string     The child's first message
  --access string      coding or workspace, at most the master's (default: the master's, capped at workspace)
  --cwd string         A folder inside the master's (default: the master's)
  --budget-usd float   Spend cap for the child, carved out of the master's remaining budget`,
		Examples: []string{`monoagentcli --profile work coder sessions spawn --message "write the parser tests" --budget-usd 2`},
	},
	{
		Name:     "coder sessions status",
		Short:    "A master's view of its own session or a descendant, or its whole subtree without a conversation (needs the session token of a coder turn)",
		Usage:    "monoagentcli [--profile P] coder sessions status [CONVERSATION]",
		Examples: []string{"monoagentcli --profile work coder sessions status --json"},
	},
	{
		Name:     "coder sessions command",
		Short:    "Show how one queued command ended: pending, done, or failed with the reason",
		Usage:    "monoagentcli [--profile P] coder sessions command ID",
		Examples: []string{"monoagentcli --profile work coder sessions command 42 --json"},
	},
	{
		Name:     "coder sessions decisions",
		Short:    "The decisions made for a session without asking a person (the ledger): list, show, revert",
		Usage:    "monoagentcli [--profile P] coder sessions decisions list|show|revert CONVERSATION",
		Examples: []string{"monoagentcli --profile work coder sessions decisions list <conv> --json"},
	},
	{
		Name:  "coder sessions decisions list",
		Short: "A session's decisions, newest first: class, question, choice, alternatives, reason, reversible, made by, status",
		Usage: "monoagentcli [--profile P] coder sessions decisions list CONVERSATION [--class C] [--status active|reverted] [--limit N]",
		Flags: `  --class string   Only this class of gap
  --status string  Only active or reverted decisions
  --limit int      At most this many (default 100, most 500)`,
		Examples: []string{"monoagentcli --profile work coder sessions decisions list <conv> --status active --json"},
	},
	{
		Name:     "coder sessions decisions show",
		Short:    "One decision of a session",
		Usage:    "monoagentcli [--profile P] coder sessions decisions show CONVERSATION ID",
		Examples: []string{"monoagentcli --profile work coder sessions decisions show <conv> 7 --json"},
	},
	{
		Name:     "coder sessions decisions revert",
		Short:    "Mark a reversible, active decision reverted in the ledger (the operator only); it does not start a turn or tell the session",
		Usage:    "monoagentcli [--profile P] coder sessions decisions revert CONVERSATION ID",
		Examples: []string{"monoagentcli --profile work coder sessions decisions revert <conv> 7"},
	},
	{
		Name:     "coder sessions questions",
		Short:    "Questions parked while nobody could answer, and who may answer each: list, answer",
		Usage:    "monoagentcli [--profile P] coder sessions questions list|answer",
		Examples: []string{"monoagentcli --profile work coder sessions questions list --json"},
	},
	{
		Name:     "coder sessions questions list",
		Short:    "Parked questions of one conversation, or of every conversation of the profile, with the answerer the policy names",
		Usage:    "monoagentcli [--profile P] coder sessions questions list [CONVERSATION] [--all]",
		Flags:    `  --all  Include the answered ones`,
		Examples: []string{"monoagentcli --profile work coder sessions questions list <conv> --json"},
	},
	{
		Name:     "coder sessions questions answer",
		Short:    "Answer a parked question: the operator when the policy names the user; with a session token (a master's turn) a question of the caller's own child when the policy names the master. The answer is sent to the session by the host as a host-made note",
		Usage:    "monoagentcli [--profile P] coder sessions questions answer CONVERSATION ID ANSWER [--no-host]",
		Flags:    `  --no-host  Queue only; do not start the host`,
		Examples: []string{`monoagentcli --profile work coder sessions questions answer <conv> 3 "yes, only the build directory"`},
	},
	{
		Name:  "coder sessions park",
		Short: "Park a question about your own session when nobody present can answer it (needs the session token of a coder turn; the operator has no session of its own); refused when the policy lets you decide; a session with no master (solo, master, orphaned child) parks only risky_action / missing_credential gaps, which wait for the person with no timeout, and is refused any other (decide and record, or stop); risky_action / missing_credential are never reversible",
		Usage: "monoagentcli [--profile P] coder sessions park --class C --question TEXT [--option TEXT]... [--default TEXT] [--reversible]",
		Flags: `  --class string     ambiguity, scope_growth, verification_failed, child_conflict, risky_action or missing_credential
  --question string  The question
  --option strings   An option you see (repeat)
  --default string   Applied after 30 minutes only where the policy allows a default (never for risky_action or missing_credential)
  --reversible       The choice can be undone cheaply`,
		Examples: []string{`monoagentcli --profile work coder sessions park --class risky_action --question "delete the build dir?" --option yes --option no`},
	},
	{
		Name:  "coder sessions record",
		Short: "Record a decision you made for your own session, shown in the ledger as made by the model (needs the session token of a coder turn); any class may be recorded whatever the autonomy preset (the ledger and its revert are the control) except risky_action / missing_credential, which are refused",
		Usage: "monoagentcli [--profile P] coder sessions record --class C --choice TEXT [--question TEXT] [--alternative TEXT]... [--reason TEXT] [--reversible]",
		Flags: `  --class string        ambiguity, scope_growth, verification_failed, child_conflict, risky_action or missing_credential
  --choice string       What you chose
  --question string     What had to be decided
  --alternative strings An alternative you did not choose (repeat)
  --reason string       Why
  --reversible          The choice can be undone cheaply`,
		Examples: []string{`monoagentcli --profile work coder sessions record --class ambiguity --question "tabs or spaces?" --choice tabs --alternative spaces --reason "repo style" --reversible`},
	},
	{
		Name:     "coder sessions mission-children",
		Short:    "The person's control of a mission's child sessions: set the policy, list it (a session token or a mission binding is refused)",
		Usage:    "monoagentcli [--profile P] coder sessions mission-children set|list",
		Examples: []string{"monoagentcli --profile work coder sessions mission-children list --json"},
	},
	{
		Name:  "coder sessions mission-children set",
		Short: "Change the child-session policy of the mission a conversation leads. An operator who writes a number also sets it as the lead's cap; giving the lead a value without limiting takes two calls (set it, then clear the cap). Mode all is turned on only here",
		Usage: "monoagentcli [--profile P] coder sessions mission-children set LEAD_CONVERSATION [--mode off|marked|all] [--max-children N] [--share-pct P] [--clear-max-children-cap] [--clear-share-pct-cap]",
		Flags: `  --mode string              off, marked (only tasks the lead marked) or all (every worker task)
  --max-children int         How many of the mission's sessions may run at once; also the lead's cap
  --share-pct int            Percent of the lead's remaining budget the sessions may carve (at most 80); also the lead's cap
  --clear-max-children-cap   Lift your cap on the lead's max_children
  --clear-share-pct-cap      Lift your cap on the lead's share_pct`,
		Examples: []string{"monoagentcli --profile work coder sessions mission-children set <lead-conv> --mode marked --max-children 3 --share-pct 40"},
	},
	{
		Name:     "coder sessions mission-children list",
		Short:    "Every child-session policy of the profile and the tasks that run, or ran, in sessions of their own (states and numbers, never task text)",
		Usage:    "monoagentcli [--profile P] coder sessions mission-children list",
		Examples: []string{"monoagentcli --profile work coder sessions mission-children list --json"},
	},
	{
		Name:     "coder sessions settings",
		Short:    "Show or change a coder session's settings: check-ins, autonomy, limits, mesh, unattended, role",
		Usage:    "monoagentcli [--profile P] coder sessions settings get|set CONVERSATION",
		Examples: []string{"monoagentcli --profile work coder sessions settings get <conv> --json"},
	},
	{
		Name:     "coder sessions settings get",
		Short:    "A session's settings, its role, whether it runs unattended, which settings are the operator's, what each allows, and for a master its check-ins in a row against the limit",
		Usage:    "monoagentcli [--profile P] coder sessions settings get CONVERSATION",
		Examples: []string{"monoagentcli --profile work coder sessions settings get <conv> --json"},
	},
	{
		Name:  "coder sessions settings set",
		Short: "Change settings with a JSON object naming only the settings to change (the operator only)",
		Usage: "monoagentcli [--profile P] coder sessions settings set CONVERSATION [--patch JSON] [--unattended true|false] [--role solo|master|child] [--parent MASTER] [--budget-usd N]",
		Flags: `  --patch string       JSON object with the settings to change; a name that is not a setting is refused
  --unattended string  true or false: false turns off the host's own scheduled check-ins for the session (a message you send runs either way)
  --role string        solo, master or child (a child needs --parent: a master with room for it)
  --parent string      The master conversation, with --role child
  --budget-usd float   The session's spend cap in USD that its children are carved from (0 = none)`,
		Examples: []string{
			`monoagentcli --profile work coder sessions settings set <conv> --patch '{"checkin_minutes": 30, "checkin_limit": 12}'`,
			"monoagentcli --profile work coder sessions settings set <conv> --role master",
			"monoagentcli --profile work coder sessions settings set <child> --role child --parent <master>",
			"monoagentcli --profile work coder sessions settings set <conv> --unattended false",
		},
	},
}
