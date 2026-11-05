package system

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/monoes/mono-agent/internal/nodemgr"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ExecuteCommandNode runs a shell command and captures output.
// Type: "system.execute_command"
type ExecuteCommandNode struct{}

// Command output caps. stdout and stderr are each capped at
// maxCommandOutputBytes; output past the cap is dropped and the result
// item carries "truncated": true.
const (
	cmdDefaultTimeoutSecs = 30
	cmdMaxTimeoutSecs     = 3600
	maxCommandOutputBytes = 10 << 20 // 10MB each for stdout/stderr
)

// limitedBuffer is a bytes.Buffer that stops storing after max bytes but
// keeps reporting successful writes so the command keeps running.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() >= b.max {
		b.truncated = true
		return len(p), nil
	}
	room := b.max - b.buf.Len()
	if len(p) > room {
		b.buf.Write(p[:room])
		b.truncated = true
	} else {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (n *ExecuteCommandNode) Type() string { return "system.execute_command" }

func (n *ExecuteCommandNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	command, _ := config["command"].(string)
	if command == "" {
		return nil, fmt.Errorf("system.execute_command: 'command' is required")
	}
	if err := workflow.RefuseUnapprovedProgram(ctx, "system.execute_command", input.WorkflowID); err != nil {
		return nil, err
	}

	var args []string
	if rawArgs, ok := config["args"].([]interface{}); ok {
		for _, a := range rawArgs {
			args = append(args, fmt.Sprintf("%v", a))
		}
	}

	workingDir, _ := config["working_dir"].(string)

	// A non-positive timeout falls back to the default (previously a
	// config value of 0 killed the command instantly); anything above the
	// 3600s ceiling is clamped.
	timeoutSecs := cmdDefaultTimeoutSecs
	if v, ok := config["timeout"].(float64); ok && int(v) > 0 {
		timeoutSecs = int(v)
	} else if v, ok := config["timeout_seconds"].(float64); ok && int(v) > 0 {
		timeoutSecs = int(v)
	}
	if timeoutSecs > cmdMaxTimeoutSecs {
		timeoutSecs = cmdMaxTimeoutSecs
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	// The user's own PATH, not monoagent's: when monoagent put its managed
	// Node first (nodemgr.Activate), a workflow's `node`/`npm` must still be
	// the user's (docs: `monoagentcli nodejs --help`). A bare name is looked
	// up there too, since exec resolves it against this process's PATH.
	bin := command
	if p, err := nodemgr.LookPathUser(command); err == nil {
		bin = p
	}
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Env = nodemgr.UserEnv(os.Environ())

	if workingDir != "" {
		cmd.Dir = workingDir
	}

	// Extra environment variables
	if envMap, ok := config["env"].(map[string]interface{}); ok {
		for k, v := range envMap {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%v", k, v))
		}
	}
	// Last, so the env above can't clear it: what a workflow runs is never the operator.
	cmd.Env = workflow.RunEnvFor(ctx, cmd.Env, input.ExecutionID)

	stdoutBuf := &limitedBuffer{max: maxCommandOutputBytes}
	stderrBuf := &limitedBuffer{max: maxCommandOutputBytes}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	exitCode := 0
	runErr := cmd.Run()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	result := map[string]interface{}{
		"stdout":    stdoutBuf.buf.String(),
		"stderr":    stderrBuf.buf.String(),
		"exit_code": exitCode,
	}
	if stdoutBuf.truncated || stderrBuf.truncated {
		result["truncated"] = true
	}
	resultItem := workflow.NewItem(result)

	var outputs []workflow.NodeOutput
	outputs = append(outputs, workflow.NodeOutput{Handle: "main", Items: []workflow.Item{resultItem}})
	if exitCode != 0 {
		outputs = append(outputs, workflow.NodeOutput{Handle: "error", Items: []workflow.Item{resultItem}})
	}
	return outputs, nil
}
