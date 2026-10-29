// Package agentpurposes holds what the operator chooses for each kind of work agents do: which
// (system, model) pairs a parent may pick from when it starts an agent for that purpose, and how.
// The purposes map onto the role categories the staffing already uses (internal/dynorg), so
// nothing here is a parallel list. The choice is enforced in the spawn path (dynorg.Staffer), not
// by a prompt. A purpose with nothing chosen uses every validated pair, as before.
package agentpurposes

import "strings"

// Info is one purpose as the settings pane shows it.
type Info struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Categories are the role categories (dynorg) that count as this purpose: an exact match or
	// a category that contains one of them. A purpose with none is only used when the lead names it.
	Categories []string `json:"categories"`
}

// Catalog is the list of purposes, in the order the pane shows them.
var Catalog = []Info{
	{"coding", "Coding", "Writing and changing code.", []string{"engineering", "development", "implement", "backend", "frontend", "devops", "architecture"}},
	{"debugging", "Debugging", "Finding why something fails.", []string{"debug"}},
	{"researching", "Researching (web)", "Looking things up in the code, the docs and on the web.", []string{"research"}},
	{"deep-research", "Deep research", "Long, thorough research that compares sources.", nil},
	{"planning", "Planning", "Breaking work down and ordering it.", []string{"planning"}},
	{"strategy", "Strategy and ideation", "Options, trade-offs and ideas.", []string{"strategy", "ideation", "product"}},
	{"reviewing", "Reviewing (code)", "Reading changes for defects.", []string{"review"}},
	{"testing", "Verifying and testing", "Proving that work does what it should.", []string{"testing", "qa"}},
	{"security-audit", "Security audit", "Looking for security holes and unsafe handling of secrets.", []string{"security"}},
	{"writing", "Writing and docs", "Documentation, text and messages.", []string{"docs", "writing", "content"}},
	{"data-analysis", "Data and analysis", "Reading data and reporting what it says.", []string{"analysis", "data"}},
	{"automation", "Automation and workflows", "Building and running automations and workflows.", []string{"automation", "operations", "workflow"}},
	{"browser", "Operating a browser", "Driving a web page.", []string{"browser"}},
	{"design", "Design", "Interfaces and visual design.", []string{"design", "ux"}},
	{"summarizing", "Summarizing and triage", "Cheap, fast reading and sorting.", []string{"summar", "triage"}},
	{"judging", "Judging and evaluating", "The arbiter: deciding whether work is good enough.", []string{"judg", "evaluat", "arbiter"}},
	{"orchestrating", "Orchestrating", "The lead that plans and hands out work.", []string{"orchestrat", "lead"}},
	{"translation", "Translation", "Translating text.", []string{"translat", "locali"}},
}

// Find returns the purpose with this id.
func Find(id string) (Info, bool) {
	for _, p := range Catalog {
		if p.ID == id {
			return p, true
		}
	}
	return Info{}, false
}

// ForCategory returns the purpose a role category counts as, if any: the first in the catalog
// whose categories the category equals or contains. "" and "general" are no purpose.
func ForCategory(category string) (Info, bool) {
	c := strings.ToLower(strings.TrimSpace(category))
	if c == "" || c == "general" {
		return Info{}, false
	}
	for _, p := range Catalog {
		for _, k := range p.Categories {
			if c == k || strings.Contains(c, k) {
				return p, true
			}
		}
	}
	return Info{}, false
}
