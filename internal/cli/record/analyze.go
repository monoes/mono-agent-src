package record

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/recordanalyze"
	"github.com/monoes/mono-agent/internal/recording"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// addRecordAnalyzeCommands adds `record analyze|verify|save`. Owned by the
// analyze builder. JSON errors come from NewCommand's wrapper.
func addRecordAnalyzeCommands(parent *cobra.Command, d Deps) {
	parent.AddCommand(newRecordAnalyzeCmd(d), newRecordVerifyCmd(d), newRecordSaveCmd(d))
}

// ExecRunner is analyze's production AI runner: monomind agent exec.
func ExecRunner(runtime, model string, timeout time.Duration) recordanalyze.Runner {
	return recordanalyze.ExecRunner{Runtime: runtime, Model: model, Timeout: timeout}
}

// VaultSecretLookup returns the profile vault lookup for an automation's
// secrets — the same one node runs use ("<automation>/<name>", then the bare
// name unless the package is imported) — or nil when the database cannot be
// opened. The returned func closes the database. It is the production
// Deps.SecretLookup.
func (d Deps) VaultSecretLookup(ctx context.Context, automationID string) (func(string) (string, bool), func()) {
	db, err := d.OpenDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: vault unavailable for verify secrets:", err)
		return nil, func() {}
	}
	return nodes.SecretLookup(ctx, db.DB, d.ProfileID(), automationID), func() { db.Close() }
}

// resolveDraftArg accepts a draft name, an absolute path, or a path
// relative to the working directory; all must resolve inside
// recording-drafts (recording.ResolveDraftDir enforces that).
func resolveDraftArg(ref string) (string, error) {
	if ref != "" && !filepath.IsAbs(ref) && !recording.ValidID(ref) && !strings.HasPrefix(ref, "-") {
		abs, err := filepath.Abs(ref)
		if err != nil {
			return "", err
		}
		ref = abs
	}
	return recording.ResolveDraftDir(ref)
}

// monoagentHome is ~/.monoagent (the parent of recording-drafts).
func monoagentHome() (string, error) {
	drafts, err := recording.DraftsDir()
	if err != nil {
		return "", err
	}
	return filepath.Dir(drafts), nil
}

