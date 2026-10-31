package chat

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/monomind"
)

// coderConfigName is the configs-table row holding coder mode's settings.
const coderConfigName = "coder_mode"

// coderSettings is coder mode's persisted configuration. Enabled gates every
// coder conversation and turn: the CLI enforces it, not the app.
type coderSettings struct {
	Enabled       bool    `json:"enabled"`
	WorkspaceRoot string  `json:"workspaceRoot"`
	MaxTurns      int     `json:"maxTurns"`
	Timeout       string  `json:"timeout"`
	BudgetUSD     float64 `json:"budgetUsd"`
	// Dynamic org (#226): per-turn worker limits, the workers' budget
	// (0 = none), and who picks a worker's model when the lead doesn't
	// ("lead-then-jev" or "lead").
	OrgMaxAgents int `json:"orgMaxAgents"`
	// orgMaxAgentsDefaulted: OrgMaxAgents was unset (the operator never set it), so a
	// mission turn may use a larger default (missionMaxAgents).
	orgMaxAgentsDefaulted bool
	// orgWritersDefaulted: OrgWriters was unset (the operator never set it), so a
	// mission turn in a git work tree may default to isolated writers (missionWriters).
	// It is never saved as an operator's choice (saveCoderSettings).
	orgWritersDefaulted bool
	OrgMaxConcurrent    int     `json:"orgMaxConcurrent"`
	OrgBudgetUSD        float64 `json:"orgBudgetUsd"`
	OrgModelPicker      string  `json:"orgModelPicker"`
	// OrgRouter (coder.router) is who picks a worker's role and skills when
	// the lead names none: "auto" (default), "jev" or "model".
	OrgRouter string `json:"orgRouter"`
	// OrgWriters is "isolated" to give each writing worker its own git
	// worktree and branch, merged by the lead (#230), or "shared" (the
	// default): writers take turns under the write lease.
	OrgWriters string `json:"orgWriters"`
	// SendApproval (default on): an agent's outbound sends wait for the
	// operator's approval (internal/hilsend). Only the operator changes it.
	SendApproval *bool `json:"sendApproval,omitempty"`
	// AppAccess (coder.appAccess, default on) decides what agents are told
	// and offered, not what they can do: on, coder prompts carry the
	// profile summary and a dynamic org's workers may get the workspace
	// access profile, whose prompt names every MonoAgent surface. Off,
	// neither (internal/dynorg); the coder lead keeps full access, a worker
	// with a shell can still run monoagentcli itself, operator-only commands
	// and the send gate apply either way, and routing still asks Jev when a
	// Jev key is set. Only the operator changes it.
	AppAccess *bool `json:"appAccess,omitempty"`
	// DefaultAccess is the level an operator-run `chat history create`
	// without --access/--mode makes when coder mode is ready: coding,
	// workspace, full or scoped. Unset it is full (see defaultAccess), what a
	// coder chat always was. Only the operator changes it, and an agent-run
	// creation never takes it (chat_default_access.go).
	DefaultAccess string `json:"defaultAccess,omitempty"`
	// Box (coder.box) is off (default), compat or strict: whether a coder lead turn of a
	// coding-access chat runs inside the agent sandbox (internal/agentbox). Nothing turns it
	// on; only the operator changes it.
	Box string `json:"box,omitempty"`
	// PeercredBinding (security.peercred_binding) asks that a turn's tokens be bound to the turn's process by the
	// kernel-checked binding service (internal/procbind). Off by default. Only the operator changes it, and it can be
	// switched on only where procbind.Refusal says nothing: while the build cannot issue bound tokens, switching it on
	// is refused and, if the setting is on anyway (a hand edit), every turn start is refused (coder_host_gate.go).
	PeercredBinding bindingFlag `json:"peercredBinding,omitempty"`
}

const (
	defaultCoderMaxTurns = 200
	defaultCoderTimeout  = "60m"
)

