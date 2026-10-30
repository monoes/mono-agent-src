package agentpurposes

import "strings"

// Purpose is a kind of work agents do. Category is the dynamic org's role
// category (internal/dynorg roles) that maps onto it: a worker whose role has
// that category is of this purpose unless its parent names another.
type Purpose struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// builtins is the list the Settings pane shows. The categories of the first
// block are the ones the built-in roles already carry (dynorg/roles.go,
// roles_app.go); the rest are new to the same table.
var builtins = []Purpose{
	{"coding", "Coding", "engineering", "Writing and changing code."},
	{"debugging", "Debugging", "debug", "Finding why something fails and fixing it."},
	{"research", "Researching", "research", "Looking things up in the code, the documents and the web."},
	{"deep-research", "Deep research", "deep-research", "Long, multi-source research with a written report."},
	{"planning", "Planning", "planning", "Breaking work into steps and choosing an approach."},
	{"strategy", "Strategy and ideation", "strategy", "Proposing directions and weighing them."},
	{"review", "Reviewing", "review", "Reviewing code and changes for defects."},
	{"testing", "Verifying and testing", "testing", "Writing and running tests, checking results."},
	{"security-audit", "Security audit", "security", "Looking for security defects and exposure."},
	{"writing", "Writing and docs", "docs", "Documents, notes and prose."},
	{"data", "Data and analysis", "analysis", "Working with data and reporting numbers."},
	{"automation", "Automation and workflows", "automation", "Building and running workflows and automations."},
	{"browsing", "Operating a browser", "browsing", "Driving a web page step by step."},
	{"design", "Design", "design", "Interface and visual design."},
	{"triage", "Summarizing and triage", "triage", "Cheap, fast summaries and sorting."},
	{"judging", "Judging and evaluating", "judging", "The arbiter: scoring work and settling disagreements."},
	{"orchestrating", "Orchestrating", "orchestrating", "The lead that plans and hands out work."},
	{"translation", "Translation", "translation", "Translating text between languages."},
}

// Builtins returns the purposes in display order.
func Builtins() []Purpose { return append([]Purpose(nil), builtins...) }

// IDs lists the purpose ids in display order.
func IDs() []string {
	out := make([]string, len(builtins))
	for i, p := range builtins {
		out[i] = p.ID
	}
	return out
}

// Lookup finds a purpose by id.
func Lookup(id string) (Purpose, bool) {
	for _, p := range builtins {
		if p.ID == id {
			return p, true
		}
	}
	return Purpose{}, false
}

// ForCategory is the purpose of a role category: the one whose category it
// equals, else the one whose category the role's category contains (the
// longest such name wins). "" when none fits: that work is not limited.
func ForCategory(category string) string {
	c := strings.ToLower(strings.TrimSpace(category))
	if c == "" {
		return ""
	}
	best, bestLen := "", 0
	for _, p := range builtins {
		switch {
		case p.Category == c:
			return p.ID
		case strings.Contains(c, p.Category) && len(p.Category) > bestLen:
			best, bestLen = p.ID, len(p.Category)
		}
	}
	return best
}
