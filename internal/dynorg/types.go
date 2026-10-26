// Package dynorg is the dynamic org of a coder chat (monoes/mono-agent#226):
// the chat's lead agent spawns workers through a few caller tools, and a
// conductor inside the `monoagentcli chat` turn process staffs each worker
// (role and skills from monomind pick, model and effort from Jev or the
// lead), runs it as its own `monomind agent exec`, and journals everything
// as agent.* events so the CLI and the GUI can show the org.
package dynorg

import (
	"context"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Access profiles the lead gives workers. The ceiling is the coder chat's
// own full access: no profile goes past it.
const (
	ProfileCoding     = "coding"     // full access: shell, edits, the runtime's web tools
	ProfileQA         = "qa"         // coding, plus the browser through monoagentcli
	ProfileAutomation = "automation" // coding, plus mono-agent workflows and automations through monoagentcli
	ProfileResearch   = "research"   // read files and the web, no edits (--access read when the runtime has it)
	// ProfileWorkspace is coding plus every monoagent surface (apps, tasks,
	// documents, people, vault, workflows) through monoagentcli; outbound
	// sends wait for approval. A superset of coding, qa and automation.
	ProfileWorkspace = "workspace"
)

// Profiles lists the access profiles in the order the lead sees them.
var Profiles = []string{ProfileWorkspace, ProfileCoding, ProfileQA, ProfileAutomation, ProfileResearch}

// writes reports whether a profile edits the chat folder (and so needs the
// write lease).
func writes(profile string) bool { return profile != ProfileResearch }

// usesBrowser reports whether a profile drives the browser (and so needs
// the browser lease).
func usesBrowser(profile string) bool {
	return profile == ProfileQA || profile == ProfileAutomation || profile == ProfileWorkspace
}

// Model pickers: who chooses a worker's model. The lead's own choice always
// wins (and is checked); these decide what happens when it names none.
const (
	PickerLead        = "lead"          // the lead must name one
	PickerLeadThenJev = "lead-then-jev" // Jev chooses, else the fallback rules
)

// Limits bound one turn's org.
type Limits struct {
	MaxAgents     int     // workers spawned per turn
	MaxConcurrent int     // workers running at once
	BudgetUSD     float64 // total worker cost per turn, estimates included (estimate.go); 0 = no budget, the default
}

// DefaultLimits are the coder settings' defaults.
func DefaultLimits() Limits { return Limits{MaxAgents: 6, MaxConcurrent: 3} }

// Model is one runtime model the conductor may staff: a ready entry of the
// validated roster (#225), or the lead's own model when there is no roster.
type Model struct {
	Runtime    string   `json:"runtime"`
	Model      string   `json:"model"` // "" = the runtime's default
	Label      string   `json:"label,omitempty"`
	Efforts    []string `json:"effort_levels,omitempty"`
	FullAccess bool     `json:"full_access"`
	Read       bool     `json:"read_access"`
	// ReadOnlySandbox: the runtime can run sandboxed read-only (agent exec
	// --sandbox read-only), the fallback confinement for a research worker
	// when --access read isn't available.
	ReadOnlySandbox bool `json:"read_only_sandbox,omitempty"`
	// CallerTools / CallerToolsFull: the runtime gives stdio caller tools
	// to the model, and also under --access full. A worker gets ask_user
	// (#256) only when its exec can take it.
	CallerTools     bool `json:"caller_tools,omitempty"`
	CallerToolsFull bool `json:"caller_tools_full,omitempty"`
	// Fidelity is the runtime's tool-activity fidelity from the scan
	// ("full", "start-only" or "none"); "" when unknown.
	Fidelity string `json:"tool_activity_fidelity,omitempty"`
	Resume   bool   `json:"resume"`
	// ReportsCost: the runtime reports its cost (the scan's reports_cost).
	// Other runtimes' workers are costed from their tokens (estimate.go).
	// reports_cost is in the scan from monomind 2.19, which the dynamic org
	// already requires (orgUnavailableReason): an older scan would leave it
	// false and have claude's workers estimated too.
	ReportsCost bool    `json:"reports_cost,omitempty"`
	CostUSD     float64 `json:"test_cost_usd,omitempty"` // cost of its one-word validation turn: a relative price signal
	LatencyMs   int64   `json:"latency_ms,omitempty"`
	Stale       bool    `json:"stale,omitempty"`
}

// Key names a model as "runtime/model".
func (m Model) Key() string {
	if m.Model == "" {
		return m.Runtime + "/default"
	}
	return m.Runtime + "/" + m.Model
}

// fits reports whether the model can run a worker with this profile.
func (m Model) fits(profile string) bool {
	if profile == ProfileResearch {
		return m.Read || m.FullAccess
	}
	return m.FullAccess
}

// SpawnRequest is org_spawn's input. Everything but Brief is optional:
// what the lead leaves out, staffing fills in.
type SpawnRequest struct {
	Brief      string   `json:"brief"`
	Role       string   `json:"role,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	Runtime    string   `json:"runtime,omitempty"`
	Model      string   `json:"model,omitempty"`
	Effort     string   `json:"effort,omitempty"`
	Access     string   `json:"access,omitempty"`
	Files      []string `json:"files,omitempty"`
	NeedsWrite *bool    `json:"needs_write,omitempty"`
	Wait       bool     `json:"wait,omitempty"`
	// AllowSpawn gives the worker org_spawn, org_wait and org_message for
	// sub-workers of its own (#230); only the lead may set it.
	AllowSpawn bool `json:"allow_spawn,omitempty"`
	// TaskID makes the worker a mission worker: it takes that task of a task
	// session, its brief is built from the task, and it is bound to the task
	// (mission_exec.go). Brief then only adds to the generated one.
	TaskID int64 `json:"task_id,omitempty"`
	// executor is who carries the task out (lead, worker, subagent): set by the
	// scheduler, never by the lead's JSON.
	executor string
	// verify makes the worker the verifier of TaskID (a task in Review): bound to verify
	// that task and nothing else; set by mission_verify, never by the lead's JSON.
	verify bool
}

// Skill is one skill given to a worker: its name and the text put in its
// prompt ("" when only the name is known).
type Skill struct {
	Name string
	Text string
}

// Staff is how a worker was staffed.
type Staff struct {
	Role      string  // the title shown on the stage ("Code Reviewer")
	AgentType string  // monomind agent id, or a built-in role id
	Category  string  // the agent's category (engineering, testing, …)
	AgentBody string  // the agent definition's instructions
	Skills    []Skill // skills given to the worker
	Model     Model   // the runtime and model it runs on
	Effort    string
	Access    string   // an access profile
	Fallbacks []Model  // next models to try if Model can't run
	Why       []string // who chose what, for agent.spawned.why
	PickConf  *float64
	JevConf   *float64
}

// Emitter journals one chat event of the lead's turn. turnJournal
// implements it; calls come from worker goroutines.
type Emitter interface {
	Emit(typ chatevents.EventType, payload any)
}

// ExecFunc runs one agent exec turn; monomind.Exec in production.
type ExecFunc func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)

// Outcome records what a real worker turn found out about its model, for
// the roster (agentroster.RecordOutcome): status is an agentroster status.
type Outcome func(runtime, model, status, detail string, at time.Time)
