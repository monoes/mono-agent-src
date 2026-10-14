// Package health is the setup & health-check engine behind
// `monoagentcli doctor` and the GUI's Settings › System health section
// (docs/plans/2026-09-24-setup-and-health-check.md).
//
// A Check inspects one component and returns a Result; a Result may name a
// Fix by ID, which `doctor --fix` / `doctor fix <id>` applies. Checks never
// change anything themselves — only fixes do.
package health

import (
	"context"
	"database/sql"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// SchemaVersion is the `doctor --json` payload version. Bump it on any
// incompatible change; the GUI checks it.
const SchemaVersion = 1

// Status is a check outcome.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip" // not applicable, or a dependency failed
	StatusInfo Status = "info" // nothing to judge, just a fact
)

// Safety says how much consent a fix needs.
type Safety string

const (
	// SafetyAuto: local, idempotent, reversible — applied without asking.
	SafetyAuto Safety = "auto"
	// SafetyConfirm: installs software, changes user config or uses the
	// network — shown and confirmed first.
	SafetyConfirm Safety = "confirm"
	// SafetyManual: needs the user; only the command/instructions are shown.
	SafetyManual Safety = "manual"
)

// Result is one check's outcome, as serialized by `doctor --json`.
type Result struct {
	ID       string   `json:"id"`
	Group    string   `json:"group"`
	Title    string   `json:"title"`
	Status   Status   `json:"status"`
	Summary  string   `json:"summary"`
	Detail   string   `json:"detail,omitempty"`
	Required bool     `json:"required"`
	Features []string `json:"features,omitempty"`
	Fix      *FixInfo `json:"fix,omitempty"`
	Source   string   `json:"source"`
	Millis   int64    `json:"ms"`
	// Parent is the ID of the check that reported this row as one of its
	// children (e.g. a runtime under runtimes.agents); empty otherwise.
	Parent string `json:"parent,omitempty"`
	// Actions are things a user may do to this row whatever its status
	// (e.g. update or remove the managed Node). Unlike Fix they are never
	// applied by `doctor --fix`; run one with `doctor fix <id>`.
	Actions []FixInfo `json:"actions,omitempty"`
	// ActionIDs is set by a check; the runner resolves it into Actions.
	ActionIDs []string `json:"-"`

	// FixID is set by a check to offer a fix; the runner resolves it into
	// Fix from the fix registry.
	FixID string `json:"-"`
	// FixCommand, when set, replaces the resolved fix's Command: for a fix
	// whose real effect depends on what the check found (which vendor
	// script a runtime install downloads and runs), so the confirmation
	// shows that rather than a generic wrapper command.
	FixCommand string `json:"-"`
	// Children are extra rows a check reports about the things it found
	// (e.g. one per agent runtime). Each needs its own ID; the runner
	// stamps Group/Source and lists them right after their parent.
	Children []Result `json:"-"`
}

// FixInfo describes a fix to a caller (CLI prompt, GUI button).
type FixInfo struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Safety  Safety `json:"safety"`
	Command string `json:"command,omitempty"`
	// Optional fixes add something the user may not want (e.g. one more
	// agent runtime). `doctor --fix` never applies them; only an explicit
	// `doctor fix <id>` or a GUI button does.
	Optional bool `json:"optional,omitempty"`
}

// Check is one health check.
type Check struct {
	ID       string
	Group    string
	Title    string
	Required bool     // failing it means the app itself is broken
	Features []string // what stops working when it isn't ok
	// DependsOn lists check IDs that must not fail/skip for this one to run.
	DependsOn []string
	// Network marks checks that call out to the network; they only run
	// with Options.Deep.
	Network bool
	// OfflineCapable marks a Network check that also runs without Deep,
	// and then keeps off the network: its Run reads Online(ctx).
	OfflineCapable bool
	// OnDemand checks only run with Options.OnDemand or when named by ID
	// (e.g. per-project monomind checks: `doctor --projects`).
	OnDemand bool
	Timeout  time.Duration // 0 = DefaultTimeout
	Run      func(ctx context.Context, env *Env) Result
}

// Fix repairs what a check found.
//
// A parameterized fix sets ApplyArg instead of Apply and is addressed as
// "<id>:<arg>" (e.g. "runtimes.install:claude"); "{arg}" in its Label and
// Command is replaced by the argument.
type Fix struct {
	FixInfo
	Apply    func(ctx context.Context, env *Env, progress func(line string)) error
	ApplyArg func(ctx context.Context, env *Env, arg string, progress func(line string)) error
}

