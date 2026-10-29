package agentpurposes

import (
	"regexp"
	"testing"
)

func TestCatalogIsWellFormed(t *testing.T) {
	kebab := regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`)
	seen := map[string]bool{}
	for _, p := range Catalog {
		if !kebab.MatchString(p.ID) || seen[p.ID] {
			t.Errorf("purpose id %q is not a unique kebab-case id", p.ID)
		}
		seen[p.ID] = true
		if p.Title == "" || p.Description == "" {
			t.Errorf("%s has no title or description", p.ID)
		}
		if got, ok := Find(p.ID); !ok || got.ID != p.ID {
			t.Errorf("Find(%q) = %+v %v", p.ID, got, ok)
		}
	}
	if len(Catalog) < 18 {
		t.Errorf("%d purposes, the plan lists 18", len(Catalog))
	}
	if _, ok := Find("nonsense"); ok {
		t.Error("an unknown purpose was found")
	}
}

// The purposes map onto the role categories the staffing already uses, so nothing is parallel.
func TestPurposeForCategory(t *testing.T) {
	for category, want := range map[string]string{
		"engineering": "coding", "development": "coding", "backend": "coding", "debug": "debugging",
		"research": "researching", "planning": "planning", "review": "reviewing", "testing": "testing",
		"security": "security-audit", "docs": "writing", "analysis": "data-analysis",
		"automation": "automation", "operations": "automation", "Review": "reviewing", " Engineering ": "coding",
	} {
		got, ok := ForCategory(category)
		if !ok || got.ID != want {
			t.Errorf("category %q -> %q (%v), want %q", category, got.ID, ok, want)
		}
	}
	for _, c := range []string{"", "general", "something-unmapped"} {
		if got, ok := ForCategory(c); ok {
			t.Errorf("category %q mapped to %q", c, got.ID)
		}
	}
}