// withDefaults fills unset fields.
func (s coderSettings) withDefaults() coderSettings {
	if s.WorkspaceRoot == "" {
		if home, err := os.UserHomeDir(); err == nil {
			s.WorkspaceRoot = filepath.Join(home, "monoagent-coder")
		}
	}
	if s.MaxTurns <= 0 {
		s.MaxTurns = defaultCoderMaxTurns
	}
	if s.Timeout == "" {
		s.Timeout = defaultCoderTimeout
	}
	d := dynorg.DefaultLimits()
	if s.OrgMaxAgents <= 0 {
		s.OrgMaxAgents = d.MaxAgents
		s.orgMaxAgentsDefaulted = true
	}
	if s.OrgMaxConcurrent <= 0 {
		s.OrgMaxConcurrent = d.MaxConcurrent
	}
	if s.OrgModelPicker != dynorg.PickerLead {
		s.OrgModelPicker = dynorg.PickerLeadThenJev
	}
	s.OrgRouter = dynorg.NormalizeRouter(s.OrgRouter)
	if s.OrgWriters == "" {
		s.orgWritersDefaulted = true
	}
	if s.OrgWriters != dynorg.WritersIsolated {
		s.OrgWriters = dynorg.WritersShared
	}
	if s.SendApproval == nil {
		on := true
		s.SendApproval = &on
	}
	if s.AppAccess == nil {
		on := true
		s.AppAccess = &on
	}
	return s
}

// Where status says the default access came from.
const (
	defaultAccessFromDefault = "default"
	defaultAccessFromSet     = "set"
)

// defaultAccess is the level new operator-run chats take when coder mode is
// ready: the stored one (`coder set --default-access`), else full. It is not
// filled in by withDefaults, so saving the settings never stores the built-in
// default as if the operator had chosen it.
func (s coderSettings) defaultAccess() string {
	if ai.ValidAccess(s.DefaultAccess) {
		return s.DefaultAccess
	}
	return ai.AccessFull
}

// appAccess reports coder.appAccess; unset is on.
func (s coderSettings) appAccess() bool { return s.AppAccess == nil || *s.AppAccess }

// bindingFlag is the peer-credential setting as stored. It is read conservatively, in ONE way everywhere (status, the
// setter and the launcher gate all decode this type): anything but a plain false or null counts as on, and a key
// that occurs more than once (or in other letter case, which encoding/json folds) is on if ANY occurrence is on or
// unreadable, because the decoder calls this for every occurrence and a later "false" never clears an earlier "on".
// Reading a junk value never fails, so `coder set --peercred-binding off` can always overwrite it: the stored JSON is
// written again from the struct, which has the key once.
type bindingFlag bool

func (f *bindingFlag) UnmarshalJSON(b []byte) error {
	if s := string(bytes.TrimSpace(b)); s != "false" && s != "null" {
		*f = true
	}
	return nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// settingsRW is what the settings need of a database, a connection or a transaction.
type settingsRW interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func loadCoderSettings(db *sql.DB) (coderSettings, error) {
	return loadCoderSettingsFrom(context.Background(), db)
}

func loadCoderSettingsFrom(ctx context.Context, db settingsRW) (coderSettings, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT config_data FROM configs WHERE name = ?`, coderConfigName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return coderSettings{}.withDefaults(), nil
	}
	if err != nil {
		return coderSettings{}, fmt.Errorf("reading coder settings: %w", err)
	}
	var s coderSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return coderSettings{}, fmt.Errorf("reading coder settings: %w", err)
	}
	return s.withDefaults(), nil
}

func saveCoderSettings(db *sql.DB, s coderSettings) error {
	return saveCoderSettingsTo(context.Background(), db, s)
}

func saveCoderSettingsTo(ctx context.Context, db settingsRW, s coderSettings) error {
	if s.orgWritersDefaulted { // the default is not the operator's choice: keep it unset
		s.OrgWriters = ""
	}
	if s.orgMaxAgentsDefaulted { // likewise: a stored default would pin missions at 6 workers
		s.OrgMaxAgents = 0
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO configs (name, config_data, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = excluded.updated_at`,
		coderConfigName, string(b))
	if err != nil {
		return fmt.Errorf("saving coder settings: %w", err)
	}
	return nil
}