// DefaultTimeout bounds a single check.
const DefaultTimeout = 10 * time.Second

// Env is everything checks and fixes touch, injected so they can be tested
// without a real machine.
type Env struct {
	Home       string // user home directory
	DataDir    string // ~/.monoagent
	DBPath     string
	Version    string // monoagentcli version
	Executable string // path of the running binary
	ProfileID  string // active profile ("" = resolve from the DB)

	// DB is the opened (not migrated) database, nil when it doesn't exist
	// yet or failed to open (DBErr).
	DB    *sql.DB
	DBErr error

	// Hooks — nil means "not available here", and the check that needs it
	// reports skip.
	PendingMigrations func(ctx context.Context) ([]string, error)
	QuickCheck        func(ctx context.Context) (string, error)
	Migrate           func(ctx context.Context, progress func(string)) error
	VaultState        func(ctx context.Context, profileID string) (string, error)
	ProfileRoot       func(profileID string) string
	EnsureProfile     func(profileID string) error
	LoginPath         func(ctx context.Context) (string, error)
	FreeBytes         func(path string) (uint64, error)
	LatestVersion     func(ctx context.Context) (string, error)

	// SchemaAhead reports the database's newest recorded migration, this
	// binary's newest embedded one, and whether the database is ahead.
	SchemaAhead func(ctx context.Context) (dbVersion, binaryVersion int, ahead bool, err error)

	// Node.js: the first node on PATH that isn't monoagent's managed one,
	// the active managed version, and installing a managed one.
	SystemNode  func(ctx context.Context) (path, version string, found bool)
	ManagedNode func() (version, path string, ok bool)
	InstallNode func(ctx context.Context, progress func(string)) error
	UpdateNode  func(ctx context.Context, progress func(string)) error // latest LTS, older versions removed
	RemoveNode  func(ctx context.Context, progress func(string)) error // the managed Node and its npm-global

	// monomind: locate the binary, handshake with it (callers memoize —
	// several checks ask), scan agent runtimes, install/upgrade it, and
	// run `monomind init` in a folder.
	FindMonomind func() (string, error)
	// MonomindCandidates lists every monomind installed, FindMonomind's
	// pick first (monomind.FindAll); nil when unavailable.
	MonomindCandidates  func() []string
	MonomindHandshake   func(ctx context.Context) (*monomind.VersionInfo, error)
	ScanRuntimes        func(ctx context.Context) (*monomind.ScanResult, error)
	InstallMonomind     func(ctx context.Context, progress func(string)) error
	InitMonomindProfile func(ctx context.Context, root string, progress func(string)) error
	// RepinMonomindProfile re-runs `monomind init --force` in a profile
	// folder that is already set up, so .mcp.json pins the installed
	// version. InstallMonomindDeps runs `monomind deps install`.
	RepinMonomindProfile func(ctx context.Context, root string, progress func(string)) error
	InstallMonomindDeps  func(ctx context.Context, progress func(string)) error
	// MonomindDoctor runs `monomind doctor --json` in dir (rev 9); nil
	// when unavailable. MonomindProjects lists the monomind projects to
	// check inside the active profile's folder (paths relative to it).
	MonomindDoctor   func(ctx context.Context, opts monomind.DoctorOptions) (*monomind.DoctorReport, error)
	MonomindProjects func() []string

	// InstallRuntime installs one agent runtime by its scan id.
	InstallRuntime func(ctx context.Context, id string, progress func(string)) error

	// Browser and the MonoAgent extension bridge.
	FindBrowser func() string
	// ExtensionInstalled scans browser profiles; checked is false when every
	// existing profile was unreadable (e.g. macOS Full Disk Access), in
	// which case found says nothing and callers should not treat it as "no".
	ExtensionInstalled func() (found, checked bool)
	ExtensionDir       func() string
	Bridge             func(ctx context.Context) (BridgeInfo, bool)
	// BridgeService names the user service that owns the bridge pid, or "".
	// It is separate from Bridge because resolving it runs systemctl.
	BridgeService func(ctx context.Context, pid int) string
	// RestartBridge restarts unit, refusing unless the bridge is still pid
	// and still owned by unit.
	RestartBridge func(ctx context.Context, unit string, pid int, progress func(string)) error

	// Background services.
	Daemon           func(ctx context.Context) DaemonInfo
	APIHealth        func(ctx context.Context, addr string) error
	AutostartStatus  func(ctx context.Context) (installed bool, where string)
	InstallAutostart func(ctx context.Context, progress func(string)) error
	StartDaemon      func(ctx context.Context, progress func(string)) error
	// StopDaemon gracefully stops the daemon at pid (SIGTERM, then a forced
	// kill if it has not exited in time), used to restart a daemon-owned
	// bridge that predates the running CLI.
	StopDaemon func(ctx context.Context, pid int, progress func(string)) error
	// CanStartDaemon says why StartDaemon would refuse right now (nil when it
	// would not), so that a fix that stops the daemon first can refuse before
	// it does. Nil means there is nothing to check.
	CanStartDaemon func(ctx context.Context) error

	// Agent-tool integrations (Claude Code).
	ClaudeSkills        func() (claudeFound bool, missing, stale []string)
	InstallClaudeSkills func() error
	MCPRegistration     func() (claudeFound, registered bool, where string)
	// MCPRuntimes, when set, is the registration per installed coding agent
	// (one doctor row each); MCPRegistration is the fallback without it.
	MCPRuntimes func() []MCPRuntime
	RegisterMCP func(ctx context.Context, progress func(string)) error

	// MonoesAccount reads the monoes.me session from this machine (no
	// network, nothing written).
	MonoesAccount func(ctx context.Context) AccountInfo

	// Accounts of the active profile.
	Connections       func(ctx context.Context) ([]ConnectionInfo, error)
	TestConnection    func(ctx context.Context, id string) error
	RefreshConnection func(ctx context.Context, id string) error // silent refresh_token exchange only
	LoginSessions     func(ctx context.Context) ([]SessionInfo, error)

	// TypeSafe Jev: where the active profile's key comes from (config,
	// vault or env; an error when there is none), and a models listing
	// that proves the API accepts it (network).
	JevKey    func(ctx context.Context) (source string, err error)
	JevModels func(ctx context.Context) error

	// Automations reads installed packages and their selector health
	// (local files and the database; never seeds or installs).
	Automations func(ctx context.Context) (*AutomationsInfo, error)
}

