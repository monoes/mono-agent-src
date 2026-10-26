package coderhost

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"testing"
)

// The approval writers (mission confirmation, settings, budgets, the tree) are
// named only by the files that own them. A new file naming one fails here, so
// the other autonomy-adjacent code cannot reach them either.
func TestApprovalWritersAreNamedOnlyByTheirOwners(t *testing.T) {
	owners := map[string][]string{
		"Confirm": {"missionconfirm.go"}, "ConsumeConfirm": {"missionconfirm.go"},
		"PeekConfirm": {"missionconfirm.go", "host.go"}, "Revoke": {"missionconfirm.go", "host.go"},
		"SaveSettings": {"settings.go", "children_control.go", "children.go"}, "SetUnattended": {"settings.go"},
		"SetBudget": {"children.go"}, "carveBudget": {"children.go"}, "Spawn": {"children.go"},
		"SetRole": {"store.go", "children.go"}, "SetChild": {"children_control.go"},
		"SetCommand": {"store.go", "host.go"},
	}
	for _, file := range nonTestFiles(t, "*.go") {
		ast.Inspect(parse(t, file), func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if files, listed := owners[id.Name]; listed && !oneOf(file, files) {
					t.Errorf("%s names %s, which only %v may", file, id.Name, files)
				}
			}
			return true
		})
	}
}

var insertRows = regexp.MustCompile(`(?i)INSERT\s+(?:OR\s+\w+\s+)?INTO\s+(coder_session_commands|coder_sessions)\b`)

// Rows of the command queue and of sessions are created in store.go only.
func TestCommandAndSessionRowsAreInsertedOnlyByTheStore(t *testing.T) {
	for _, file := range nonTestFiles(t, "*.go") {
		if file == "store.go" {
			continue
		}
		ast.Inspect(parse(t, file), func(n ast.Node) bool {
			if b, ok := n.(*ast.BasicLit); ok && b.Kind == token.STRING {
				v, _ := strconv.Unquote(b.Value)
				if m := insertRows.FindStringSubmatch(v); m != nil {
					t.Errorf("%s inserts into %s; only store.go does", file, m[1])
				}
			}
			return true
		})
	}
}
