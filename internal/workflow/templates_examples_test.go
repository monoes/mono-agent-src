package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// R26-06: examples/ holds copies of these bundled templates; the two sets
// must not drift apart, so a change to one has to be made in the other.
func TestExamplesMatchBundledTemplates(t *testing.T) {
	pairs := map[string]string{
		"data-pipeline":           "data_pipeline",
		"github-issues-to-linear": "github_issues_to_linear",
		"http-to-slack":           "http_to_slack",
		"morning-briefing":        "morning_briefing",
		"sheets-to-gmail":         "sheets_to_gmail",
		"stripe-events-to-sheets": "stripe_events_to_sheets",
		"telegram-notify":         "telegram_notify",
		"webhook-to-postgres":     "webhook_to_postgres",
	}
	read := func(p string) map[string]any {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return m
	}
	for ex, tpl := range pairs {
		e := read(filepath.Join("..", "..", "examples", ex+".json"))
		b := read(filepath.Join("templates", tpl+".json"))
		for _, k := range []string{"nodes", "connections"} {
			if !reflect.DeepEqual(e[k], b[k]) {
				t.Errorf("examples/%s.json and templates/%s.json differ in %s", ex, tpl, k)
			}
		}
	}
}