// ConnectionInfo is a saved connection, without any secret.
type ConnectionInfo struct {
	ID, Platform, Label, Method string
	HasRefreshToken             bool
}

// SessionInfo is a saved platform login session.
type SessionInfo struct {
	Platform, Username string
	Expiry             time.Time
}

// BridgeInfo is what the running extension bridge reports about itself.
type BridgeInfo struct {
	Addr      string
	Status    string // connected | waiting | unpaired
	PID       int
	Version   string
	UptimeSec int64
	// Owner says what runs it: the daemon, a service, or an `extension
	// serve` started by hand ("" when unknown).
	Owner string
	// ServiceUnit is set only for a user service verified to own this PID;
	// see serviceUnit, which resolves it through Env.BridgeService.
	ServiceUnit string
}

// DaemonInfo is the workflow daemon's heartbeat.
type DaemonInfo struct {
	Running bool
	PID     int
	APIAddr string
	AgeMS   int64
	// BridgeAddr is the extension bridge it serves; "" when it runs with
	// --bridge=false or its bridge failed to start.
	BridgeAddr string
	// Version is the version the daemon started with, from its heartbeat; ""
	// for a daemon that predates the field (v0.46.0).
	Version string
}

// Report is the `doctor --json` payload.
type Report struct {
	V                int            `json:"v"`
	GeneratedAt      time.Time      `json:"generated_at"`
	MonoagentVersion string         `json:"monoagent_version"`
	ProfileID        string         `json:"profile_id"`
	Deep             bool           `json:"deep"`
	Summary          map[Status]int `json:"summary"`
	Results          []Result       `json:"results"`
}

// RequiredFailures counts failed required checks — non-zero means the app
// can't work as installed.
func (r *Report) RequiredFailures() int {
	n := 0
	for _, res := range r.Results {
		if res.Required && res.Status == StatusFail {
			n++
		}
	}
	return n
}

// MCPRuntime is one installed coding agent's monoagent MCP registration.
type MCPRuntime struct {
	ID, Label  string
	Registered bool   // a `monoagentcli mcp` server is configured
	Current    bool   // ... and it runs with --allow-mutations
	Where      string // where it is configured (entry and file)
	Err        string // the agent's config could not be read
}
