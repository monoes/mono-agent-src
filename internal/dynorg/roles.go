package dynorg

import (
	"slices"
	"strings"
)

// builtinRole is a role used when monomind's agent registry offers nothing
// for a brief (a folder without `.monomind/registry.json`, or no match).
type builtinRole struct {
	ID, Title, Category, Profile, Body string
	keywords                           []string
}

var coreRoles = []builtinRole{
	{ID: "reviewer", Title: "Reviewer", Category: "review", Profile: ProfileResearch,
		keywords: []string{"review", "audit", "check", "inspect", "critique", "security"},
		Body:     "You review code and changes. Read carefully, find real defects (bugs, races, security holes, missing error handling), and report each with file:line, why it is wrong and a concrete fix. Do not edit files."},
	{ID: "researcher", Title: "Researcher", Category: "research", Profile: ProfileResearch,
		keywords: []string{"research", "find", "investigate", "explore", "look up", "compare", "document", "explain", "understand", "where"},
		Body:     "You investigate and report. Read the code, docs and the web as needed, and answer the brief with facts and their sources (file:line or URL). Do not edit files."},
	{ID: "tester", Title: "Tester", Category: "testing", Profile: ProfileCoding,
		keywords: []string{"test", "qa", "verify", "reproduce", "coverage", "e2e"},
		Body:     "You test. Write or run tests that prove the behavior the brief asks about, reproduce failures, and report exactly what passed and failed with the commands you ran."},
	{ID: "planner", Title: "Planner", Category: "planning", Profile: ProfileResearch,
		keywords: []string{"plan", "design", "architecture", "break down", "spec"},
		Body:     "You plan. Read the relevant code, then return a short, ordered plan with the files to change and the risks. Do not edit files."},
	{ID: "coder", Title: "Coder", Category: "engineering", Profile: ProfileCoding,
		keywords: []string{"implement", "fix", "refactor", "build", "add ", "write code", "change"},
		Body:     "You implement. Make the change the brief asks for with the smallest correct diff, following the code around it, and run the relevant tests."},
}

// builtinRoles is the fallback roster: the core roles, then the app-aware
// ones (roles_app.go).
var builtinRoles = append(append([]builtinRole{}, coreRoles...), appRoles...)

func (r builtinRole) hits(brief string) int {
	b, n := strings.ToLower(brief), 0
	for _, k := range r.keywords {
		if strings.Contains(b, k) {
			n++
		}
	}
	return n
}

// builtinFor picks the built-in role whose keywords the brief mentions
// most (an app role wins a tie, then the first listed); the coder when
// none match.
func builtinFor(brief string) builtinRole {
	best, bestHits := coreRoles[len(coreRoles)-1], 0
	for _, r := range slices.Concat(appRoles, coreRoles) { // the specific app roles win ties
		if n := r.hits(brief); n > bestHits {
			best, bestHits = r, n
		}
	}
	return best
}

// appRoleFor is the app-aware built-in role the brief clearly names (at
// least one keyword), for surfaces monomind has no agent for.
func appRoleFor(brief string) (builtinRole, bool) {
	best, bestHits := builtinRole{}, 0
	for _, r := range appRoles {
		if n := r.hits(brief); n > bestHits {
			best, bestHits = r, n
		}
	}
	return best, bestHits > 0
}

// appRolesNamed is every app-aware role the brief names, best match first:
// a brief that spans surfaces (tasks and documents) offers all of them.
func appRolesNamed(brief string) []builtinRole {
	var out []builtinRole
	for _, r := range appRoles {
		if r.hits(brief) > 0 {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b builtinRole) int { return b.hits(brief) - a.hits(brief) })
	return out
}

// setBuiltin staffs a worker with a built-in role.
func (st *Staff) setBuiltin(r builtinRole, why string) {
	st.Role, st.AgentType, st.Category, st.AgentBody = r.Title, r.ID, r.Category, r.Body
	st.Builtin = true
	st.Access = r.Profile
	st.Why = append(st.Why, why)
}

func builtinByID(id string) (builtinRole, bool) {
	for _, r := range builtinRoles {
		if strings.EqualFold(r.ID, id) || strings.EqualFold(r.Title, id) {
			return r, true
		}
	}
	return builtinRole{}, false
}

// profileForCategory is the default access profile for an agent category.
func profileForCategory(category, id string) string {
	s := strings.ToLower(category + " " + id)
	switch {
	case containsAny(s, "automation", "workflow", "browser-operator", "task-manager", "people-", "vault"):
		return ProfileWorkspace
	case containsAny(s, "qa", "testing", "tester", "e2e", "browser", "accessibility", "evidence"):
		return ProfileQA
	case containsAny(s, "research", "review", "audit", "analysis", "planning", "planner", "architect", "docs", "writer"):
		return ProfileResearch
	}
	return ProfileCoding
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
