//go:build doomsday

package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// `doomsday governor status [--json]`: the resource governor's read model for the control room and the person. It is
// read-only by construction (it opens nothing for writing and carries no argument that names a run, a limit or a queue
// row), it sits behind the sign-in like the other read verbs, and it is NOT open like Stop. Only a build with the mode has it.

// GovernorSchema versions the JSON of the status verb.
const GovernorSchema = 1

// GovernorStaleAfter is how old the host's last report may be before the view says the host is not reporting.
const GovernorStaleAfter = time.Minute

// GovernorStatusFunc reads the view; nil is the layer being off.
type GovernorStatusFunc func(ctx context.Context, generation int64) (GovernorView, error)

// GovernorView is what the verb prints: numbers and reason names only. No member id, payload, conversation or provider key
// secret appears in it.
type GovernorView struct {
	SchemaVersion int    `json:"schema_version"`
	Off           bool   `json:"off,omitempty"`
	Generation    int64  `json:"generation,omitempty"`
	RunState      string `json:"run_state,omitempty"`
	// From the verified charter (the only source of a cap).
	Cap           int   `json:"cap,omitempty"`
	SafeMin       int   `json:"safe_min,omitempty"`
	QueueMaxRows  int   `json:"queue_max_rows,omitempty"`
	QueueMaxBytes int64 `json:"queue_max_bytes,omitempty"`
	// From the database: authoritative counts.
	LiveProcess int   `json:"live_process"`
	LiveSession int   `json:"live_session"`
	QueueRows   int   `json:"queue_rows"`
	QueueBytes  int64 `json:"queue_bytes"`
	// From the host's last report: derived display state, absent when the host is not reporting.
	HostReporting bool              `json:"host_reporting"`
	ReportedAt    int64             `json:"reported_at,omitempty"`
	Effective     *int              `json:"effective,omitempty"`
	Admitting     *bool             `json:"admitting,omitempty"`
	Reasons       []string          `json:"reasons,omitempty"`
	Providers     []GovernorBackoff `json:"providers,omitempty"`
}

// GovernorBackoff is one provider's backoff in the view.
type GovernorBackoff struct {
	Provider string `json:"provider"`
	Value    int    `json:"value"`
	LeftSecs int64  `json:"left_secs"`
}

