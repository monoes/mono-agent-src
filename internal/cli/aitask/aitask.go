// Package aitask is `monoagentcli aitask`: hand a goal to a coder conversation
// and get a checked result back (Contract A of the coder front-door plan). The
// model is internal/aitask; this package is the commands, and the processes
// that run a task's turn.
package aitask

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentclient"
	"github.com/monoes/mono-agent/internal/aitask"
	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	JSONOutput func() bool
	// ProfileID is the --profile flag, or the profile OpenDB resolved; it is read when a command runs.
	ProfileID func() string
	DBPath    func() string
	OpenDB    func() (*storage.Database, error)
	// RequireOperator refuses an agent, the chat's runs and workflow nodes: only the user does what.
	RequireOperator func(what string) error
	// Launcher builds the conversation and turn launcher; nil runs this binary.
	Launcher func() aitask.Launcher
	// API is the agent API client in client mode (MONOAGENT_API_URL and MONOAGENT_API_TOKEN set), else nil.
	// With a client, kinds, status, result, complete, fail and cancel go through it and never open the
	// database; the root refuses every other command before it runs.
	API func() *agentclient.Client
}

// New is `monoagentcli aitask`.
func New(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aitask",
		Short: "Hand a goal to a coder conversation and get a checked result back",
		Long: "An AI task is a goal, some context and a result contract, run as a normal coder conversation (it appears as a " +
			"bubble in the app). The agent finishes with `aitask complete <id> --result FILE`, which checks the file against " +
			"the kind's schema; a conversation that ends without it is `needs_user`, never success. When coder mode is off, " +
			"`submit` fails with code coder_disabled and \"fallback\":\"oneshot\" and does nothing else: the caller decides.\n\n" +
			"submit, start, wait and the rest are the user's own (an agent, the chat or a workflow is refused: exit 3, " +
			"operator_only). The task's own session, which holds its token (" + aitask.EnvTaskID + ", " + aitask.EnvToken +
			"), may only status, result, list, cancel, complete and fail that one task.",
	}
	c := &cli{d: &d}
	for _, sub := range []*cobra.Command{c.submit(), c.submitPage(), c.start(), c.status(), c.result(), c.wait(), c.cancel(), c.list(),
		c.complete(), c.fail(), c.supervise(), c.budgetCmd(), c.kinds()} {
		clicore.WithJSONErrors(d.JSONOutput, sub)
		cmd.AddCommand(sub)
	}
	return cmd
}

type cli struct{ d *Deps }

// remote is the agent API client when this process is in client mode, else nil.
func (c *cli) remote() *agentclient.Client {
	if c.d.API == nil {
		return nil
	}
	return c.d.API()
}

// remoteTask runs op against the agent API and prints the task it returns.
func (c *cli) remoteTask(cmd *cobra.Command, api *agentclient.Client, op func(*agentclient.Client) (aitask.Task, error)) error {
	t, err := op(api)
	if err != nil {
		return cliErr(err)
	}
	return c.print(cmd, t, human(t))
}

// open returns the service for the profile; closeDB must be called.
func (c *cli) open() (*aitask.Service, func(), error) {
	db, err := c.d.OpenDB()
	if err != nil {
		return nil, nil, fmt.Errorf("initializing database: %w", err)
	}
	var l aitask.Launcher = selfLauncher{c.d}
	if c.d.Launcher != nil {
		l = c.d.Launcher()
	}
	svc := &aitask.Service{
		Store:    &aitask.Store{DB: db.DB, Profile: c.d.ProfileID()},
		Launcher: l,
		StateDir: filepath.Dir(clicore.ExpandPath(c.d.DBPath())),
		Redact:   aitask.RedactValues([]string{os.Getenv(aitask.EnvToken)}),
	}
	svc.Admit = ledger(svc).Admitter()
	return svc, func() { db.Close() }, nil
}

// holder says whether this process is task id's own session: it carries the
// task's id and a token that matches.
func holder(s *aitask.Store, id string) bool {
	return os.Getenv(aitask.EnvTaskID) == id && s.CheckToken(id, os.Getenv(aitask.EnvToken))
}

