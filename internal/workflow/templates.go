package workflow

import (
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

//go:embed templates/*.json
var templateFS embed.FS

// Template describes a bundled, ready-to-use workflow definition that a user
// can instantiate with one command/click instead of building it node-by-node.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Summary is one plain sentence for app users: what the template does,
	// with no CLI syntax. Description stays the CLI-oriented text.
	Summary string   `json:"summary,omitempty"`
	Inputs  []string `json:"inputs"` // trigger-data keys the template reads, e.g. ["prompts"]
}

// templateMeta holds the template-only keys of a bundled template file —
// keys a saved workflow does not have, so WorkflowFile does not carry them.
type templateMeta struct {
	Summary string `json:"summary"`
	// Order pins a template ahead of the alphabetical rest: templates with
	// an order > 0 are listed first, lowest order first.
	Order int `json:"order"`
}

// triggerInputPattern matches references to trigger data in node configs —
// {{ $json.prompt }} and the ($node["Trigger"].json.prompts) form the
// multi-node templates use.
var triggerInputPattern = regexp.MustCompile(`\$(?:json|node\[[^\]]*\])\.json\.([A-Za-z_][A-Za-z0-9_]*)|\$json\.([A-Za-z_][A-Za-z0-9_]*)`)

// templateInputs returns the trigger-data keys a template reads, sorted and
// deduplicated. These are exactly the keys that belong in
// `workflow templates run <id> --input '{...}'`, derived from the template
// itself so the docs can never drift from the definition.
//
// $json is the trigger data only in a node the trigger feeds directly; further
// down it is the previous node's output (e.g. an HTTP response's "body"), so
// those nodes count only through the $node["<trigger>"] form.
func templateInputs(wf WorkflowFile) []string {
	triggers := map[string]bool{}
	for _, n := range wf.Nodes {
		if strings.HasPrefix(n.Type, "trigger.") {
			triggers[n.ID] = true
			triggers[n.Name] = true
		}
	}
	fedByTrigger := map[string]bool{}
	for _, c := range wf.Connections {
		if triggers[c.Source] {
			fedByTrigger[c.Target] = true
		}
	}
	seen := map[string]bool{}
	for _, n := range wf.Nodes {
		raw, err := json.Marshal(n.Config)
		if err != nil {
			continue
		}
		for _, m := range triggerInputPattern.FindAllStringSubmatch(string(raw), -1) {
			switch {
			case m[1] != "" && triggers[nodeRefName(m[0])]:
				seen[m[1]] = true
			case m[2] != "" && fedByTrigger[n.ID]:
				seen[m[2]] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nodeRefPattern extracts the node name from a $node["Name"] reference as it
// appears in JSON-encoded config (quotes escaped or not).
var nodeRefPattern = regexp.MustCompile(`\$node\[\s*\\?["']([^"'\\]*)\\?["']\s*\]`)

// nodeRefName returns the node name a $node["Name"].json.x reference names,
// or "" when ref is not of that form.
func nodeRefName(ref string) string {
	m := nodeRefPattern.FindStringSubmatch(ref)
	if m == nil {
		return ""
	}
	return m[1]
}

var templateFiles, templateMetas = loadTemplateFiles()

func loadTemplateFiles() (map[string]WorkflowFile, map[string]templateMeta) {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil, nil
	}
	out := make(map[string]WorkflowFile, len(entries))
	metas := make(map[string]templateMeta, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			continue
		}
		var wf WorkflowFile
		if err := json.Unmarshal(data, &wf); err != nil {
			continue
		}
		var meta templateMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		out[id] = wf
		metas[id] = meta
	}
	return out, metas
}

// ListTemplates returns metadata for all bundled workflow templates: those
// with an explicit order first (lowest first), then the rest by name.
func ListTemplates() []Template {
	out := make([]Template, 0, len(templateFiles))
	for id, wf := range templateFiles {
		out = append(out, Template{
			ID:          id,
			Name:        wf.Name,
			Description: wf.Description,
			Summary:     templateMetas[id].Summary,
			Inputs:      templateInputs(wf),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		oi, oj := templateMetas[out[i].ID].Order, templateMetas[out[j].ID].Order
		if (oi > 0) != (oj > 0) {
			return oi > 0
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// GetTemplate returns the workflow definition for a bundled template, keyed by
// the ID returned from ListTemplates. Returns false if templateID is unknown.
// The returned WorkflowFile has no ID/ProfileID/timestamps set — callers
// instantiate it into a real workflow via their own save path (fresh node
// IDs, profile_id, etc.), matching how `workflow import` already turns a
// WorkflowFile into a saved Workflow.
//
// The returned value is a deep copy of the cached template: templateFiles is
// a package-level, process-lifetime cache, and WorkflowNode.Config is a
// map[string]interface{} — returning the cached struct by value still
// aliases that nested map. Real call sites mutate a node's Config in place
// (e.g. stamping their own profile_id) before saving, which would otherwise
// permanently corrupt the shared cache for every future caller/profile and
// race under concurrent instantiation.
func GetTemplate(templateID string) (WorkflowFile, bool) {
	wf, ok := templateFiles[templateID]
	if !ok {
		return WorkflowFile{}, false
	}
	cp, err := deepCopyWorkflowFile(wf)
	if err != nil {
		// Should be unreachable: templateFiles was itself decoded from JSON,
		// so it is always re-encodable. Fall back to the (aliasing) value
		// rather than failing the caller outright.
		return wf, true
	}
	return cp, true
}

// deepCopyWorkflowFile returns an independent copy of wf via a JSON
// marshal/unmarshal round trip, so nested mutable fields (node Config maps,
// Schema pointers) don't alias the original.
func deepCopyWorkflowFile(wf WorkflowFile) (WorkflowFile, error) {
	data, err := json.Marshal(wf)
	if err != nil {
		return WorkflowFile{}, err
	}
	var cp WorkflowFile
	if err := json.Unmarshal(data, &cp); err != nil {
		return WorkflowFile{}, err
	}
	return cp, nil
}
