package orgdesign

import (
	"encoding/json"
	"testing"
)

// A role id becomes part of a directory name (worktree-<id>), an env value
// and the org:role sender form, so path separators, "..", and the colon are
// refused at validation.
func TestValidate_RoleIDCannotMoveTheWorkdir(t *testing.T) {
	for _, id := range []string{"../x", "a/b", `a\b`, "..", ".", "a:b"} {
		d := &Doc{Name: "x", Roles: []Role{{ID: id, ReportsTo: nil}}}
		if err := Validate(d); err == nil {
			t.Errorf("role id %q was accepted", id)
		}
	}
	for _, id := range []string{"ceo", "lead-2", "Role_3", "a.b"} {
		d := &Doc{Name: "x", Roles: []Role{{ID: id, ReportsTo: nil}}}
		if err := Validate(d); err != nil {
			t.Errorf("role id %q refused: %v", id, err)
		}
	}
}

// A relative run_config.workspace that climbs out of the project root would
// make the confinement base an ancestor of it.
func TestValidate_WorkspaceCannotEscapeTheRoot(t *testing.T) {
	check := func(ws string) error {
		d := &Doc{Name: "x", Roles: []Role{{ID: "a", ReportsTo: nil}},
			RunConfig: map[string]json.RawMessage{"workspace": json.RawMessage(`"` + ws + `"`)}}
		return Validate(d)
	}
	for _, ws := range []string{"..", "../..", "../x", "a/../../x"} {
		if check(ws) == nil {
			t.Errorf("workspace %q was accepted", ws)
		}
	}
	for _, ws := range []string{"repo", "worktree-per-role", "sub/dir", "a/../b", "/abs/elsewhere"} {
		if err := check(ws); err != nil {
			t.Errorf("workspace %q refused: %v", ws, err)
		}
	}
}