// authorize lets the task's own session through when holderOK, and
// everyone else only when they are the operator.
func (c *cli) authorize(s *aitask.Store, what, id string, holderOK bool) error {
	if holderOK && holder(s, id) {
		return nil
	}
	return clicore.RequireOperator(c.d.RequireOperator, what)
}

// coded is a refusal with its machine-readable code for --json.
type coded struct {
	exit   int
	msg    string
	fields map[string]any
}

func (e *coded) Error() string                   { return e.msg }
func (e *coded) JSONErrorFields() map[string]any { return e.fields }
func (e *coded) Unwrap() error                   { return &clicore.Error{Code: e.exit, Msg: e.msg} }

// cliErr gives an aitask.Error its exit code and --json fields.
func cliErr(err error) error {
	var ae *aitask.Error
	if !errors.As(err, &ae) {
		return err
	}
	exit := 3
	switch ae.Code {
	case aitask.CodeNotFound:
		exit = 2
	case aitask.CodeWaitTimeout:
		exit = 1
	}
	fields := map[string]any{"code": ae.Code}
	for k, v := range ae.Fields {
		fields[k] = v
	}
	return &coded{exit: exit, msg: ae.Message, fields: fields}
}

func (c *cli) print(cmd *cobra.Command, v any, human string) error {
	if c.d.JSONOutput() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), human)
	return err
}

func human(t aitask.Task) string {
	s := fmt.Sprintf("%s  %s  %s  conversation %s", t.ID, t.Kind, t.Status, t.ConversationID)
	if t.Error != "" {
		s += "\nerror: " + t.Error
	}
	if len(t.Result) > 0 {
		s += "\nresult: " + string(t.Result)
	}
	return s
}

func (c *cli) submit() *cobra.Command {
	var in aitask.SubmitInput
	var contextArgs []string
	var budget float64
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Create a task and its coder conversation, and start its first turn",
		Long: "Creates a coder conversation in the profile's folder, writes --context (CLASS=PATH; messages and people threads are refused) into <folder>/" + aitask.ContextDir +
			"/<task>/ (data, never instructions; size-capped; secret values hidden), builds the first turn from the --kind and " +
			"starts it. --queue-only leaves the task queued for `aitask start` (the person's click). Kinds: " +
			strings.Join(aitask.Names(), ", ") + ". --unattended needs --access qa and a kind that allows it; with a " +
			"full-access level it is refused (unattended_full_access_refused). Refused inside an agent.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := clicore.RequireOperator(c.d.RequireOperator, "submit an AI task"); err != nil {
				return err
			}
			svc, closeDB, err := c.open()
			if err != nil {
				return err
			}
			defer closeDB()
			for _, arg := range contextArgs {
				cs, err := aitask.ParseContextSource(arg)
				if err != nil {
					return cliErr(err)
				}
				in.Context = append(in.Context, cs)
			}
			if cmd.Flags().Changed("budget-usd") {
				in.BudgetUSD = &budget
			}
			t, err := svc.Submit(cmd.Context(), in)
			if err != nil {
				return cliErr(err)
			}
			return c.print(cmd, t, human(t))
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.Kind, "kind", "generic", "Task kind")
	f.StringVar(&in.Goal, "goal", "", "What the agent is asked to do")
	f.StringArrayVar(&contextArgs, "context", nil, "CLASS=PATH: a file or folder for the task's context (repeatable); CLASS is "+strings.Join(aitask.ContextSources, ", "))
	f.StringVar(&in.ApplicationID, "application", "", "The application the task is about")
	f.StringVar(&in.Access, "access", "", "coding, workspace or qa (default: the kind's)")
	f.StringVar(&in.Runtime, "runtime", "", "Agent runtime (default claude; see 'coder status')")
	f.BoolVar(&in.Unattended, "unattended", false, "Run without a person watching (only for a qa task of a kind that allows it)")
	f.Float64Var(&budget, "budget-usd", 0, "Spend cap in USD (default: the coder budget); 0 means no spend and is refused, never unlimited")
	f.BoolVar(&in.QueueOnly, "queue-only", false, "Create the task but do not start it (aitask start does)")
	return cmd
}

