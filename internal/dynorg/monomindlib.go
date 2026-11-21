package dynorg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/monomind"
)

// MonomindLibrary picks and reads agents and skills through monomind, from
// the chat folder: `monomind pick`, the folder's agent registry
// (.monomind/registry.json, written by `monomind init`), and `monomind org
// skills show`.
type MonomindLibrary struct {
	Bin string
	Cwd string

	once     sync.Once
	registry map[string]registryAgent
}

type registryAgent struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Category string `json:"category"`
	FilePath string `json:"filePath"`
	// Description is the one-line summary org_roster shows.
	Description string `json:"description"`
}

// ListAgents implements AgentLister: the registry's agents by category.
func (l *MonomindLibrary) ListAgents(context.Context) map[string][]AgentInfo {
	l.once.Do(l.loadRegistry)
	out := map[string][]AgentInfo{}
	seen := map[string]bool{}
	for _, a := range l.registry {
		id := orElse(a.Slug, a.Name)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cat := orElse(a.Category, "general")
		out[cat] = append(out[cat], AgentInfo{ID: id, Description: a.Description})
	}
	return out
}

const monomindCallTimeout = 60 * time.Second

// safeName matches agent and skill ids that are safe to put in a path, a
// glob and a monomind argv: no separators, glob characters, "..", or a
// leading "-" that would read as a flag.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidName reports whether an agent or skill id from the lead is safe to
// look up.
func ValidName(name string) bool {
	return safeName.MatchString(name) && !strings.Contains(name, "..")
}