// GovernorCommands is the `governor` group of the doomsday command; the engine of a build with the mode adds it.
func GovernorCommands(d Deps, status GovernorStatusFunc) *cobra.Command {
	grp := &cobra.Command{Use: "governor", Short: "Resource governor of the open-ended mode: status (read-only)", Hidden: true}
	var generation int64
	st := &cobra.Command{
		Use: "status", Short: "Concurrency cap, effective limit, slots and queue of a run (read-only)", Args: cobra.NoArgs,
		Annotations: map[string]string{AnnotationReadOnly: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if generation < 0 {
				return clicore.InvalidInput("--generation must not be negative")
			}
			if status == nil {
				return renderOff(cmd.OutOrStdout(), d.json())
			}
			v, err := status(cmd.Context(), generation)
			if err != nil {
				var ce *clicore.Error
				if errors.As(err, &ce) {
					return err
				}
				return errors.New("the governor state could not be read")
			}
			if v.Off {
				return renderOff(cmd.OutOrStdout(), d.json())
			}
			if d.json() {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			return renderGovernor(cmd.OutOrStdout(), v)
		},
	}
	st.Flags().Int64Var(&generation, "generation", 0, "Generation to read (default: the open run)")
	grp.AddCommand(st)
	return grp
}

func renderGovernor(w io.Writer, v GovernorView) error {
	fmt.Fprintf(w, "generation %d (run %s): process slots %d of cap %d, session slots %d\n", v.Generation, orDash(v.RunState), v.LiveProcess, v.Cap, v.LiveSession)
	fmt.Fprintf(w, "queue: %d of %d rows, %d of %d bytes\n", v.QueueRows, v.QueueMaxRows, v.QueueBytes, v.QueueMaxBytes)
	if !v.HostReporting {
		_, err := fmt.Fprintln(w, "the host is not reporting (no decision shown; the safe minimum applies whenever the machine cannot be read)")
		return err
	}
	adm := "admitting"
	if v.Admitting != nil && !*v.Admitting {
		adm = "NOT admitting"
	}
	fmt.Fprintf(w, "effective limit %d, %s", *v.Effective, adm)
	if len(v.Reasons) > 0 {
		fmt.Fprintf(w, ", lowered by %v", v.Reasons)
	}
	fmt.Fprintln(w)
	for _, p := range v.Providers {
		fmt.Fprintf(w, "provider %s: limit %d for %d more seconds\n", p.Provider, p.Value, p.LeftSecs)
	}
	return nil
}

// GovernorReader reads the view from the database: the verified charter and the run (the cap), the slot and queue counts
// (live), and the host's last report (derived). It writes nothing.
type GovernorReader struct {
	DB      *sql.DB
	Profile string
	Runs    interface {
		OpenRuns(ctx context.Context, profile string) ([]store.Run, error)
		LoadCharter(ctx context.Context, profile string, generation int64) (store.Charter, error)
	}
	Now func() time.Time
}

// Read returns the view of the open run, or of generation when it is non-zero. No run is the layer being off.
func (r GovernorReader) Read(ctx context.Context, generation int64) (GovernorView, error) {
	runs, err := r.Runs.OpenRuns(ctx, r.Profile)
	if err != nil {
		return GovernorView{}, err
	}
	var run store.Run
	for _, x := range runs {
		if generation == 0 || x.Generation == generation {
			run = x
			break
		}
	}
	if run.Generation == 0 {
		return GovernorView{SchemaVersion: GovernorSchema, Off: true}, nil
	}
	c, err := r.Runs.LoadCharter(ctx, r.Profile, run.Generation)
	if err != nil {
		return GovernorView{}, err
	}
	lim := governor.Limits{Profile: r.Profile, Generation: run.Generation, MaxConcurrent: c.Limits.MaxConcurrent, LivePerTree: c.Limits.LivePerTree,
		SafeMin: c.Limits.SafeMinConcurrent, QueueMaxRows: c.Limits.QueueMaxRows, QueueMaxBytes: c.Limits.QueueMaxBytes}.WithDefaults()
	v := GovernorView{SchemaVersion: GovernorSchema, Generation: run.Generation, RunState: string(run.State), Cap: lim.MaxConcurrent, SafeMin: lim.SafeMin,
		QueueMaxRows: lim.QueueMaxRows, QueueMaxBytes: lim.QueueMaxBytes}
	if err := r.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(kind = 'process'), 0), COALESCE(SUM(kind = 'session'), 0) FROM doomsday_slots WHERE profile = ? AND generation = ? AND released_at IS NULL`,
		r.Profile, run.Generation).Scan(&v.LiveProcess, &v.LiveSession); err != nil {
		return GovernorView{}, err
	}
	if err := r.DB.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(bytes), 0) FROM doomsday_queue WHERE profile = ? AND generation = ? AND state = 'queued'`,
		r.Profile, run.Generation).Scan(&v.QueueRows, &v.QueueBytes); err != nil {
		return GovernorView{}, err
	}
	var raw string
	switch err := r.DB.QueryRowContext(ctx, `SELECT value FROM doomsday_governor_state WHERE profile = ? AND generation = ? AND key = ?`,
		r.Profile, run.Generation, govStateKey).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		return v, nil
	case err != nil:
		return GovernorView{}, err
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	p, ok := parsePublished(raw)
	if !ok || now().Sub(time.Unix(p.At, 0)) > GovernorStaleAfter || run.State != store.StateRunning {
		return v, nil // a report that cannot be read, is old, or belongs to a run that is stopping shows no decision
	}
	// The report row is unauthenticated: it is display only, reported by the host and unverified. Everything taken from it is
	// clamped, filtered to known names, stripped of control characters and bounded, so a forged row can say nothing but plain,
	// small text; the cap and the counts above come from the verified charter and the slot and queue tables.
	eff := min(max(p.Effective, 0), v.Cap)
	v.HostReporting, v.ReportedAt, v.Effective, v.Admitting = true, p.At, &eff, &p.Admitting
	for _, rs := range p.Reasons {
		if knownReasons[rs] && len(v.Reasons) < len(knownReasons) {
			v.Reasons = append(v.Reasons, rs)
		}
	}
	for _, pr := range p.Providers {
		if len(v.Providers) >= maxViewProviders {
			break
		}
		v.Providers = append(v.Providers, GovernorBackoff{Provider: plainName(pr.Provider), Value: min(max(pr.Value, 0), v.Cap), LeftSecs: min(max(pr.LeftSecs, 0), maxBackoffSecs)})
	}
	return v, nil
}

// Bounds of what the view takes from the host's unauthenticated report.
const (
	maxViewProviders = 64
	maxBackoffSecs   = 3600 // the policy never backs off longer than ten minutes
	maxNameBytes     = 64
)

var knownReasons = map[string]bool{
	string(governor.ReasonLoad): true, string(governor.ReasonMemory): true, string(governor.ReasonMemoryStop): true,
	string(governor.ReasonDisk): true, string(governor.ReasonProvider): true, string(governor.ReasonSampleFail): true,
	string(governor.ReasonRecovering): true,
}

// plainName keeps printable ASCII only (anything else becomes '?') and at most maxNameBytes of it.
func plainName(s string) string {
	b := make([]byte, 0, min(len(s), maxNameBytes))
	for _, r := range s {
		if len(b) >= maxNameBytes {
			break
		}
		if r >= 0x20 && r < 0x7f {
			b = append(b, byte(r))
		} else {
			b = append(b, '?')
		}
	}
	return string(b)
}