func (c *cli) start() *cobra.Command {
	return &cobra.Command{
		Use:   "start <id>",
		Short: "Start a queued task's first turn",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(c.d.RequireOperator, "start an AI task"); err != nil {
				return err
			}
			return c.withTask(cmd, args[0], func(svc *aitask.Service) (aitask.Task, error) {
				if t, err := svc.Store.Get(args[0]); err == nil && t.Status == aitask.StatusQueued {
					if err := c.checkTarget(cmd.Context(), t); err != nil {
						return aitask.Task{}, err
					}
				}
				return svc.Start(cmd.Context(), args[0])
			})
		},
	}
}

// withTask runs op on the service and prints the task it returns.
func (c *cli) withTask(cmd *cobra.Command, id string, op func(*aitask.Service) (aitask.Task, error)) error {
	svc, closeDB, err := c.open()
	if err != nil {
		return err
	}
	defer closeDB()
	t, err := op(svc)
	if err != nil {
		return cliErr(err)
	}
	return c.print(cmd, t, human(t))
}

// readTask is status and result: the operator's, or the task's own session's.
func (c *cli) readTask(use, short string, resultOnly bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api := c.remote(); api != nil {
				if resultOnly {
					r, err := api.Result(cmd.Context(), args[0])
					if err != nil {
						return cliErr(err)
					}
					return c.print(cmd, map[string]any{"taskId": r.TaskID, "status": r.Status, "result": r.Result, "error": r.Error},
						human(aitask.Task{ID: r.TaskID, Status: r.Status, Result: r.Result, Error: r.Error}))
				}
				return c.remoteTask(cmd, api, func(a *agentclient.Client) (aitask.Task, error) { return a.Task(cmd.Context(), args[0]) })
			}
			svc, closeDB, err := c.open()
			if err != nil {
				return err
			}
			defer closeDB()
			if err := c.authorize(svc.Store, "read an AI task", args[0], true); err != nil {
				return err
			}
			t, err := svc.Store.Get(args[0])
			if err != nil {
				return cliErr(err)
			}
			if resultOnly && c.d.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"taskId": t.ID, "status": t.Status, "result": t.Result, "error": t.Error})
			}
			return c.print(cmd, t, human(t))
		},
	}
}

func (c *cli) status() *cobra.Command {
	return c.readTask("status", "Show a task", false)
}

func (c *cli) result() *cobra.Command {
	return c.readTask("result", "Show a task's result (empty until it is complete)", true)
}

func (c *cli) wait() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait <id>",
		Short: "Block until a task is over or needs the user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(c.d.RequireOperator, "wait for an AI task"); err != nil {
				return err
			}
			svc, closeDB, err := c.open()
			if err != nil {
				return err
			}
			defer closeDB()
			t, err := svc.Wait(cmd.Context(), args[0], timeout)
			if err != nil {
				return cliErr(err)
			}
			return c.print(cmd, t, human(t))
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "Give up after this long (0 waits for ever); exits 1 with code wait_timeout")
	return cmd
}

func (c *cli) cancel() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <id>",
		Short: "Stop a task that is not over",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api := c.remote(); api != nil {
				return c.remoteTask(cmd, api, func(a *agentclient.Client) (aitask.Task, error) {
					return a.Cancel(cmd.Context(), args[0], os.Getenv(aitask.EnvToken))
				})
			}
			svc, closeDB, err := c.open()
			if err != nil {
				return err
			}
			defer closeDB()
			if err := c.authorize(svc.Store, "cancel an AI task", args[0], true); err != nil {
				return err
			}
			t, err := svc.Cancel(args[0])
			if err != nil {
				return cliErr(err)
			}
			return c.print(cmd, t, human(t))
		},
	}
}

