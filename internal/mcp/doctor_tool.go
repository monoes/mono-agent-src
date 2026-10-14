package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// doctorExecutable is the monoagentcli binary the doctor tool runs: this
// process's own (the MCP server is `monoagentcli mcp`). Tests replace it.
var doctorExecutable = os.Executable

func doctorTool() tool {
	list := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": desc}
	}
	return tool{
		name: "doctor",
		description: "Run monoagent's health checks and return the report of `monoagentcli doctor --json` (each check's status, message and the fix it offers). " +
			"It applies no fix: the user runs `monoagentcli doctor --fix` or `doctor fix <id>`. The runtimes group asks every agent CLI for its version, which takes a while: skip_group leaves it out.",
		schema: objSchema(map[string]interface{}{
			"group":      list("Only run checks of these groups (e.g. core)"),
			"skip_group": list("Leave out checks of these groups (e.g. runtimes)"),
			"check":      list("Only run these checks (and what they depend on)"),
		}),
		annotations: map[string]bool{"readOnlyHint": true},
		handler:     toolDoctor,
	}
}

// toolDoctor runs `monoagentcli doctor --json` with this server's database
// and profile, so the report is the command's, byte for byte. Its exit
// status 1 (a required check failed) still has the report on stdout.
func toolDoctor(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Group     stringList `json:"group"`
		SkipGroup stringList `json:"skip_group"`
		Check     stringList `json:"check"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	bin, err := doctorExecutable()
	if err != nil {
		return nil, fmt.Errorf("doctor: find monoagentcli: %w", err)
	}
	argv := []string{"doctor", "--json"}
	if s.opts.DBPath != "" {
		argv = append(argv, "--db-path", s.opts.DBPath)
	}
	if s.opts.Profile != "" {
		argv = append(argv, "--profile", s.opts.Profile)
	}
	for _, f := range []struct {
		flag   string
		values []string
	}{{"--group", a.Group}, {"--skip-group", a.SkipGroup}, {"--check", a.Check}} {
		flag := f.flag
		for _, v := range f.values {
			if strings.HasPrefix(v, "-") {
				return nil, fmt.Errorf("%s: %q is not a name", strings.TrimPrefix(flag, "--"), v)
			}
			argv = append(argv, flag, v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) > 0 && json.Valid(out) {
		return json.RawMessage(out), nil
	}
	if runErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("doctor failed: %s", msg)
		}
		return nil, fmt.Errorf("doctor failed: %w", runErr)
	}
	return nil, fmt.Errorf("doctor printed no report")
}

// stringList decodes a JSON array of strings or a single string, which models
// often pass for a one-element list.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one != "" {
			*l = stringList{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}
