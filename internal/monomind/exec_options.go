package monomind

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// ToolHandler executes one bridged tool call (protocol §4). The returned
// string is what the agent sees; an error becomes an ok:false frame.
type ToolHandler func(ctx context.Context, name string, args json.RawMessage) (string, error)

// ExecOptions configures one `monomind agent exec` turn (protocol §3.1).
type ExecOptions struct {
	Runtime string
	Prompt  string
	Model   string
	// Effort sets the reasoning effort level (e.g. "low", "medium", "high", "xhigh", "max").
	Effort string
	// EffortFlag passes Effort as `--effort` (monomind has
	// CapAgentExecEffort, which maps it per runtime); otherwise only claude
	// gets it, through the CLAUDE_EFFORT env.
	EffortFlag bool
	Cwd        string
	Resume     string
	// SystemPrompt, when set, is written to a temp file and passed via
	// --system-file (avoids argv limits).
	SystemPrompt string
	// Tools enables the stdio bridge (§4); empty passes --tools none
	// explicitly so the turn never inherits monomind's default toolset.
	Tools []ToolSpec
	// OnToolCall executes bridged tool calls; required when Tools is set.
	OnToolCall ToolHandler
	// ToolTimeout bounds each tool round-trip (--tool-timeout).
	ToolTimeout time.Duration
	// Timeout is the overall wall-clock cap (--timeout); zero = none.
	Timeout time.Duration
	// BudgetUSD is the optional spend cap (--budget-usd).
	BudgetUSD float64
	// Attach passes each file as `--attach`; only a monomind with
	// CapAgentExecAttachments takes it, which the caller checks.
	Attach []string
	// Env adds KEY=VALUE entries to the monomind process environment.
	Env map[string]string
	// Bin overrides the monomind binary (tests); empty = discovery.
	Bin string
	// AllowBashPrefixes lets the underlying agent's own Bash tool run
	// commands starting with one of these prefixes (e.g. "monomind",
	// "monoagentcli"), on top of whatever Tools are wired — scoped, not a
	// blanket Bash grant (see agent-exec.ts's canUseTool). Real shell
	// access to a well-known CLI is far more reliable for the model to
	// actually use than a large custom tool surface alone.
	AllowBashPrefixes []string
	// Access "full" runs the turn with the runner's own tools unrestricted
	// (monomind#355); "" keeps monomind's scoped default. Needs Cwd.
	Access string
	// Settings lists the Claude Code setting sources the turn loads
	// ("user", "project", "local"; monomind#356). Empty loads none.
	Settings []string
	// MaxTurns caps agent turns (--max-turns); zero keeps monomind's default.
	MaxTurns int
	// Sandbox asks for a sandbox mode (TurnSandboxMode); SandboxArgs
	// decides what that means for this runtime and monomind. A monomind
	// that can do neither runs the turn exactly as without it. "" asks for
	// nothing (coder mode, which has its own full-access contract).
	Sandbox string
	// RequireSandbox fails closed: when Sandbox is set but this runtime and
	// monomind can't apply it now (SandboxArgs' verdict isn't
	// SandboxStatusSandboxed), Exec starts nothing and returns
	// ErrSandboxRequired instead of running the turn unconfined. A caller
	// that decided from an older scan (a dynamic-org research worker) relies
	// on it.
	RequireSandbox bool
	// WorkspacePurpose names the SandboxWorkspaceDir a sandboxed turn with
	// no Cwd runs in, so workspace-write has a real folder. Ignored when
	// no sandbox args are passed, when Cwd is set, and for the claude runtime:
	// claude is restricted by --access scoped rather than a folder, and
	// keys its resumable sessions by folder, so moving it would orphan
	// every existing conversation.
	WorkspacePurpose string
	// TempDir is where the prompt, system-prompt and tools files Exec writes for
	// monomind are created. Empty means a private folder of the monoagent home
	// (see execTempDir), not the system temp directory, which a turn in a
	// workspace-write sandbox may write: another turn could rewrite the files
	// between their creation and monomind reading them. A caller that runs
	// such turns itself gives each its own folder.
	TempDir string
	// Stderr receives monomind's diagnostics; nil means os.Stderr.
	Stderr io.Writer
	// OnStart, when set, is called once with the pid of the monomind process right after it has
	// started (before any event is read). The agent runtime and every shell and tool server it
	// starts are its descendants, so a caller can bind a credential to that process tree.
	OnStart func(pid int)
}