func (l *MonomindLibrary) run(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, monomindCallTimeout)
	defer cancel()
	if err := monomind.CheckOutside(l.Bin, l.Cwd); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(cctx, l.Bin, args...)
	cmd.Dir = l.Cwd
	cmd.Env = monomind.PinEnvIn(monomind.FilteredEnviron(), l.Bin, l.Cwd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("monomind %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

type pickList struct {
	Confident bool `json:"confident"`
	Ranked    []struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Category    string  `json:"category"`
		Description string  `json:"description"`
		Probability float64 `json:"probability"`
	} `json:"ranked"`
}

func (l *MonomindLibrary) pick(ctx context.Context, brief, kind string) ([]Candidate, bool, error) {
	out, err := l.run(ctx, "pick", "-t", brief, "--"+kind, "--top", "5", "--json")
	if err != nil {
		return nil, false, err
	}
	var res map[string]pickList
	if i := bytes.IndexByte(out, '{'); i > 0 {
		out = out[i:]
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, false, fmt.Errorf("monomind pick: unparseable output: %w", err)
	}
	list := res[kind]
	cands := make([]Candidate, 0, len(list.Ranked))
	for _, r := range list.Ranked {
		if r.ID != "" {
			cands = append(cands, Candidate{ID: r.ID, Name: r.Name, Category: r.Category, Description: r.Description, Probability: r.Probability})
		}
	}
	return cands, list.Confident, nil
}

// Agents implements Picker.
func (l *MonomindLibrary) Agents(ctx context.Context, brief string) ([]Candidate, bool, error) {
	return l.pick(ctx, brief, "agents")
}

// Skills implements Picker.
func (l *MonomindLibrary) Skills(ctx context.Context, brief string) ([]Candidate, bool, error) {
	return l.pick(ctx, brief, "skills")
}

func (l *MonomindLibrary) loadRegistry() {
	l.registry = map[string]registryAgent{}
	b, err := os.ReadFile(filepath.Join(l.Cwd, ".monomind", "registry.json"))
	if err != nil {
		return
	}
	var reg struct {
		Agents []registryAgent `json:"agents"`
	}
	if json.Unmarshal(b, &reg) != nil {
		return
	}
	for _, a := range reg.Agents {
		if a.Slug != "" {
			l.registry[strings.ToLower(a.Slug)] = a
		}
		if a.Name != "" {
			l.registry[strings.ToLower(a.Name)] = a
		}
	}
}

// AgentBody implements Library: the agent definition's markdown without
// its front matter, from the folder's registry, else from an agents
// folder (the chat folder's, then the user's).
func (l *MonomindLibrary) AgentBody(_ context.Context, id string) (string, string, string, error) {
	l.once.Do(l.loadRegistry)
	if a, ok := l.registry[strings.ToLower(id)]; ok && a.FilePath != "" && !ValidName(id) {
		// A registry name with spaces ("Code Reviewer") is fine to look up
		// in the map; only the file lookups below need a safe id.
		return l.registryBody(a, id)
	}
	if !ValidName(id) {
		return "", "", "", fmt.Errorf("invalid agent id %q", id)
	}
	if a, ok := l.registry[strings.ToLower(id)]; ok && a.FilePath != "" {
		if body, title, cat, err := l.registryBody(a, id); err == nil {
			return body, title, cat, nil
		}
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{filepath.Join(l.Cwd, ".claude", "agents"), filepath.Join(home, ".claude", "agents")} {
		for _, pattern := range []string{filepath.Join(dir, id+".md"), filepath.Join(dir, "*", id+".md")} {
			matches, _ := filepath.Glob(pattern)
			for _, m := range matches {
				if b, err := os.ReadFile(m); err == nil {
					body, name, cat := parseAgentFile(string(b))
					return body, orElse(name, id), cat, nil
				}
			}
		}
	}
	return "", "", "", fmt.Errorf("no agent %q", id)
}

// registryBody reads a registry agent's file, which must lie inside the
// chat folder when its path is relative.
func (l *MonomindLibrary) registryBody(a registryAgent, id string) (string, string, string, error) {
	p := a.FilePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(l.Cwd, p)
		if rel, err := filepath.Rel(l.Cwd, p); err != nil || strings.HasPrefix(rel, "..") {
			return "", "", "", fmt.Errorf("agent %q's file is outside the folder", id)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", "", "", err
	}
	return stripFrontMatter(string(b)), orElse(a.Name, id), a.Category, nil
}

// SkillText implements Library: `monomind org skills show`, else the
// skill's SKILL.md in the chat folder or the user's skills.
func (l *MonomindLibrary) SkillText(ctx context.Context, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	if out, err := l.run(ctx, "org", "skills", "show", name); err == nil && len(bytes.TrimSpace(out)) > 0 {
		return string(out), nil
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{filepath.Join(l.Cwd, ".claude", "skills"), filepath.Join(home, ".claude", "skills")} {
		if b, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md")); err == nil {
			return stripFrontMatter(string(b)), nil
		}
	}
	return "", fmt.Errorf("no skill %q", name)
}

// stripFrontMatter drops a leading "---" … "---" block.
func stripFrontMatter(s string) string {
	body, _, _ := splitFrontMatter(s)
	return body
}

// parseAgentFile returns an agent file's body and its front matter's name
// and category.
func parseAgentFile(s string) (body, name, category string) {
	body, fm, _ := splitFrontMatter(s)
	for _, line := range strings.Split(fm, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "category":
			category = v
		}
	}
	return body, name, category
}

func splitFrontMatter(s string) (body, frontMatter string, ok bool) {
	t := strings.TrimLeft(strings.TrimPrefix(s, "\uFEFF"), " \t\r\n")
	if !strings.HasPrefix(t, "---") {
		return strings.TrimSpace(s), "", false
	}
	rest := t[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return strings.TrimSpace(s), "", false
	}
	after := rest[end+4:]
	if i := strings.IndexByte(after, '\n'); i >= 0 {
		after = after[i+1:]
	} else {
		after = ""
	}
	return strings.TrimSpace(after), rest[:end], true
}

// JevChooser answers staffing questions with Jev (one choice question per
// call).
type JevChooser struct{ Client *jev.Client }

// Choose implements Chooser.
func (j JevChooser) Choose(ctx context.Context, state any, question string, options map[string]string) (string, float64, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := j.Client.Ask(cctx, state, map[string]jev.Question{
		"choice": {Type: jev.TypeChoice, Criteria: options, Instructions: question},
	})
	if err != nil {
		return "", 0, err
	}
	a, ok := resp.Answers["choice"]
	if !ok {
		return "", 0, fmt.Errorf("jev: no answer")
	}
	choice, p := jev.GateValue(resp, a) // no probability gates as 0: the rule picks (plan D7)
	return choice, p, nil
}