// updateCoderSettings reads the settings, lets change alter them and writes them back inside ONE write transaction
// (BEGIN IMMEDIATE), so two writers cannot both read the old struct and the second one's save undo the first one's
// (a concurrent `coder set --box` could otherwise erase an operator's peer-credential setting, and the reverse). It
// returns the settings as saved; an error from change leaves the stored settings as they were.
func updateCoderSettings(ctx context.Context, db *sql.DB, change func(s *coderSettings) error) (coderSettings, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return coderSettings{}, err
	}
	defer conn.Close()
	for attempt := 0; ; attempt++ { // only the BEGIN is retried when the database is busy
		if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err == nil || attempt >= 4 || !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "BUSY") {
			break
		}
		time.Sleep(100 * time.Millisecond << attempt)
	}
	if err != nil {
		return coderSettings{}, fmt.Errorf("saving coder settings: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	s, err := loadCoderSettingsFrom(ctx, conn)
	if err != nil {
		return coderSettings{}, err
	}
	if err := change(&s); err != nil {
		return coderSettings{}, err
	}
	if err := saveCoderSettingsTo(ctx, conn, s); err != nil {
		return coderSettings{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return coderSettings{}, fmt.Errorf("saving coder settings: %w", err)
	}
	committed = true
	return s, nil
}

// coderStatus is `coder status --json`. Ready and MissingCapabilities are
// monomind's global coder support; Runtimes is each runtime's, and Runtime
// stays "claude" for app versions that only know that one.
type coderStatus struct {
	coderSettings
	Ready               bool                    `json:"ready"`
	MissingCapabilities []string                `json:"missingCapabilities"`
	MonomindVersion     string                  `json:"monomindVersion"`
	Runtime             string                  `json:"runtime"`
	Runtimes            []monomind.CoderRuntime `json:"runtimes"`
	// DefaultAccessSource says whether defaultAccess is the built-in default
	// or one the operator set.
	DefaultAccessSource string `json:"defaultAccessSource"`

	caps *monomind.CapabilitySet
	scan *monomind.ScanResult // nil when the scan failed
}

// capabilityProbe is monomind's capability handshake; a var so tests can
// stand in for a monomind that has (or lacks) the coder capabilities.
var capabilityProbe = func(cmd *cobra.Command) (*monomind.CapabilitySet, error) {
	return monomind.Capabilities(cmd.Context())
}

// runtimeScan is `monomind agent scan`; a var so tests can stand in for
// the installed runtimes.
var runtimeScan = monomind.Scan

func currentCoderStatus(cmd *cobra.Command, s coderSettings) coderStatus {
	st := coderStatus{coderSettings: s, Runtime: monomind.DefaultCoderRuntime, DefaultAccessSource: defaultAccessFromDefault}
	if ai.ValidAccess(s.DefaultAccess) {
		st.DefaultAccessSource = defaultAccessFromSet
	}
	st.DefaultAccess = s.defaultAccess()
	st.Box = s.boxMode()
	set, err := capabilityProbe(cmd)
	if err == nil && set != nil {
		st.MonomindVersion = set.Version
		st.caps = set
	}
	st.MissingCapabilities = monomind.MissingCoderCapabilities(set)
	if st.MissingCapabilities == nil {
		st.MissingCapabilities = []string{}
	}
	st.Ready = len(st.MissingCapabilities) == 0
	var scan *monomind.ScanResult
	if err == nil {
		// A failed scan leaves claude alone, as before per-runtime support.
		scan, _ = runtimeScan(cmd.Context())
	}
	st.scan = scan
	st.Runtimes = monomind.CoderRuntimes(scan, st.caps)
	return st
}

// coderError is a coder-mode refusal with a machine-readable code for --json.
type coderError struct {
	code string
	err  *clicore.Error
}

func (e *coderError) Error() string                   { return e.err.Error() }
func (e *coderError) Unwrap() error                   { return e.err }
func (e *coderError) JSONErrorFields() map[string]any { return map[string]any{"code": e.code} }

func errCoderDisabled() error {
	return &coderError{code: "coder_disabled", err: &clicore.Error{Code: 3,
		Msg: "coder mode is disabled — enable it in Settings or with `monoagentcli coder enable --yes-i-understand`"}}
}

func errNeedsMonomindUpdate(missing []string) error {
	return &coderError{code: "needs_monomind_update", err: &clicore.Error{Code: 1,
		Msg: fmt.Sprintf("coder mode needs a newer monomind (missing capabilities: %v) — update monomind: `npm install -g @monoes/monomindcli@latest`", missing)}}
}

// errCoderRuntime refuses a runtime monomind won't run with full access:
// unknown to it, left out by an older monomind (claude only), or not a
// coding agent at all.
func errCoderRuntime(runtime string, known, olderMonomind bool) error {
	msg := fmt.Sprintf("coder mode can't run on %s: monomind doesn't run it with full access (see `monoagentcli coder status`)", runtime)
	switch {
	case !known:
		msg = fmt.Sprintf("coder mode can't run on %q: monomind doesn't know that runtime (see `monoagentcli coder status`)", runtime)
	case olderMonomind:
		msg = fmt.Sprintf("coder mode runs on %s only with a newer monomind (the installed one runs claude only) — update monomind: `npm install -g @monoes/monomindcli@latest`", runtime)
	}
	return &coderError{code: "coder_runtime_unsupported", err: &clicore.Error{Code: 3, Msg: msg}}
}

// requireCoderReady refuses unless coder mode is enabled, monomind supports
// it and runs runtime ("" = claude) with full access, and this process is
// not root. An uninstalled runtime is not refused here: the turn itself
// reports it as not set up, which the app knows how to offer to fix.
func requireCoderReady(cmd *cobra.Command, db *sql.DB, runtime string) (coderSettings, coderStatus, monomind.CoderRuntime, error) {
	s, err := loadCoderSettings(db)
	if err != nil {
		return s, coderStatus{}, monomind.CoderRuntime{}, err
	}
	if !s.Enabled {
		return s, coderStatus{}, monomind.CoderRuntime{}, errCoderDisabled()
	}
	if monomind.IsRoot() {
		return s, coderStatus{}, monomind.CoderRuntime{}, clicore.InvalidInput("coder mode refuses to run as root")
	}
	st := currentCoderStatus(cmd, s)
	if !st.Ready {
		return s, st, monomind.CoderRuntime{}, errNeedsMonomindUpdate(st.MissingCapabilities)
	}
	if runtime == "" {
		runtime = monomind.DefaultCoderRuntime
	}
	r := monomind.FindCoderRuntime(st.Runtimes, runtime)
	if r == nil || !r.FullAccess {
		return s, st, monomind.CoderRuntime{}, errCoderRuntime(runtime, r != nil, !st.caps.Has(monomind.CapAgentExecFullAccessAny))
	}
	return s, st, *r, nil
}

func newCoderCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "coder",
		Short: "Coder mode: chats where the agent has full access to this computer",
		Long: "Coder mode runs a chat as a full coding-agent session (Claude Code, Codex, OpenCode, … — " +
			"see `coder status` for which runtimes are ready) inside a folder: it can run any command and " +
			"read or change any file your user can, with no approval prompts, and it loads your normal " +
			"setup for that agent (its instructions files, skills, hooks, MCP servers). It is off until " +
			"enabled here. Web pages and files in the folder can carry instructions that steer it, so " +
			"only point it at folders you trust. A workflow's processes and the chat's runs can't start a " +
			"coder chat: start it from the app or your own terminal, or have the workflow queue one with " +
			"`coder queue add` for the app's Start.",
	}
	cmd.AddCommand(newCoderStatusCmd(cfg), newCoderEnableCmd(cfg), newCoderDisableCmd(cfg), newCoderSetCmd(cfg), newCoderWorkspaceCmd(cfg), newCoderStopBackgroundCmd(cfg),
		newCoderQueueCmd(cfg), newCoderProjectsCmd(cfg), newCoderListCmd(cfg), newCoderHostCmd(cfg), newCoderSessionsCmd(cfg))
	return cmd
}