func newRecordAnalyzeCmd(d Deps) *cobra.Command {
	var target, runtime, model string
	var timeout time.Duration
	var allowAdvanced bool
	cmd := &cobra.Command{
		Use:   "analyze <recording>",
		Short: "Turn a recording into a draft automation action (AI via the monomind runner)",
		Long: `Normalizes the recording, detects inputs, lists, repetitions and a login,
asks the AI (monomind agent exec) for an action draft, lints it against the
recorded DOM and writes it to ~/.monoagent/recording-drafts/<recording>/.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := applyRecordScope(d); err != nil {
				return err
			}
			dir, err := recording.Find(args[0])
			if err != nil {
				return err
			}
			rec, err := recordanalyze.LoadRecording(dir)
			if err != nil {
				return err
			}
			home, err := monoagentHome()
			if err != nil {
				return err
			}
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			res, err := recordanalyze.Analyze(cmd.Context(), rec, recordanalyze.AnalyzeOptions{
				Home: home, Target: target, Registry: reg, AllowAdvanced: allowAdvanced,
				Runner: d.AnalyzeRunner(runtime, model, timeout),
			})
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), res)
			}
			printAnalyzeResult(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "automation", "", "Add the action to this installed automation (default: a new automation)")
	cmd.Flags().StringVar(&runtime, "runtime", recordanalyze.DefaultRuntime, "Agent runtime for monomind agent exec")
	cmd.Flags().StringVar(&model, "model", "", "Model for the runtime (default: the runtime's own)")
	cmd.Flags().BoolVar(&allowAdvanced, "allow-advanced", false, "Let the draft use page_script, http_fetch_in_page, call_action, upload and download")
	cmd.Flags().DurationVar(&timeout, "timeout", recordanalyze.DefaultTimeout, "Timeout of the AI turn")
	return cmd
}

func printAnalyzeResult(w io.Writer, res *recordanalyze.Result) {
	d := res.Draft
	kind := "existing automation"
	if d.IsNew {
		kind = "new automation"
	}
	fmt.Fprintf(w, "Draft: %s\n  action %s in %s %s (suggested save: %s)\n", res.DraftDir, d.Action, kind, d.TargetAutomation, d.SaveAs)
	for _, is := range d.Lint {
		fmt.Fprintf(w, "  %s %s %s: %s\n", is.Severity, is.StepID, is.Code, is.Message)
	}
	fmt.Fprintf(w, "Next: monoagentcli record verify %s\n", res.DraftDir)
}

func newRecordVerifyCmd(d Deps) *cobra.Command {
	var full, keepOpen bool
	var inputs []string
	var inputsFile string
	cmd := &cobra.Command{
		Use:   "verify <draft>",
		Short: "Replay a draft in the browser (stops before the first side-effect step unless --full)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveDraftArg(args[0])
			if err != nil {
				return err
			}
			draft, err := recordanalyze.ReadDraft(dir)
			if err != nil {
				return err
			}
			secrets, release := d.SecretLookup(cmd.Context(), draft.TargetAutomation)
			defer release()
			exec, err := d.VerifyExec(vault.ContextWithProfileID(cmd.Context(), d.BrowserProfile()), draft.TargetAutomation, d.Verbose(), keepOpen, secrets)
			if err != nil {
				return err
			}
			overrides := map[string]any{}
			if inputsFile != "" {
				fromFile, err := recordanalyze.ReadInputsFile(inputsFile)
				if err != nil {
					return err
				}
				overrides = fromFile
			}
			for _, kv := range inputs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("--input wants name=value")
				}
				overrides[k] = v // --input wins over --inputs-file
			}
			rep, err := recordanalyze.Verify(cmd.Context(), dir, recordanalyze.VerifyOptions{Full: full, Exec: exec, Inputs: overrides, SecretLookup: secrets})
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				if err := d.WriteJSON(cmd.OutOrStdout(), rep); err != nil {
					return err
				}
			} else {
				for _, s := range rep.Steps {
					fmt.Fprintf(cmd.OutOrStdout(), "  %-28s %-14s %s %s\n", s.Status, s.ID, s.Type, s.Message)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "ok: %v\n", rep.OK)
			}
			if !rep.OK {
				// Exit 1 on a failed replay; the report is the output.
				return d.Reported(errors.New("verify failed: see the step results"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "Also run side-effect steps (writes on the site)")
	cmd.Flags().BoolVar(&keepOpen, "keep-open", false, "Leave the replay tab open (it stays open anyway when safe mode outlines the element it stopped before)")
	cmd.Flags().Bool("safe", true, "Stop before the first side-effect step (default)")
	cmd.Flags().StringVar(&inputsFile, "inputs-file", "", "JSON object {name: value} of inputs (mode 0600, owned by you; values are never echoed)")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "Input value name=value (repeatable; secrets are never recorded)")
	return cmd
}

func newRecordSaveCmd(d Deps) *cobra.Command {
	var as, target, newID, name string
	var force, keepSelectors bool
	var renames []string
	cmd := &cobra.Command{
		Use:   "save <draft>",
		Short: "Install a draft as an action, a fragment, or a workflow of actions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveDraftArg(args[0])
			if err != nil {
				return err
			}
			if err := applyRecordScope(d); err != nil {
				return err
			}
			ren := map[string]string{}
			for _, kv := range renames {
				old, nw, ok := strings.Cut(kv, "=")
				if !ok || old == "" || nw == "" {
					return fmt.Errorf("--rename-input wants old=new, got %q", kv)
				}
				ren[old] = nw
			}
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			res, err := recordanalyze.Save(cmd.Context(), reg, dir, recordanalyze.SaveOptions{
				As: as, Automation: target, New: newID, Name: name, RenameInputs: ren, Force: force,
				KeepPackageSelectors: keepSelectors,
				CreateWorkflow:       recordWorkflowCreator(d),
				LinkRecording:        linkRecording,
			})
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s into %s %s", res.Action, res.Automation, res.Version)
			if res.NodeType != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (node %s)", res.NodeType)
			}
			if res.WorkflowID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), ", workflow %s", res.WorkflowID)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			for _, w := range res.Warnings {
				fmt.Fprintln(cmd.OutOrStdout(), "  warning:", w)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&as, "as", recordanalyze.SaveAsAction, "action | fragment | workflow")
	cmd.Flags().StringVar(&target, "automation", "", "Save into this automation (default: the draft's)")
	cmd.Flags().StringVar(&newID, "new", "", "Save into a new automation with this id")
	cmd.Flags().StringVar(&name, "name", "", "Action or fragment name (default: the AI's)")
	cmd.Flags().BoolVar(&keepSelectors, "keep-package-selectors", false, "On selector conflicts with the target automation, keep the automation's current selectors")
	cmd.Flags().BoolVar(&force, "force", false, "Save even when the draft's lint has errors")
	cmd.Flags().StringArrayVar(&renames, "rename-input", nil, "Rename an input old=new, including its {{old}} uses (repeatable)")
	return cmd
}

// linkRecording marks the recording with the automation it was saved into.
func linkRecording(recordingID, automationID string) error {
	dir, err := recording.Find(recordingID)
	if err != nil {
		return err
	}
	return recording.SetAutomation(dir, automationID)
}

// recordWorkflowCreator stores trigger.manual → node… through the same
// store `workflow create/import` use.
func recordWorkflowCreator(d Deps) recordanalyze.WorkflowCreator {
	return func(ctx context.Context, name, description string, nodes []recordanalyze.WorkflowNode) (string, error) {
		db, err := d.OpenDB()
		if err != nil {
			return "", fmt.Errorf("open database: %w", err)
		}
		defer db.Close()
		wf := buildRecordedWorkflow(name, description, d.ProfileID(), nodes)
		if err := workflow.ValidateConnectionsAgainstNodes(wf.Nodes, wf.Connections); err != nil {
			return "", err
		}
		// Persist exactly as `workflow import` does (workflow row, then nodes
		// and connections), so SQLite readers such as `workflow run --json`
		// see the node types too.
		if err := d.StoreWorkflow(ctx, db, wf); err != nil {
			return "", err
		}
		return wf.ID, nil
	}
}

func buildRecordedWorkflow(name, description, profileID string, nodes []recordanalyze.WorkflowNode) *workflow.Workflow {
	now := time.Now().UTC()
	wf := &workflow.Workflow{ID: uuid.New().String(), Name: name, Description: description,
		Version: 1, ProfileID: profileID, CreatedAt: now, UpdatedAt: now}
	prev := uuid.New().String()
	wf.Nodes = append(wf.Nodes, workflow.WorkflowNode{ID: prev, WorkflowID: wf.ID, Type: "trigger.manual",
		Name: "Start", Config: map[string]interface{}{}, PositionX: 100, PositionY: 200, CreatedAt: now, UpdatedAt: now})
	for i, n := range nodes {
		id := uuid.New().String()
		cfgMap := n.Config
		if cfgMap == nil {
			cfgMap = map[string]interface{}{}
		}
		wf.Nodes = append(wf.Nodes, workflow.WorkflowNode{ID: id, WorkflowID: wf.ID, Type: n.Type, Name: n.Name,
			Config: cfgMap, PositionX: float64(350 + 250*i), PositionY: 200, CreatedAt: now, UpdatedAt: now})
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{ID: uuid.New().String(), WorkflowID: wf.ID,
			SourceNodeID: prev, SourceHandle: "main", TargetNodeID: id, TargetHandle: "main", Position: i})
		prev = id
	}
	return wf
}