func (c *cli) list() *cobra.Command {
	var kind, application string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the profile's tasks, newest first (a task's own session sees only its own)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, closeDB, err := c.open()
			if err != nil {
				return err
			}
			defer closeDB()
			tasks, err := svc.Store.List(kind, application)
			if err != nil {
				return err
			}
			if err := clicore.RequireOperator(c.d.RequireOperator, "list AI tasks"); err != nil {
				id := os.Getenv(aitask.EnvTaskID)
				if id == "" || !holder(svc.Store, id) {
					return err
				}
				own := tasks[:0:0]
				for _, t := range tasks {
					if t.ID == id {
						own = append(own, t)
					}
				}
				tasks = own
			}
			lines := make([]string, 0, len(tasks))
			for _, t := range tasks {
				lines = append(lines, human(t))
			}
			return c.print(cmd, map[string]any{"tasks": tasks}, strings.Join(lines, "\n"))
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Only this kind")
	cmd.Flags().StringVar(&application, "application", "", "Only this application's tasks")
	return cmd
}

// readResultFile reads the agent's result: a file, or stdin for "-".
func readResultFile(cmd *cobra.Command, path string) ([]byte, error) {
	var r io.Reader = cmd.InOrStdin()
	if path != "-" {
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, clicore.InvalidInput("--result %s is not a readable file", path)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, clicore.InvalidInput("--result %s: %v", path, err)
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, aitask.MaxResultBytes+1))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// resultPathAllowed returns the file --result may read for task id, resolved:
// "-" (stdin) is not a file; for a confined task the path must lie under the
// task's own work or context folder. A confined agent is fed text written by
// strangers, so a --result of any file would let it probe the user's files (the
// schema errors name keys and syntax). It FAILS CLOSED: when the task cannot
// be looked up the path is refused, and so is a symlink that does not resolve.
// A task of another kind reads any path, as before. The caller reads the
// returned path, not the one it was given, so a link swapped in afterwards
// leads nowhere.
func (c *cli) resultPathAllowed(id, path string) (string, error) {
	if path == "-" {
		return path, nil
	}
	refuse := func(kind string) (string, error) {
		return "", clicore.InvalidInput("--result %s is not under this task's work folder: a %s task may hand back a file from there, "+
			"or its result with --result-json, or - for stdin", filepath.Base(path), kind)
	}
	db, err := c.d.OpenDB()
	if err != nil {
		return refuse("confined")
	}
	t, err := (&aitask.Store{DB: db.DB, Profile: c.d.ProfileID()}).Get(id)
	db.Close()
	if err != nil {
		return refuse("confined")
	}
	if !chatcli.ConfinedKind(t.Kind) {
		return path, nil
	}
	stateDir := filepath.Dir(clicore.ExpandPath(c.d.DBPath()))
	snapshot := aitask.SnapshotDir(stateDir, id)
	work := filepath.Join(filepath.Dir(snapshot), "work")
	resolved, err := resolveBelow(path, work, snapshot)
	if err != nil {
		return refuse(t.Kind)
	}
	return resolved, nil
}

// resolveBelow resolves path (symlinks too) and returns it when it lies under
// one of dirs, themselves resolved. A path that is a link must resolve (a
// dangling one is outside); a path that does not exist yet is judged by its
// parent folder.
func resolveBelow(path string, dirs ...string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		if _, lerr := os.Lstat(abs); lerr == nil {
			return "", errors.New("dangling link") // the link exists, its target does not
		}
		parent, perr := filepath.EvalSymlinks(filepath.Dir(abs))
		if perr != nil {
			return "", perr
		}
		target, err = filepath.Join(parent, filepath.Base(abs)), nil
	}
	if err != nil {
		return "", err
	}
	for _, d := range dirs {
		rd, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(rd, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return target, nil
		}
	}
	return "", errors.New("outside")
}