// coderSettingsCmd builds a subcommand that optionally changes the settings
// and then prints the status.
func coderSettingsCmd(cfg *Deps, use, short string, change func(cmd *cobra.Command, s *coderSettings) error) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := cfg.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			var s coderSettings
			if change != nil {
				ctx := cmd.Context()
				if ctx == nil {
					ctx = context.Background()
				}
				s, err = updateCoderSettings(ctx, db.DB, func(s *coderSettings) error { return change(cmd, s) })
			} else {
				s, err = loadCoderSettings(db.DB)
			}
			if err != nil {
				return err
			}
			st := currentCoderStatus(cmd, s)
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enabled: %v\nworkspace root: %s\nmax turns: %d\ntimeout: %s\nbudget: $%g\nready: %v\n",
				st.Enabled, st.WorkspaceRoot, st.MaxTurns, st.Timeout, st.BudgetUSD, st.Ready)
			fmt.Fprintf(cmd.OutOrStdout(), "app access (profile summary in prompts, org workers' MonoAgent surfaces): %s\n", onOff(st.appAccess()))
			fmt.Fprintf(cmd.OutOrStdout(), "default access for new chats: %s\n", st.DefaultAccess)
			fmt.Fprintf(cmd.OutOrStdout(), "peer-credential binding of turn tokens: %s (off by default; it cannot be switched on in this build yet)\n", onOff(bool(st.PeercredBinding)))
			fmt.Fprintf(cmd.OutOrStdout(), "sandbox (box) for coding-access turns: %s (off by default; best-effort hardening, not isolation: the host network is shared and the host root is read-only)\n", st.Box)
			if !st.Ready {
				fmt.Fprintf(cmd.OutOrStdout(), "missing monomind capabilities: %v\n", st.MissingCapabilities)
			}
			for _, r := range st.Runtimes {
				if r.FullAccess {
					fmt.Fprintf(cmd.OutOrStdout(), "runtime %s: installed %v, ready %v, tool activity %s\n", r.ID, r.Installed, r.Ready, r.ToolActivity)
				}
			}
			return nil
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, c)
	return c
}

