package extension

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/orgsign"
)

// cliCoder is the real coderCLI: this binary, never with the bridge's agent
// markers. Every command a coder.* request runs (Run) is without
// coderAgentEnv: the request came from the person's browser, so a bridge
// that happens to run under an agent's shell must not make it an agent's
// request by its environment. That is all the stripping does: the CLI's
// turn-aware gate (opgate) also follows the process ancestry, so a bridge
// started from a shell inside a coder session's turn is refused for the
// operator-only verbs it runs (coder queue add, record save, ...) whatever its
// children's environment says. The supported bridge is the systemd user
// service (SECURITY.md); one started from a turn shell is restarted from the
// user's own terminal. A coder turn (Start: coder.send, which only runs after the
// desktop-started check) is also without the run markers (coderTurnEnv): a
// bridge started from a chat or a workflow run must not make the person's
// own follow-up a run's turn, which the coder-turn gate refuses. The bridge
// itself keeps its environment.
type cliCoder struct{ chat cliChatBackend }

// coderAgentEnv is browserctl.AgentContextEnv (the agent markers), and
// coderTurnEnv is operator.MarkerEnv (those plus the run markers and the
// agent kind): this package can import neither (both import it), and tests
// hold each copy equal to its source.
func coderAgentEnv() []string {
	return append(orgsign.AgentContextMarkers(), hilsend.EnvAgentContext, "MONOAGENT_ACTOR")
}

func coderTurnEnv() []string {
	return append(coderAgentEnv(),
		"MONOAGENT_WORKFLOW_RUN", "MONOAGENT_AI_CALLER", "MONOAGENT_AGENT_KIND", "MONOAGENT_AGENT_KIND_TOKEN")
}

// without is env minus the variables named in drop.
func without(env, drop []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !slices.Contains(drop, k) {
			out = append(out, kv)
		}
	}
	return out
}

// command builds the child: a turn's without coderTurnEnv, any other
// without coderAgentEnv.
func (c *cliCoder) command(ctx context.Context, stdin string, args []string, turn bool) (*exec.Cmd, error) {
	bin, err := c.chat.binary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = monomindWaitDelay
	cmd.Stdin = strings.NewReader(stdin)
	drop := coderAgentEnv()
	if turn {
		drop = coderTurnEnv()
	}
	cmd.Env = without(os.Environ(), drop)
	return cmd, nil
}

func (c *cliCoder) Run(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd, err := c.command(ctx, stdin, args, false)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, &RequestError{Code: CodeTimeout, Err: ctx.Err()}
		}
		return nil, cliFailure(stdout.Bytes(), stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

type execProc struct {
	cmd *exec.Cmd
	out io.Reader
}

func (p *execProc) Stdout() io.Reader { return p.out }
func (p *execProc) Wait() error       { return p.cmd.Wait() }

func (c *cliCoder) Start(ctx context.Context, stdin string, args ...string) (coderProc, error) {
	cmd, err := c.command(ctx, stdin, args, true)
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start monoagentcli: %w", err)
	}
	return &execProc{cmd: cmd, out: out}, nil
}
