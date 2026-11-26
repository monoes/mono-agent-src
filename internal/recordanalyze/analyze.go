package recordanalyze

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/recording"
)

// Recording is a loaded recording envelope.
type Recording struct {
	Dir      string
	Summary  *recording.Summary
	Events   []recording.Event
	Snippets map[string]string // event id → dom-<id>.html
	Net      []recording.NetEntry
}

// LoadRecording reads an envelope through the recording package.
func LoadRecording(dir string) (*Recording, error) {
	sum, events, err := recording.Load(dir)
	if err != nil {
		return nil, err
	}
	r := &Recording{Dir: dir, Summary: sum, Events: events, Snippets: map[string]string{}}
	for _, ev := range events {
		if s, err := recording.DOMSnippet(dir, ev.ID); err == nil && s != "" {
			r.Snippets[ev.ID] = s
		}
	}
	r.Net = readNet(filepath.Join(dir, recording.NetworkArtifact))
	return r, nil
}

func readNet(path string) []recording.NetEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []recording.NetEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e recording.NetEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// PackageLookup is the part of *automation.Registry analyze needs.
type PackageLookup interface {
	Get(id string) (*automation.Package, error)
	List(includeRemoved bool) ([]automation.InstalledInfo, error)
}

// AnalyzeOptions controls Analyze.
type AnalyzeOptions struct {
	Home     string // ~/.monoagent; drafts go to <Home>/recording-drafts/<id>
	Target   string // existing automation to add to ("" = new automation)
	Runner   Runner
	Registry PackageLookup // may be nil (no target, no collision check)
	// AllowAdvanced lets the draft use AdvancedSteps.
	AllowAdvanced bool
}

// Result is `record analyze --json`.
type Result struct {
	DraftDir string     `json:"draftDir"`
	Draft    *DraftView `json:"draft"`
	// SandboxStatus is set when a drafting turn ran without a sandbox.
	SandboxStatus string `json:"sandboxStatus,omitempty"`
}

// Analyze runs normalize → detect → AI draft → lint and writes the draft.
func Analyze(ctx context.Context, rec *Recording, opts AnalyzeOptions) (*Result, error) {
	if opts.Runner == nil {
		return nil, errors.New("no AI runner")
	}
	if len(rec.Events) == 0 {
		return nil, errors.New("the recording has no events")
	}
	norm := Normalize(rec.Summary, rec.Events)
	norm.Net = rec.Net
	if norm.RecordingID == "" {
		norm.RecordingID = filepath.Base(rec.Dir)
	}
	a := Detect(norm)
	env := &Env{Analysis: a, AllowAdvanced: opts.AllowAdvanced}
	if opts.Target != "" {
		if opts.Registry == nil {
			return nil, errors.New("no automation registry to look up " + opts.Target)
		}
		p, err := opts.Registry.Get(opts.Target)
		if err != nil {
			return nil, fmt.Errorf("automation %s: %w", opts.Target, err)
		}
		if env.Target, err = LoadExisting(p); err != nil {
			return nil, fmt.Errorf("automation %s: %w", opts.Target, err)
		}
	}
	if opts.Registry != nil {
		infos, err := opts.Registry.List(true)
		if err == nil {
			ids := map[string]bool{}
			for _, in := range infos {
				ids[in.ID] = true
			}
			env.AutomationTaken = func(id string) bool { return ids[id] }
		}
	}
	prompt, err := BuildPrompt(a, env, rec.Snippets)
	if err != nil {
		return nil, err
	}
	turns, sandbox := monomind.WatchSandbox(ctx)
	out, err := Generate(turns, opts.Runner, prompt, env)
	if err != nil {
		return nil, err
	}
	stampProvenance(out, norm.RecordingID, a.Goal)
	enforced := EnforceSideEffects(out, env)
	m := BuildManifest(out, env)
	d := &Draft{
		RecordingID:      norm.RecordingID,
		RecordingDir:     rec.Dir,
		TargetAutomation: out.Automation.ID,
		IsNew:            env.Target == nil,
		Action:           out.Action.ActionType,
		SaveAs:           suggestSaveAs(a),
		Names:            DraftNames{Automation: out.Names["automation"], Action: out.Names["action"], Fragment: out.Names["fragment"]},
		Lint:             append(enforced, Lint(out, env, m, rec.Snippets)...),
		Segments:         draftSegments(a, out.Action.ActionType),
		Goal:             a.Goal,
		RecordedInputs:   recordedInputs(a),
		AllowAdvanced:    opts.AllowAdvanced,
	}
	for _, f := range out.Fragments {
		d.NewFragments = append(d.NewFragments, f.Name)
	}
	dir := filepath.Join(DraftsRoot(opts.Home), safeDirName(norm.RecordingID))
	if err := WriteDraft(dir, out, env, m, d); err != nil {
		return nil, err
	}
	view, err := LoadDraftView(dir)
	if err != nil {
		return nil, err
	}
	return &Result{SandboxStatus: sandbox.Missing(), DraftDir: dir, Draft: view}, nil
}

func stampProvenance(out *Output, recID, goal string) {
	if out.Action.Provenance == nil {
		out.Action.Provenance = map[string]interface{}{}
	}
	out.Action.Provenance["recording"] = recID
	out.Action.Provenance["generator"] = "record-analyze/1"
	if goal != "" {
		out.Action.Provenance["goal"] = goal
	}
}

// suggestSaveAs proposes a workflow when the recording spans several sites.
func suggestSaveAs(a *Analysis) string {
	hosts := map[string]bool{}
	for _, s := range a.Segments {
		if s.Host != "" {
			hosts[s.Host] = true
		}
	}
	if len(hosts) > 1 {
		return SaveAsWorkflow
	}
	return SaveAsAction
}

func draftSegments(a *Analysis, actionName string) []DraftSegment {
	out := []DraftSegment{}
	for _, s := range a.Segments {
		out = append(out, DraftSegment{From: s.From, To: s.To, Action: actionName})
	}
	return out
}

// recordedInputs are the values verify replays with (never secrets).
func recordedInputs(a *Analysis) map[string]any {
	m := map[string]any{}
	for _, in := range a.Inputs {
		if in.Type != "secret" && in.Default != "" {
			m[in.Name] = in.Default
		}
	}
	return m
}

func safeDirName(id string) string {
	if id == "" || id == "." || id == ".." {
		return "recording"
	}
	return filepath.Base(filepath.Clean("/" + id))
}
