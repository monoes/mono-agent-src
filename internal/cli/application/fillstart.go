package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// FillStartWiring is what `fill start` takes from the CLI that Deps does not
// carry; cmd/monoagentcli sets it (application_fill_wire.go).
var FillStartWiring struct {
	// RequireOperator refuses an agent, the chat's runs and workflow nodes.
	RequireOperator func(what string) error
	// DBPath is the --db-path in force, passed on to the process that submits the task.
	DBPath func() string
}

// selfPath is the binary that submits the task: this one. A var so tests stand in for it.
var selfPath = os.Executable

// submitTask runs `aitask submit` with args (after the global flags) and returns its stdout. It is
// run as a process so the operator check, the coder check and the launcher are the ones the user's
// own `aitask submit` goes through. A var so tests stand in for it.
var submitTask = func(ctx context.Context, args []string) ([]byte, error) {
	bin, err := selfPath()
	if err != nil {
		return nil, fmt.Errorf("cannot locate monoagentcli: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	return out, errors.New(strings.TrimSpace(stderr.String()))
}

// coded is a refusal with its machine-readable code and fields for --json.
type coded struct {
	msg    string
	fields map[string]any
}

func (e *coded) Error() string                   { return e.msg }
func (e *coded) JSONErrorFields() map[string]any { return e.fields }
func (e *coded) Unwrap() error                   { return &clicore.Error{Code: 3, Msg: e.msg} }

// openTask reports the application's task when it is still going (not over).
func openTask(store *aitask.Store, applicationID string) (*aitask.Task, error) {
	t, err := store.ForApplication(applicationID)
	if err != nil || t == nil || aitask.Terminal(t.Status) {
		return nil, err
	}
	return t, nil
}

func (d Deps) newFillStartCmd() *cobra.Command {
	var runtime string
	cmd := &cobra.Command{
		Use:   "start <id>",
		Short: "Have the coder fill in the application's form in your browser (never submits)",
		Long: "Hands the application to a coder task of kind " + applyfill.Kind + ": it opens the posting's form in your browser and " +
			"fills the visible fields from your profile facts, then stops. It gets the application's captured posting text, your " +
			"profile facts and the documents registered to this application, and nothing else. It never submits: the submit of an " +
			"application on a real site always waits for your approval (browser permissions, kind apply), and `applied` is only ever " +
			"set by you. A login wall or CAPTCHA ends it as needs_you.\n\n" +
			"One task at a time per application. Operator-only, like `aitask submit`. When coder mode is off it fails with code " +
			"coder_disabled and \"fallback\":\"oneshot\" and does nothing else.",
		Example: `  monoagentcli --profile work application fill start 1c2e...
  monoagentcli --profile work application fill start 1c2e... --runtime codex`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(FillStartWiring.RequireOperator, "have the coder fill in an application"); err != nil {
				return err
			}
			return d.fillStart(cmd, args[0], runtime)
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "Agent runtime (default claude; see 'coder status')")
	clicore.WithJSONErrors(d.JSONOutput, cmd)
	return cmd
}

func (d Deps) fillStart(cmd *cobra.Command, id, runtime string) error {
	ctx := cmd.Context()
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.DB.Close()
	store := applications.NewStore(db.DB)
	profile := d.ProfileID()
	app, err := store.Get(ctx, profile, id)
	if err != nil {
		return d.mapStoreErr(id, err)
	}
	switch app.Status {
	case applications.StatusPending, applications.StatusAwaitingSubmit, applications.StatusNeedsYou:
	default:
		return d.InvalidInput("application %s is %s: only an application still being worked on is filled in", id, app.Status)
	}
	tasks := &aitask.Store{DB: db.DB, Profile: profile}
	if open, err := openTask(tasks, id); err != nil {
		return err
	} else if open != nil {
		return &coded{msg: fmt.Sprintf("application %s already has an open task (%s, %s): wait for it or cancel it first", id, open.ID, open.Status),
			fields: map[string]any{"code": "task_open", "taskId": open.ID}}
	}
	assets, err := BuildAssets(ctx, db.DB, profile, app)
	if err != nil {
		return err
	}
	if strings.TrimSpace(assets.Posting.URL) == "" {
		return d.InvalidInput("application %s has no posting URL to open", id)
	}
	facts, err := applyfill.Facts(ctx, db.DB, profile)
	if err != nil {
		return err
	}
	in := applyfill.Input{ApplicationID: id, Title: applicationTitle(app), URL: assets.Posting.URL,
		PostingText: assets.Posting.CapturedText, Facts: facts}
	for _, doc := range assets.Documents {
		if doc.Path != "" {
			in.Documents = append(in.Documents, doc.Path)
		}
	}
	dir, err := os.MkdirTemp("", "applyfill-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	sources, err := in.WriteSources(dir)
	if err != nil {
		return err
	}
	task, err := d.submitFill(ctx, in, sources, runtime)
	if err != nil {
		return err
	}
	if err := d.linkTask(ctx, store, id, task); err != nil {
		return fmt.Errorf("task %s started but could not be linked to the application: %w", task.ID, err)
	}
	return d.writeJSON(cmd, map[string]any{"applicationId": id, "taskId": task.ID, "conversationId": task.ConversationID, "status": task.Status})
}

func applicationTitle(app *applications.Application) string {
	switch {
	case app.Job != nil:
		return strings.TrimSpace(app.Job.Title + " at " + app.Job.Company)
	case app.Tender != nil:
		return app.Tender.Title
	}
	return app.ID
}

// submitFill runs `aitask submit` for the fill and reads the task it prints.
func (d Deps) submitFill(ctx context.Context, in applyfill.Input, sources []aitask.ContextSource, runtime string) (aitask.Task, error) {
	var global []string
	if FillStartWiring.DBPath != nil {
		global = append(global, "--db-path="+FillStartWiring.DBPath())
	}
	args := append(global, "--profile="+d.ProfileID(), "--json", "aitask", "submit", "--kind", applyfill.Kind,
		"--goal", in.Goal(), "--application", in.ApplicationID)
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	if id, _ := ctx.Value(fillTaskIDKey{}).(string); id != "" {
		args = append(args, "--task-id", id)
	}
	for _, s := range sources {
		args = append(args, "--context", s.Class+"="+s.Path)
	}
	out, err := submitTask(ctx, args)
	if err != nil {
		return aitask.Task{}, refusalOf(out, err)
	}
	var t aitask.Task
	if err := json.Unmarshal(bytes.TrimSpace(out), &t); err != nil || t.ID == "" {
		return t, fmt.Errorf("aitask submit printed no task: %s", strings.TrimSpace(string(out)))
	}
	if want, _ := ctx.Value(fillTaskIDKey{}).(string); want != "" && t.ID != want {
		return t, fmt.Errorf("aitask submit made task %s, not the %s asked for", t.ID, want)
	}
	return t, nil
}

// fillTaskIDKey carries the id a fill's task must take (a resume holds the tab under it before the task exists).
type fillTaskIDKey struct{}

// refusalOf turns the {"error","code"} line a failed submit printed into a
// coded error (coder_disabled keeps its oneshot fallback), else err.
func refusalOf(out []byte, err error) error {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	var r struct{ Error, Code, Fallback string }
	if json.Unmarshal(lines[len(lines)-1], &r) != nil || r.Code == "" {
		return err
	}
	fields := map[string]any{"code": r.Code}
	if r.Code == aitask.CodeCoderDisabled {
		fields["fallback"] = "oneshot"
	}
	return &coded{msg: r.Error, fields: fields}
}

// linkTask makes task the application's conversation link (one per
// application: an earlier task's is replaced) and shows the fill as filling.
func (d Deps) linkTask(ctx context.Context, store *applications.Store, id string, t aitask.Task) error {
	profile := d.ProfileID()
	old, err := store.ListLinks(ctx, profile, id, applications.LinkConversation)
	if err != nil {
		return err
	}
	for _, l := range old {
		if l.RefID != t.ConversationID {
			if _, err := store.RemoveLink(ctx, profile, id, applications.LinkConversation, l.RefID); err != nil {
				return err
			}
		}
	}
	if err := store.AddLink(ctx, profile, id, applications.LinkConversation, t.ConversationID); err != nil {
		return err
	}
	return store.RecordFill(ctx, profile, id, applications.Fill{Status: applications.FillFilling})
}

// taskWorkDir is the task's own working folder, where its screenshot is
// kept (<db dir>/aitask/<task>/work); "" when the database path is unknown.
func taskWorkDir(taskID string) string {
	if FillStartWiring.DBPath == nil {
		return ""
	}
	snapshot := aitask.SnapshotDir(filepath.Dir(clicore.ExpandPath(FillStartWiring.DBPath())), taskID)
	return filepath.Join(filepath.Dir(snapshot), "work")
}

func (d Deps) newFillSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync <id>",
		Short: "Record a finished fill task's result on the application",
		Long: "Reads the application's coder task; when it is a completed " + applyfill.Kind + " task, records its validated result " +
			"as the fill record and moves the application to awaiting_submit (filled) or needs_you. Safe to run again. It never " +
			"sets applied.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			app, err := applications.NewStore(db.DB).Get(cmd.Context(), d.ProfileID(), args[0])
			if err != nil {
				return d.mapStoreErr(args[0], err)
			}
			t, err := (&aitask.Store{DB: db.DB, Profile: d.ProfileID()}).ForApplication(app.ID)
			if err != nil {
				return err
			}
			if t == nil || t.Kind != applyfill.Kind {
				return d.NotFound("application %s has no %s task", args[0], applyfill.Kind)
			}
			if err := applyfill.Finalize(cmd.Context(), db.DB, d.ProfileID(), *t, taskWorkDir(t.ID)); err != nil {
				if errors.Is(err, applyfill.ErrNotFinished) {
					return d.writeJSON(cmd, map[string]any{"applicationId": app.ID, "taskId": t.ID, "taskStatus": t.Status, "recorded": false})
				}
				return err
			}
			return d.writeJSON(cmd, map[string]any{"applicationId": app.ID, "taskId": t.ID, "taskStatus": t.Status, "recorded": true})
		},
	}
	clicore.WithJSONErrors(d.JSONOutput, cmd)
	return cmd
}
