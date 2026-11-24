package workflow

import (
	"fmt"
	"testing"
)

// r01-r01-8: the parsed-template cache is bounded.
func TestExpressionCacheIsBounded(t *testing.T) {
	e := NewExpressionEngine()
	for i := 0; i < maxCachedTemplates+500; i++ {
		if _, err := e.parse(fmt.Sprintf("{{ .json.k%d }}", i)); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	e.cache.Range(func(_, _ any) bool { n++; return true })
	if n > maxCachedTemplates {
		t.Fatalf("cache holds %d templates, cap is %d", n, maxCachedTemplates)
	}
	if _, err := e.parse("{{ .json.late }}"); err != nil {
		t.Fatalf("a source past the cap must still parse: %v", err)
	}
}
