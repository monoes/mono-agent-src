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
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/monomind"
)

// coderConfigName is the configs-table row holding coder mode's settings.
const coderConfigName = "coder_mode"

// coderSettings is coder mode's persisted configuration. Enabled gates every
// coder conversation and turn: the CLI enforces it, not the app.
type coderSettings struct {
	// tx is the write transaction the settings are being changed in (set only while updateCoderSettings runs change); a
	// setter that must check other state atomically with the save reads through it. Never stored.
	tx *sql.Conn

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

// readCoderRow is the one read of the coder settings row; found is false when nothing is stored. Every reader goes through it,
// so the configs table has the one read and the one write of this file (writeguard_test.go counts the statements).
func readCoderRow(ctx context.Context, db settingsRW) (raw string, found bool, err error) {
	err = db.QueryRowContext(ctx, `SELECT config_data FROM configs WHERE name = ?`, coderConfigName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading coder settings: %w", err)
	}
	return raw, true, nil
}

func loadCoderSettingsFrom(ctx context.Context, db settingsRW) (coderSettings, error) {
	raw, found, err := readCoderRow(ctx, db)
	if err != nil {
		return coderSettings{}, err
	}
	if !found {
		return coderSettings{}.withDefaults(), nil
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
	// The row is written whole, so a field a newer version stored and this one does not know would be lost by any save
	// (an older CLI that changes one setting would silently reset what a newer version set). Keep those; the fields this
	// version knows are written from the struct alone, so turning one off still removes it.
	extra, err := storedUnknownCoderKeys(ctx, db)
	if err != nil {
		return err
	}
	if b, err = withExtraKeys(b, extra); err != nil {
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
		dbconn.AbandonBegin(conn)
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
	s.tx = conn
	err = change(&s)
	s.tx = nil
	if err != nil {
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
	// UnknownSettings are the stored settings fields this version does not know (a newer version's, or a hand edit).
	UnknownSettings     []string `json:"unknownSettings,omitempty"`
	Ready               bool     `json:"ready"`
	MissingCapabilities []string `json:"missingCapabilities"`
	MonomindVersion     string   `json:"monomindVersion"`
	// ProbeError is why monomind could not be run (not found, a shim that
	// does not resolve, a crash); empty when it answered. While it is set,
	// MissingCapabilities stays empty: nothing is known to be missing.
	ProbeError string                  `json:"probeError"`
	Runtime    string                  `json:"runtime"`
	Runtimes   []monomind.CoderRuntime `json:"runtimes"`
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
	st.MissingCapabilities = []string{}
	if err != nil || set == nil {
		st.ProbeError = probeErrorText(err)
	} else {
		st.MissingCapabilities = monomind.MissingCoderCapabilities(set)
		if st.MissingCapabilities == nil {
			st.MissingCapabilities = []string{}
		}
	}
	st.Ready = st.ProbeError == "" && len(st.MissingCapabilities) == 0
	var scan *monomind.ScanResult
	if err == nil {
		// A failed scan leaves claude alone, as before per-runtime support.
		scan, _ = runtimeScan(cmd.Context())
	}
	st.scan = scan
	st.Runtimes = monomind.CoderRuntimes(scan, st.caps)
	return st
}

// maxProbeErrorLen bounds the probe failure shown to people.
const maxProbeErrorLen = 300

// probeErrorText is the capability probe's failure as one short line for
// the status; a nil error with no capability set (nothing to read) says so.
func probeErrorText(err error) string {
	msg := "monomind gave no capability list"
	if err != nil {
		msg = err.Error()
	}
	return shortLine(msg, maxProbeErrorLen)
}

// terminalSequence matches ANSI escape sequences (CSI, and OSC up to BEL or
// ST), whose parameters would otherwise stay behind as visible text.
var terminalSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)

// shortLine makes text from monomind safe to show: control characters
// (escape sequences, BEL) and invisible format characters (bidi overrides,
// zero-width) are dropped, whitespace collapsed, and the result cut to max
// runes.
func shortLine(text string, max int) string {
	text = terminalSequence.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > max {
		text = string(r[:max]) + "…"
	}
	return text
}

// maxVersionLen bounds a monomind version shown in a message.
const maxVersionLen = 40

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

func errNeedsMonomindUpdate(missing []string, found string) error {
	if found = shortLine(found, maxVersionLen); found == "" {
		found = "unknown version"
	}
	return &coderError{code: "needs_monomind_update", err: &clicore.Error{Code: 1,
		Msg: fmt.Sprintf("coder mode needs a newer monomind (found %s, missing capabilities: %v) — update monomind: `npm install -g %s`", found, missing, health.MonomindPackage)}}
}

// errMonomindUnusable refuses because monomind could not be run at all,
// which an update does not necessarily fix.
func errMonomindUnusable(probeError string) error {
	return &coderError{code: "monomind_unusable", err: &clicore.Error{Code: 1,
		Msg: fmt.Sprintf("monomind couldn't be used: %s — repair it with `monoagentcli doctor --fix --yes` (or `npm install -g %s`)", probeError, health.MonomindPackage)}}
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
	if st.ProbeError != "" {
		return s, st, monomind.CoderRuntime{}, errMonomindUnusable(st.ProbeError)
	}
	if !st.Ready {
		return s, st, monomind.CoderRuntime{}, errNeedsMonomindUpdate(st.MissingCapabilities, st.MonomindVersion)
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
			"coder chat: start it from the app or your own terminal, or ask the operator to queue one for " +
			"the app's Start by running `coder queue add` in their own terminal (queueing is the operator's too).",
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
			kctx := cmd.Context()
			if kctx == nil {
				kctx = context.Background()
			}
			st.UnknownSettings = unknownCoderKeyNames(kctx, db.DB)
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enabled: %v\nworkspace root: %s\nmax turns: %d\ntimeout: %s\nbudget: $%g\nready: %v\n",
				st.Enabled, st.WorkspaceRoot, st.MaxTurns, st.Timeout, st.BudgetUSD, st.Ready)
			fmt.Fprintf(cmd.OutOrStdout(), "app access (profile summary in prompts, org workers' MonoAgent surfaces): %s\n", onOff(st.appAccess()))
			fmt.Fprintf(cmd.OutOrStdout(), "default access for new chats: %s\n", st.DefaultAccess)
			fmt.Fprintf(cmd.OutOrStdout(), "peer-credential binding of turn tokens: %s (off by default; switching it on is the operator's, on a machine that can run the binding service)\n", onOff(bool(st.PeercredBinding)))
			if len(st.UnknownSettings) > 0 {
				shown := st.UnknownSettings
				more := ""
				if len(shown) > 5 {
					shown, more = shown[:5], fmt.Sprintf(" and %d more", len(shown)-5)
				}
				quoted := make([]string, len(shown))
				for i, k := range shown {
					if len(k) > 40 {
						k = k[:40] + "…"
					}
					quoted[i] = fmt.Sprintf("%q", k)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "warning: the stored settings hold %s%s, which this version does not know: saved by another version of monoagentcli, or by hand. They are kept up to a limit but not acted on. Update this monoagentcli (`monoagentcli update`) so it and the daemon run the same version.\n", strings.Join(quoted, ", "), more)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sandbox (box) for coding-access turns: %s (off by default; best-effort hardening, not isolation: the host network is shared and the host root is read-only)\n", st.Box)
			if st.ProbeError != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "monomind couldn't be used: %s\n", st.ProbeError)
			} else if !st.Ready {
				fmt.Fprintf(cmd.OutOrStdout(), "missing monomind capabilities: %v (found monomind %s)\n", st.MissingCapabilities, st.MonomindVersion)
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

// coderSettingKeys are the JSON keys this version of coderSettings reads and writes.
var coderSettingKeys = func() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(coderSettings{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.PkgPath != "" || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys[name] = true
	}
	return keys
}()

// How many stored fields this version does not know it keeps, and their total size: a bound, so a stored blob cannot grow
// the row on every save. Past it the keys last in key order are not carried.
const (
	maxKeptCoderKeys  = 64
	maxKeptCoderBytes = 64 << 10
)

// retiredCoderKeys are fields an earlier version wrote and none reads any more: they are not carried. To retire a field,
// remove it from coderSettings and add its key here.
var retiredCoderKeys = map[string]bool{}

// storedUnknownCoderKeys returns the stored settings' fields this version does not know, as they were stored. Nothing
// stored has none. Something stored that is not a JSON object is an error: it is not overwritten blindly.
func storedUnknownCoderKeys(ctx context.Context, db settingsRW) (map[string]json.RawMessage, error) {
	raw, found, err := readCoderRow(ctx, db)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	var all map[string]json.RawMessage
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &all) != nil {
		// Not a settings object: a write now would replace whatever it is without a trace.
		return nil, errors.New("the stored coder settings are not a JSON object; refusing to overwrite them (fix or remove the coder_mode config row)")
	}
	names := make([]string, 0, len(all))
	for k := range all {
		if !knownCoderKey(k) && !retiredCoderKeys[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	extra := map[string]json.RawMessage{}
	size := 0
	for _, k := range names {
		if len(extra) >= maxKeptCoderKeys || size+len(k)+len(all[k]) > maxKeptCoderBytes {
			break
		}
		extra[k] = all[k]
		size += len(k) + len(all[k])
	}
	return extra, nil
}

// knownCoderKey says whether k names a field of coderSettings. encoding/json matches a field whatever the case of its key,
// so a key that differs from a known one only in case IS that field: it is not carried, because kept beside the struct's
// own key it would win on the next read.
func knownCoderKey(k string) bool {
	for known := range coderSettingKeys {
		if strings.EqualFold(k, known) {
			return true
		}
	}
	return false
}

// unknownCoderKeyNames is every stored key this version does not know and has not retired, in key order, for `coder status`.
func unknownCoderKeyNames(ctx context.Context, db settingsRW) []string {
	raw, found, err := readCoderRow(ctx, db)
	if err != nil || !found {
		return nil
	}
	var all map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &all) != nil {
		return nil
	}
	var names []string
	for k := range all {
		if !knownCoderKey(k) && !retiredCoderKeys[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

// withExtraKeys adds the extra fields to the JSON object b, after its own, in key order.
func withExtraKeys(b []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return b, nil
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(b) < 2 || b[len(b)-1] != '}' {
		return nil, errors.New("coder settings did not marshal to an object")
	}
	out := append([]byte(nil), b[:len(b)-1]...)
	for _, k := range keys {
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, kb...)
		out = append(out, ':')
		out = append(out, extra[k]...)
	}
	return append(out, '}'), nil
}
