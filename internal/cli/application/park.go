package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/applypark"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// Parking: the agent fills an application, its tab stays open (parked), the
// next queued application starts, and the person submits in the tab. These
// commands are the operator's: an agent shell can neither queue, resume nor
// sweep. None of them sets `applied` (only `application send` does).

// pollEvery is how often a queue looks at a running task. A var for tests.
var pollEvery = 2 * time.Second

// liveTabs reads `browser tabs` as this process, the operator, sees them.
// A var so tests stand in for the browser.
var liveTabs = func(ctx context.Context, d Deps) ([]applypark.LiveTab, error) {
	return LiveTabs(ctx, d.ProfileID())
}

// LiveTabs runs `browser tabs` as a process (the operator's view of every tab)
// and returns the tabs. cmd/monoagentcli hands it to applyfill.Parking.
func LiveTabs(ctx context.Context, profile string) ([]applypark.LiveTab, error) {
	var global []string
	if FillStartWiring.DBPath != nil {
		global = append(global, "--db-path="+FillStartWiring.DBPath())
	}
	out, err := submitTask(ctx, append(global, "--profile="+profile, "--json", "browser", "tabs"))
	if err != nil {
		return nil, fmt.Errorf("reading the browser's tabs: %w", err)
	}
	var res struct {
		Tabs []applypark.LiveTab `json:"tabs"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
		return nil, fmt.Errorf("reading the browser's tabs: %w", err)
	}
	return res.Tabs, nil
}

func (d Deps) parkCmds() []*cobra.Command {
	return []*cobra.Command{d.newFillQueueCmd(), d.newFillResumeCmd(), d.newFillSweepCmd(), d.newFillTabCmd()}
}

func (d Deps) requireOperator(what string) error {
	return clicore.RequireOperator(FillStartWiring.RequireOperator, what)
}

func (d Deps) newFillSweepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Reconcile the parked tabs with the browser's tabs",
		Long: "Compares the parked application tabs with `browser tabs`. A tab that is gone turns its application into " +
			"needs_you (\"Reopen and refill\"); a tab of an application that is already over is closed. Nothing is changed when " +
			"the browser cannot be read. It never sets applied.\n\n" + clicore.OperatorOnlyHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.requireOperator("sweep the parked application tabs"); err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			res, err := d.sweep(cmd.Context(), db.DB)
			if err != nil {
				return err
			}
			return d.writeJSON(cmd, res)
		},
	}
	clicore.WithJSONErrors(d.JSONOutput, cmd)
	return cmd
}

func (d Deps) sweep(ctx context.Context, db *sql.DB) (applypark.SweepResult, error) {
	live, err := liveTabs(ctx, d)
	if err != nil {
		return applypark.SweepResult{}, err
	}
	return applypark.Sweep(ctx, applypark.Store{DB: db, Profile: d.ProfileID()}, applications.NewStore(db), live)
}

func (d Deps) newFillTabCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tab <id>",
		Short: "Show the tab an application's fill left open, and whether a submit was seen in it",
		Long: "tab is null when no fill left a tab. submitObservedAt is set when the extension saw the person submit the form " +
			"in that tab (a trusted submit followed by a navigation or an on-page change). It is only a hint to ask the person: " +
			"the application stays awaiting_submit until `application send`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			if _, err := applications.NewStore(db.DB).Get(cmd.Context(), d.ProfileID(), args[0]); err != nil {
				return d.mapStoreErr(args[0], err)
			}
			tab, err := applypark.Store{DB: db.DB, Profile: d.ProfileID()}.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			at, err := applypark.ObservedAt(cmd.Context(), db.DB, args[0])
			if err != nil {
				return err
			}
			return d.writeJSON(cmd, map[string]any{"applicationId": args[0], "tab": tab, "submitObservedAt": at})
		},
	}
	clicore.WithJSONErrors(d.JSONOutput, cmd)
	return cmd
}

// startFill runs fill start for id and returns its task id.
func (d Deps) startFill(ctx context.Context, id, runtime string) (string, error) {
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetContext(ctx)
	if err := d.fillStart(c, id, runtime); err != nil {
		return "", err
	}
	var res struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.TaskID == "" {
		return "", fmt.Errorf("fill start printed no task")
	}
	return res.TaskID, nil
}

func (d Deps) newFillResumeCmd() *cobra.Command {
	var runtime string
	cmd := &cobra.Command{
		Use:   "resume <id>",
		Short: "Have the coder take a parked application up again (a new task)",
		Long: "Starts a new " + applyfill.Kind + " task for an application whose tab is parked or lost, and binds the tab to " +
			"that task alone: until the task is over no other task can act on it. The agent never touches a parked tab unless " +
			"you resume it. Refused while the application has a task still open.\n\n" + clicore.OperatorOnlyHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.requireOperator("resume a parked application"); err != nil {
				return err
			}
			return d.fillResume(cmd, args[0], runtime)
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "Agent runtime (default claude; see 'coder status')")
	clicore.WithJSONErrors(d.JSONOutput, cmd)
	return cmd
}

func (d Deps) fillResume(cmd *cobra.Command, id, runtime string) error {
	ctx := cmd.Context()
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.DB.Close()
	if _, err := applications.NewStore(db.DB).Get(ctx, d.ProfileID(), id); err != nil {
		return d.mapStoreErr(id, err)
	}
	tabs := applypark.Store{DB: db.DB, Profile: d.ProfileID()}
	tab, err := tabs.Get(ctx, id)
	if err != nil {
		return err
	}
	if tab == nil || (tab.State != applypark.StateParked && tab.State != applypark.StateLost) {
		return d.InvalidInput("application %s has no parked or lost tab: use `application fill start` to fill it", id)
	}
	hold := tab.State == applypark.StateParked
	if hold {
		if err := tabs.SetState(ctx, id, applypark.StateActive, applypark.ResumePending); err != nil {
			return err
		}
	}
	taskID, err := d.startFill(ctx, id, runtime)
	if err != nil {
		if hold {
			_ = tabs.SetState(ctx, id, applypark.StateParked, tab.TaskID)
		}
		return err
	}
	if hold {
		if err := tabs.SetState(ctx, id, applypark.StateActive, taskID); err != nil {
			return err
		}
	}
	if err := applypark.ClearObserved(ctx, db.DB, id); err != nil {
		return err
	}
	return d.writeJSON(cmd, map[string]any{"applicationId": id, "taskId": taskID, "tabId": tab.TabID, "tabState": map[bool]string{true: "active", false: "lost"}[hold]})
}

func (d Deps) newFillQueueCmd() *cobra.Command {
	var runtime string
	var maxParked int
	var all bool
	cmd := &cobra.Command{
		Use:   "queue [id...]",
		Short: "Fill several applications in turn, each left parked in its tab for you to submit",
		Long: "Fills the named applications (or, with --pending, every pending one) one after another. Each fill is the " +
			"confined " + applyfill.Kind + " task; when it ends the application is parked at awaiting_submit with its tab left " +
			"open (or needs_you with the reason) and the next one starts. At most --max-parked fills run or wait for your submit " +
			"at once (default: config applications.max_parked, else 3; 1 to 10): as you submit or close tabs, more start.\n\n" +
			"It runs in the foreground until every application has been tried (Ctrl-C stops it; fills already running keep going " +
			"as tasks). It never submits and never sets applied.\n\n" + clicore.OperatorOnlyHelp,
		Example: `  monoagentcli --profile work application fill queue 1c2e... 8f3a... --max-parked 2
  monoagentcli --profile work application fill queue --pending`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.requireOperator("queue application fills"); err != nil {
				return err
			}
			if cmd.Flags().Changed("max-parked") && (maxParked < applypark.MinMaxParked || maxParked > applypark.MaxMaxParked) {
				return d.InvalidInput("--max-parked must be %d to %d, got %d", applypark.MinMaxParked, applypark.MaxMaxParked, maxParked)
			}
			if all == (len(args) > 0) {
				return d.InvalidInput("name the applications to fill, or use --pending (not both)")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return d.fillQueue(ctx, cmd, args, all, maxParked, runtime)
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "Agent runtime (default claude; see 'coder status')")
	cmd.Flags().IntVar(&maxParked, "max-parked", 0, "Most fills running or waiting for your submit at once (1 to 10)")
	cmd.Flags().BoolVar(&all, "pending", false, "Queue every pending application of the profile")
	clicore.WithJSONErrors(d.JSONOutput, cmd)
	return cmd
}

func (d Deps) fillQueue(ctx context.Context, cmd *cobra.Command, args []string, all bool, maxParked int, runtime string) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.DB.Close()
	profile := d.ProfileID()
	apps := applications.NewStore(db.DB)
	ids, err := d.queueIDs(ctx, apps, args, all)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return d.writeJSON(cmd, map[string]any{"max": applypark.MaxParked(db.DB), "outcomes": []applypark.Outcome{}})
	}
	if maxParked == 0 {
		maxParked = applypark.MaxParked(db.DB)
	}
	tasks := &aitask.Store{DB: db.DB, Profile: profile}
	q := applypark.Queue{
		Store:  applypark.Store{DB: db.DB, Profile: profile},
		Slots:  applypark.Slots{DB: db.DB, Profile: profile, Owner: aitask.NewID(), Pid: os.Getpid()},
		Max:    maxParked,
		Tick:   pollEvery,
		Log:    func(f string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", a...) },
		Runner: queueRunner{d: d, db: db.DB, tasks: tasks, runtime: runtime},
		Status: func(ctx context.Context, id string) (string, error) {
			a, err := apps.Get(ctx, profile, id)
			if err != nil {
				return "", err
			}
			return string(a.Status), nil
		},
	}
	outcomes, runErr := q.Run(ctx, ids)
	if err := d.writeJSON(cmd, map[string]any{"max": q.Max, "outcomes": outcomes}); err != nil {
		return err
	}
	if runErr != nil && ctx.Err() == nil {
		return runErr
	}
	return nil
}

// queueIDs checks the applications the queue was asked for: each must exist,
// be one still being filled in (pending or needs_you) and have no open task.
func (d Deps) queueIDs(ctx context.Context, apps *applications.Store, args []string, all bool) ([]string, error) {
	profile := d.ProfileID()
	if all {
		list, err := apps.List(ctx, profile, applications.ListFilter{Status: applications.StatusPending})
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			args = append(args, a.ID)
		}
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range args {
		id = strings.TrimSpace(id)
		if seen[id] {
			continue
		}
		seen[id] = true
		a, err := apps.Get(ctx, profile, id)
		if err != nil {
			return nil, d.mapStoreErr(id, err)
		}
		if a.Status != applications.StatusPending && a.Status != applications.StatusNeedsYou {
			return nil, d.InvalidInput("application %s is %s: only a pending or needs_you application is queued", id, a.Status)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// queueRunner is applypark.Runner over the application commands.
type queueRunner struct {
	d       Deps
	db      *sql.DB
	tasks   *aitask.Store
	runtime string
}

func (r queueRunner) Start(ctx context.Context, id string) (string, error) {
	if err := applypark.ClearObserved(ctx, r.db, id); err != nil {
		return "", err
	}
	return r.d.startFill(ctx, id, r.runtime)
}

func (r queueRunner) Wait(ctx context.Context, taskID string) (string, error) {
	for {
		t, err := r.tasks.Get(taskID)
		if err != nil {
			return "", err
		}
		if aitask.Terminal(t.Status) || t.Status == aitask.StatusNeedsUser {
			return t.Status, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

func (r queueRunner) Finalize(ctx context.Context, id string) error {
	t, err := r.tasks.ForApplication(id)
	if err != nil {
		return err
	}
	if t == nil || t.Kind != applyfill.Kind {
		return fmt.Errorf("application %s has no %s task", id, applyfill.Kind)
	}
	if t.Status == aitask.StatusNeedsUser {
		return r.needsYou(ctx, id, "The fill stopped before it finished: open its chat to continue.")
	}
	return applyfill.Finalize(ctx, r.db, r.d.ProfileID(), *t, taskWorkDir(t.ID))
}

func (r queueRunner) Sweep(ctx context.Context) error {
	_, err := r.d.sweep(ctx, r.db)
	return err
}

// needsYou parks an application whose task ended without a result.
func (r queueRunner) needsYou(ctx context.Context, id, reason string) error {
	profile := r.d.ProfileID()
	apps := applications.NewStore(r.db)
	fill, err := apps.GetFill(ctx, profile, id)
	if err != nil {
		return err
	}
	fill.Status, fill.Note = applications.FillNeedsYou, reason
	if err := apps.RecordFill(ctx, profile, id, fill); err != nil {
		return err
	}
	if app, err := apps.Get(ctx, profile, id); err != nil || app.Status == applications.StatusNeedsYou {
		return err
	}
	return apps.SetStatus(ctx, profile, id, applications.StatusNeedsYou, applications.ActorSystem, reason)
}
