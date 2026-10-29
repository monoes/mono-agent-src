package coderhost

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A static guard for the budget request path (phase P-F). Who may file, answer and read a request is decided by
// which code names the functions that do it, so a future change that reaches the answer from anywhere else (a
// tool, a token path, a model-facing package) must change this list on purpose.
//
//   - GrantBudget and DeclineBudget are named only by the operator's CLI (and the store's own file).
//   - RequestBudget is named only by the lead's MCP tool.
//   - The request table is written only by budgetreq.go, and a session's budget is raised only by GrantBudget.
var budgetPathAllowed = map[string][]string{
	"GrantBudget":   {"internal/coderhost/budgetreq.go", "internal/cli/chat/coder_sessions_budget.go"},
	"DeclineBudget": {"internal/coderhost/budgetreq.go", "internal/cli/chat/coder_sessions_budget.go"},
	"RequestBudget": {"internal/coderhost/budgetreq.go", "internal/mcp/mission_budget_tools.go"},
}

func repoGoFiles(t *testing.T) (root string, files []string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".claude", "frontend", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return root, files
}

func TestBudgetAnswerPathIsNamedOnlyWhereItBelongs(t *testing.T) {
	root, files := repoGoFiles(t)
	seen := map[string]bool{}
	for _, rel := range files {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, rel), nil, 0)
		if err != nil {
			continue // a file this guard cannot parse names nothing here (build-tagged or generated)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			allowed, guarded := budgetPathAllowed[id.Name]
			if !guarded {
				return true
			}
			seen[id.Name] = true
			for _, a := range allowed {
				if a == rel {
					return true
				}
			}
			t.Errorf("%s names %s: only %v may (the person's answer is the operator CLI's alone)", rel, id.Name, allowed)
			return true
		})
	}
	for name := range budgetPathAllowed {
		if !seen[name] {
			t.Errorf("the guard saw no use of %s: it is looking in the wrong place", name)
		}
	}
}

func TestBudgetRequestTableAndBudgetRaiseAreWrittenOnlyByTheStoreFile(t *testing.T) {
	root, files := repoGoFiles(t)
	for _, rel := range files {
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") && !strings.HasPrefix(rel, "wails-app/") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if rel != "internal/coderhost/budgetreq.go" {
			for _, w := range []string{"INTO coder_budget_requests", "UPDATE coder_budget_requests", "DELETE FROM coder_budget_requests"} {
				// Deleting a conversation removes its lead's requests and closes the ones that named it as a target
				// (never grants or reopens): the one other writer, with its own test.
				if rel == "internal/ai/chat_events_conversations.go" && w != "INTO coder_budget_requests" {
					continue
				}
				if strings.Contains(src, w) {
					t.Errorf("%s writes the budget requests (%q): only budgetreq.go does", rel, w)
				}
			}
			if strings.Contains(src, "budget_usd = budget_usd +") {
				t.Errorf("%s raises a budget in place: the person's grant is the only such write", rel)
			}
		}
	}
	raw, err := os.ReadFile("budgetreq.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "budget_usd = budget_usd +"); n != 1 {
		t.Fatalf("budgetreq.go raises a budget in %d places, want exactly the grant's", n)
	}
	for _, w := range []string{"Enqueue(", "SetCommand", "Confirm", "SetBudget", "carveBudget"} { // a request or grant starts no turn and never approves
		if strings.Contains(string(raw), w) {
			t.Errorf("budgetreq.go names %s", w)
		}
	}
}
