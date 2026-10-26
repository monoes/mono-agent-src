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
		Short:    "Coder sessions the host carries on: list, send, stop, pause, resume, command, settings",
		Usage:    "monoagentcli [--profile P] coder sessions list|send|stop|pause|resume|command|settings",
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
		Short: "Queue a message for a coder conversation; the host starts the turn when the session is free (the operator only)",
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
		Name:     "coder sessions command",
		Short:    "Show how one queued command ended: pending, done, or failed with the reason",
		Usage:    "monoagentcli [--profile P] coder sessions command ID",
		Examples: []string{"monoagentcli --profile work coder sessions command 42 --json"},
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
		Usage: "monoagentcli [--profile P] coder sessions settings set CONVERSATION [--patch JSON] [--unattended true|false] [--role solo|master|child] [--parent MASTER]",
		Flags: `  --patch string       JSON object with the settings to change; a name that is not a setting is refused
  --unattended string  true or false: false turns off the host's own scheduled check-ins for the session (a message you send runs either way)
  --role string        solo, master or child (a child needs --parent: a master with room for it)
  --parent string      The master conversation, with --role child`,
		Examples: []string{
			`monoagentcli --profile work coder sessions settings set <conv> --patch '{"checkin_minutes": 30, "checkin_limit": 12}'`,
			"monoagentcli --profile work coder sessions settings set <conv> --role master",
			"monoagentcli --profile work coder sessions settings set <child> --role child --parent <master>",
			"monoagentcli --profile work coder sessions settings set <conv> --unattended false",
		},
	},
}