func newCoderStatusCmd(cfg *Deps) *cobra.Command {
	return coderSettingsCmd(cfg, "status", "Show coder mode's settings and whether monomind supports it", nil)
}

func newCoderEnableCmd(cfg *Deps) *cobra.Command {
	var yes bool
	c := coderSettingsCmd(cfg, "enable", "Enable coder mode (full-access chats; operator only)", func(_ *cobra.Command, s *coderSettings) error {
		if err := clicore.RequireOperator(cfg.RequireOperator, "enable coder mode, which runs chats with full access to this computer"); err != nil {
			return err
		}
		if !yes {
			return clicore.InvalidInput("coder mode gives the agent full access to this computer; pass --yes-i-understand to enable it")
		}
		s.Enabled = true
		return nil
	})
	c.Flags().BoolVar(&yes, "yes-i-understand", false, "Confirm that coder chats can run any command and change any file without asking")
	return c
}

func newCoderDisableCmd(cfg *Deps) *cobra.Command {
	return coderSettingsCmd(cfg, "disable", "Disable coder mode", func(_ *cobra.Command, s *coderSettings) error {
		s.Enabled = false
		return nil
	})
}

// coderTimeout parses the stored timeout, falling back to the default.
func (s coderSettings) timeout() time.Duration {
	if d, err := parseDurationFlag(s.Timeout); err == nil && d > 0 {
		return d
	}
	d, _ := time.ParseDuration(defaultCoderTimeout)
	return d
}

// expandHome expands a leading "~/".
func expandHome(p string) string {
	if p == "~" || len(p) > 1 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
