package main

import "testing"

// argsWantJSON stops at the "--" terminator and reads --json=<bool> like cobra does.
func TestArgsWantJSONStopsAtTerminatorAndParsesBools(t *testing.T) {
	t.Parallel()
	for args, want := range map[string]bool{
		"workflow run x -- --json": false,
		"--json=1 workflow list":   true,
		"--json=t workflow list":   true,
		"--json=false list":        false,
		"--json=0 list":            false,
		"--json list":              true,
		"list --json -- other":     true,
	} {
		if got := argsWantJSON(splitFields(args)); got != want {
			t.Errorf("argsWantJSON(%q) = %v, want %v", args, got, want)
		}
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
