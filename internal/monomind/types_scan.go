package monomind

// ScanEntry is one runtime detection result from `agent scan` (§6).
type ScanEntry struct {
	ID          string  `json:"id"`
	Installed   bool    `json:"installed"`
	Binary      *string `json:"binary"`
	Version     *string `json:"version"`
	InstallHint string  `json:"install_hint"`
	// ExecutionSupported is false for a runtime monomind cannot run headless
	// (no prompt/JSON transport); ExecutionUnsupportedReason is its
	// own sentence for why. Both are nil from a monomind that predates them.
	ExecutionSupported         *bool   `json:"execution_supported,omitempty"`
	ExecutionUnsupportedReason *string `json:"execution_unsupported_reason,omitempty"`
	// Install is InstallHint as a recipe a caller can run without a shell
	// (protocol rev 9); nil from an older monomind.
	Install *InstallRecipe `json:"install,omitempty"`
	// LoginHint is the runtime's own sign-in command (rev 9), when any.
	LoginHint *string `json:"login_hint,omitempty"`
	// StreamsIncrementally (protocol rev 5): mirrors Event's own field —
	// see its doc comment for why this deliberately has no `omitempty`.
	// Static per-runtime metadata: unlike Installed/Version it never
	// depends on probing the binary, so it's present even when
	// Installed is false.
	StreamsIncrementally bool `json:"streams_incrementally"`

	// Coder-mode metadata (static per runtime, like StreamsIncrementally).
	// FullAccess: the runtime accepts --access full (monomind#355).
	// ToolActivityFidelity: "full" (start + matched end), "start-only" or
	// "none" for its tool_activity events (monomind#357). The rest are
	// agent-exec-full-access-any additions; an older monomind omits them.
	FullAccess           bool   `json:"full_access"`
	ToolActivityFidelity string `json:"tool_activity_fidelity,omitempty"`
	Resume               bool   `json:"resume"`
	Effort               bool   `json:"effort"`
	MaxTurns             bool   `json:"max_turns"`
	ReportsCost          bool   `json:"reports_cost"`
	// InitTarget is the `monomind init --target` value for the runtime's
	// setup files; nil when it has none.
	InitTarget *string `json:"init_target"`
	// SandboxModes are the agent exec --sandbox modes the runtime accepts
	// (agent-exec-sandbox, monomind 2.19.0); nil from an older monomind.
	// monomind refuses any other mode, so SandboxArgs checks this first.
	SandboxModes []string `json:"sandbox_modes,omitempty"`
	// SandboxModeReports says, per accepted mode, what a scoped turn
	// reports (protocol rev 26); nil from an older monomind. Approvals "on"
	// means the mode is the CLI's own allow/deny rules (copilot
	// workspace-write), not an OS sandbox.
	SandboxModeReports map[string]SandboxReport `json:"sandbox_mode_reports,omitempty"`
	// NativeSandbox says who confines the runtime's own tools: "monomind"
	// (its allow-list gate is the only tool gate, as for claude), a vendor
	// sandbox mode, or "none"; "" from a monomind that predates the field.
	NativeSandbox string `json:"native_sandbox,omitempty"`
	// AccessModes are the --access modes the runtime accepts ("scoped",
	// "read", "full"; agent-exec-access-read). CallerTools says stdio
	// caller tools reach the model, CallerToolsWithFullAccess that they
	// also do under --access full (agent-exec-full-access-tools).
	AccessModes               []string `json:"access_modes,omitempty"`
	CallerTools               bool     `json:"caller_tools,omitempty"`
	CallerToolsWithFullAccess bool     `json:"caller_tools_with_full_access,omitempty"`
}

// SandboxReport is one sandbox mode's native_sandbox and approvals.
type SandboxReport struct {
	NativeSandbox string `json:"native_sandbox"`
	Approvals     string `json:"approvals"`
}

// RuleBasedSandbox reports whether mode is enforced by the runtime's own
// approval rules rather than an OS sandbox. False when the scan did not say.
// This keys on approvals=="on", which monomind also reports for dsh (a real
// sandbox), so dsh is over-demoted: fail-closed, never served as Sandboxed.
func (e ScanEntry) RuleBasedSandbox(mode string) bool {
	return e.SandboxModeReports[mode].Approvals == "on"
}

// UnsupportedReason returns monomind's reason when it reports the runtime
// as not executable ("" and false otherwise, including from an older monomind).
func (e ScanEntry) UnsupportedReason() (string, bool) {
	if e.ExecutionSupported == nil || *e.ExecutionSupported {
		return "", false
	}
	if e.ExecutionUnsupportedReason != nil {
		return *e.ExecutionUnsupportedReason, true
	}
	return "", true
}

// ScanResult is the `agent scan --json` payload (§6).
type ScanResult struct {
	V      int         `json:"v"`
	Agents []ScanEntry `json:"agents"`
}

// Installed returns only the installed runtimes, ordered as scanned.
func (s *ScanResult) Installed() []ScanEntry {
	out := make([]ScanEntry, 0, len(s.Agents))
	for _, a := range s.Agents {
		if a.Installed {
			out = append(out, a)
		}
	}
	return out
}

// Find returns a runtime by id from the scan result (nil when absent).
func (s *ScanResult) Find(id string) *ScanEntry {
	for i := range s.Agents {
		if s.Agents[i].ID == id {
			return &s.Agents[i]
		}
	}
	return nil
}

// InstallRecipe is `agent scan`'s structured install hint (rev 9):
// kind "npm" (Packages), "script" (URL run with Shell) or "manual".
type InstallRecipe struct {
	Kind     string   `json:"kind"`
	Packages []string `json:"packages,omitempty"`
	URL      string   `json:"url,omitempty"`
	Shell    string   `json:"shell,omitempty"`
}