func (c *cli) complete() *cobra.Command {
	var resultFile, resultJSON string
	cmd := &cobra.Command{
		Use:   "complete <id> (--result FILE | --result-json JSON)",
		Short: "Hand a task's result back (only the task's own session)",
		Long: "Checks FILE (a JSON value; - reads stdin) or the --result-json text against the task kind's schema and records it as the result. " +
			"A result that doesn't match is refused (exit 3, schema_invalid) with the reason; fix it and run it " +
			"again. A confined task (one whose agent has no file tools) hands its result back with --result-json, or from a " +
			"file in its own work folder. Needs the task's token, " + aitask.EnvToken + " in this session's environment.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (resultFile == "") == (resultJSON == "") {
				return clicore.InvalidInput("give exactly one of --result FILE and --result-json JSON")
			}
			var b []byte
			if resultJSON != "" {
				if len(resultJSON) > aitask.MaxResultBytes {
					return clicore.InvalidInput("--result-json is larger than %d MB", aitask.MaxResultBytes>>20)
				}
				b = []byte(resultJSON)
			} else {
				path := resultFile
				if c.remote() != nil {
					// Client mode cannot look the task up to tell whether it is confined, so a file is
					// refused (a confined agent must not probe the user's files): stdin and --result-json work.
					if path != "-" {
						return clicore.InvalidInput("in agent mode hand the result back with --result-json JSON or --result - (stdin)")
					}
				} else {
					var err error
					if path, err = c.resultPathAllowed(args[0], resultFile); err != nil {
						return err
					}
				}
				var err error
				if b, err = readResultFile(cmd, path); err != nil {
					return err
				}
			}
			if api := c.remote(); api != nil {
				return c.remoteTask(cmd, api, func(a *agentclient.Client) (aitask.Task, error) {
					return a.Complete(cmd.Context(), args[0], os.Getenv(aitask.EnvToken), b)
				})
			}
			return c.withTask(cmd, args[0], func(svc *aitask.Service) (aitask.Task, error) {
				return svc.Complete(args[0], os.Getenv(aitask.EnvToken), b)
			})
		},
	}
	cmd.Flags().StringVar(&resultFile, "result", "", "File holding the result JSON (- for stdin)")
	cmd.Flags().StringVar(&resultJSON, "result-json", "", "The result JSON itself, for an agent that cannot write a file")
	return cmd
}

func (c *cli) fail() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "fail <id> --reason TEXT",
		Short: "Say a task can't be done (only the task's own session)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api := c.remote(); api != nil {
				return c.remoteTask(cmd, api, func(a *agentclient.Client) (aitask.Task, error) {
					return a.Fail(cmd.Context(), args[0], os.Getenv(aitask.EnvToken), reason)
				})
			}
			return c.withTask(cmd, args[0], func(svc *aitask.Service) (aitask.Task, error) {
				return svc.Fail(args[0], os.Getenv(aitask.EnvToken), reason)
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why the task can't be done")
	return cmd
}

// supervise runs a task's turn and records how it ended. `submit` and `start`
// run it detached.
func (c *cli) supervise() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "supervise <id>",
		Short:  "Run a task's turn and record how it ended (started by submit and start)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(c.d.RequireOperator, "supervise an AI task"); err != nil {
				return err
			}
			svc, closeDB, err := c.open()
			if err != nil {
				return err
			}
			defer closeDB()
			t, err := svc.Store.Get(args[0])
			if err != nil {
				return cliErr(err)
			}
			if t.Status != aitask.StatusRunning {
				return nil
			}
			// From here the token is held by this process and what it runs.
			if err := svc.Store.SetHolder(t.ID, os.Getpid()); err != nil {
				return err
			}
			turnErr := runTurn(cmd.Context(), c.d, svc, t)
			text := ""
			if turnErr != nil {
				text = "the agent's turn ended with an error: " + turnErr.Error()
			}
			err = svc.EndTurn(t.ID, text)
			// What the task spent is charged to the budgets, and its unused cap freed.
			_ = ledger(svc).Settle(cmd.Context(), t.ID, t.ConversationID)
			return err
		},
	}
	return cmd
}
