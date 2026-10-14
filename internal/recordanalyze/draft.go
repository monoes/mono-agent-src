package recordanalyze

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/statedir"
)

// Save targets.
const (
	SaveAsAction   = "action"
	SaveAsFragment = "fragment"
	SaveAsWorkflow = "workflow"
)

// DraftFile is the draft metadata file next to the package files.
const DraftFile = "draft.json"

// Draft is draft.json (contracts §5), plus the recorded input values
// verify replays with.
type Draft struct {
	RecordingID      string                 `json:"recordingId"`
	RecordingDir     string                 `json:"recordingDir,omitempty"`
	TargetAutomation string                 `json:"targetAutomation"`
	IsNew            bool                   `json:"isNew"`
	Action           string                 `json:"action"`
	SaveAs           string                 `json:"saveAs"`
	Names            DraftNames             `json:"names"`
	Lint             []automation.IssueJSON `json:"lint"`
	Segments         []DraftSegment         `json:"segments"`
	CreatedAt        string                 `json:"createdAt"`
	Goal             string                 `json:"goal,omitempty"`
	RecordedInputs   map[string]any         `json:"recordedInputs,omitempty"`
	// NewFragments are the fragments this draft adds (not the target's).
	NewFragments []string `json:"newFragments,omitempty"`
	// AllowAdvanced records `record analyze --allow-advanced`.
	AllowAdvanced bool `json:"allowAdvanced,omitempty"`
}

type DraftNames struct {
	Automation string `json:"automation"`
	Action     string `json:"action"`
	Fragment   string `json:"fragment"`
}

type DraftSegment struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"`
}

// DraftsRoot is <home>/recording-drafts (home is normally ~/.monoagent).
func DraftsRoot(home string) string { return filepath.Join(home, "recording-drafts") }

// BuildManifest makes the draft package's manifest: the target's manifest
// widened with what the draft uses, or a new one from the recording.
func BuildManifest(out *Output, env *Env) *automation.Manifest {
	used := stepTypes(out)
	if !env.AllowAdvanced {
		// Never auto-grant advanced step types (lint reports their use).
		kept := used[:0]
		for _, t := range used {
			if !AdvancedSteps[t] {
				kept = append(kept, t)
			}
		}
		used = kept
	}
	scripts := sortedKeys(out.Scripts)
	var domains []string
	start := ""
	if env.Analysis != nil {
		domains = env.Analysis.Domains
		start = env.Analysis.ActionFrom
	}
	if env.Target != nil {
		m := env.Target.Manifest
		m.Actions = []string{out.Action.ActionType}
		if len(m.Permissions.Steps) > 0 {
			m.Permissions.Steps = union(m.Permissions.Steps, used)
		}
		m.Permissions.Scripts = union(m.Permissions.Scripts, scripts)
		if len(m.Site.Domains) > 0 {
			m.Site.Domains = union(m.Site.Domains, domains)
		}
		return &m
	}
	m := &automation.Manifest{
		Schema:      automation.SchemaV1,
		ID:          out.Automation.ID,
		Name:        out.Automation.Name,
		Version:     "0.1.0",
		Description: out.Automation.Description,
		Site:        automation.Site{StartURL: start, Domains: append([]string(nil), domains...)},
		Permissions: automation.Permissions{Steps: used, Scripts: scripts},
		Actions:     []string{out.Action.ActionType},
		Policy:      automation.Policy{Tier: "standard"},
	}
	if m.Site.StartURL == "" && out.Automation.Site != nil {
		m.Site.StartURL = out.Automation.Site.StartURL
	}
	if m.Permissions.Scripts == nil {
		m.Permissions.Scripts = []string{}
	}
	switch {
	case out.Automation.Login != nil:
		l := *out.Automation.Login
		if l.URL == "" && env.Analysis != nil && env.Analysis.Login != nil {
			l.URL = env.Analysis.Login.URL
		}
		m.Login = &l
	case env.Analysis != nil && env.Analysis.Login != nil:
		m.Login = &automation.Login{URL: env.Analysis.Login.URL, SessionTTLDays: 30}
	}
	if m.Login != nil && m.Login.SessionTTLDays == 0 {
		m.Login.SessionTTLDays = 30
	}
	return m
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// closure copies into the draft the target's selectors, fragments and
// scripts the draft references, so the draft is a self-contained package.
func closure(out *Output, env *Env) (map[string]action.SelectorEntry, []action.FragmentDef, map[string]string) {
	sels := map[string]action.SelectorEntry{}
	for k, v := range out.Selectors {
		sels[k] = v
	}
	frags := append([]action.FragmentDef(nil), out.Fragments...)
	scripts := map[string]string{}
	for k, v := range out.Scripts {
		scripts[k] = v
	}
	if env.Target == nil {
		return sels, frags, scripts
	}
	have := map[string]bool{}
	for _, f := range frags {
		have[f.Name] = true
	}
	var visit func(steps []action.StepDef)
	visit = func(steps []action.StepDef) {
		walkSteps(steps, func(s *action.StepDef) {
			if _, ok := sels[s.ConfigKey]; s.ConfigKey != "" && !ok {
				if e, ok := env.Target.Selectors[s.ConfigKey]; ok {
					sels[s.ConfigKey] = e
				}
			}
			if _, ok := scripts[s.Script]; s.Script != "" && !ok {
				if src, ok := env.Target.Scripts[s.Script]; ok {
					scripts[s.Script] = src
				}
			}
			if s.Type == "call_fragment" && !have[s.Fragment] {
				if f, ok := env.Target.Fragments[s.Fragment]; ok {
					have[s.Fragment] = true
					frags = append(frags, *f)
					visit(f.Steps)
				}
			}
		})
	}
	visit(out.Action.Steps)
	for _, f := range out.Fragments {
		visit(f.Steps)
	}
	return sels, frags, scripts
}

// WriteDraft (re)creates dir as a package plus draft.json.
func WriteDraft(dir string, out *Output, env *Env, m *automation.Manifest, d *Draft) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	sels, frags, scripts := closure(out, env)
	if d.CreatedAt == "" {
		d.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if d.Lint == nil {
		d.Lint = []automation.IssueJSON{}
	}
	files := map[string]any{
		"automation.json": m,
		"actions/" + out.Action.ActionType + ".json": &out.Action,
		"selectors.json": sels,
		DraftFile:        d,
	}
	for i := range frags {
		files["fragments/"+frags[i].Name+".json"] = &frags[i]
	}
	for name, v := range files {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("encode %s: %w", name, err)
		}
		if err := writeFile(filepath.Join(dir, name), append(b, '\n')); err != nil {
			return err
		}
	}
	for name, src := range scripts {
		if err := writeFile(filepath.Join(dir, "scripts", filepath.Base(name)), []byte(src)); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, b []byte) error {
	return statedir.WriteFile(path, b)
}

// ReadDraft reads <dir>/draft.json.
func ReadDraft(dir string) (*Draft, error) {
	b, err := os.ReadFile(filepath.Join(dir, DraftFile))
	if err != nil {
		return nil, fmt.Errorf("not a recording draft (%s): %w", dir, err)
	}
	var d Draft
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", DraftFile, err)
	}
	return &d, nil
}
