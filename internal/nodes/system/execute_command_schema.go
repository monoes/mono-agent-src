package system

// ExecuteCommandNodeSchema documents the config keys
// ExecuteCommandNode.Execute reads out of its map[string]interface{}
// config — see internal/nodes/control.SetNodeSchema's doc comment for why
// this is a companion struct rather than the runtime config, and
// internal/tools/schemagen for the tag grammar.
//
// Two fields have no entry in the hand-written schemas/system.execute_command.json
// this replaces, both of which Execute has always read:
//
//   - "args" — passed as separate argv entries to exec.CommandContext
//     (not shell-interpolated into "command"), so without exposing it a
//     workflow could only run commands that need no arguments at all.
//   - "env" — extra environment variables merged on top of os.Environ()
//     for the child process.
//
// Execute also reads a "timeout_seconds" config key as a fallback when
// "timeout" is unset — that's an undocumented alias for the same value, not
// a distinct config, so it isn't given its own schema entry.
type ExecuteCommandNodeSchema struct {
	Command string `json:"command" schema:"label=Shell Command,type=text,required,placeholder=echo hello world,help=Executed directly (not via a shell) — no shell operators like pipes or redirects."`

	Args string `json:"args" schema:"label=Arguments,type=array,item_type=text,help=Extra argv entries passed to the command， each as a separate array item (no shell quoting needed)."`

	WorkingDir string `json:"working_dir" schema:"label=Working Directory,type=text,placeholder=/tmp"`

	Timeout float64 `json:"timeout" schema:"label=Timeout (seconds),type=number,default=30,help=Clamped to 3600s max. A value of 0 or blank uses the default."`

	Env string `json:"env" schema:"label=Extra Environment Variables (JSON object),type=textarea,rows=3,placeholder={ \"API_KEY\": \"{{$env.API_KEY}}\" },help=Merged on top of the process's own environment for the child command."`
}
