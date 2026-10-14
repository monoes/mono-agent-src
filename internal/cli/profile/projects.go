package profile

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// monomindProject is one `profile projects --json` row: an existing
// monomind project that could become a new profile's folder.
type monomindProject struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// monomindProjectsFile is the shape of ~/.monomind-projects.json, written by
// monomind's own `init upgrade --all`. Not owned by monoagent — read-only.
type monomindProjectsFile struct {
	Projects []string `json:"projects"`
}

// readMonomindProjectsFile returns the raw project path list, or nil if the
// file doesn't exist or can't be parsed — this is a convenience suggestion,
// never worth failing over.
func readMonomindProjectsFile() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	data, err := os.ReadFile(filepath.Join(home, ".monomind-projects.json"))
	if err != nil {
		return nil
	}
	var f monomindProjectsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil
	}
	return f.Projects
}

// isMonomindProject reports whether root was set up by `monomind init`: the
// marker monomind's own CLI uses, .monomind/config.yaml (a bare .monomind/
// doesn't count — profiledir.EnsureLayout creates it).
func isMonomindProject(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".monomind", "config.yaml"))
	return err == nil
}

// filterMonomindProjects turns a raw path list (as noisy as monomind's own
// init-upgrade bookkeeping — temp test directories from monomind's own test
// suite, one-off /tmp paths, monoagent's own profile folders — routinely
// makes it) into a small, meaningful suggestion list:
//   - a path that no longer exists, or was never (or is no longer) a real
//     monomind project (.monomind/config.yaml), is dropped — this one check
//     alone eliminates essentially all the ephemeral test-run noise
//   - a path already used as an existing profile's root_dir is dropped —
//     no point suggesting a project that already has one
//   - exact-duplicate paths collapse to their first occurrence
//
// Preserves the input's order — the source file carries no recency signal
// worth inventing one for.
func filterMonomindProjects(rawPaths []string, existingRootDirs map[string]bool) []monomindProject {
	seen := make(map[string]bool, len(rawPaths))
	out := []monomindProject{}
	for _, p := range rawPaths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if existingRootDirs[p] || !isMonomindProject(p) {
			continue
		}
		out = append(out, monomindProject{Path: p, Name: filepath.Base(p)})
	}
	return out
}

// profileRootDirOverrides is every profile's custom root_dir.
func profileRootDirOverrides(db *sql.DB) map[string]bool {
	dirs := map[string]bool{}
	rows, err := db.Query(`SELECT root_dir FROM profiles WHERE root_dir != ''`)
	if err != nil {
		return dirs
	}
	defer rows.Close()
	for rows.Next() {
		var rd string
		if rows.Scan(&rd) == nil {
			dirs[rd] = true
		}
	}
	return dirs
}

func (d Deps) newProfileProjectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List existing monomind projects that could become a new profile's folder",
		Long: "Reads monomind's own project list (~/.monomind-projects.json) and keeps the folders that " +
			"are still monomind projects and are not already a profile's folder. Never fails on a missing " +
			"or unreadable list: that is just no suggestions.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			projects := []monomindProject{}
			if raw := readMonomindProjectsFile(); len(raw) > 0 {
				existing := map[string]bool{}
				if db, err := d.OpenDB(); err == nil {
					existing = profileRootDirOverrides(db.DB)
					db.Close()
				}
				projects = filterMonomindProjects(raw, existing)
			}
			if d.JSONOutput() {
				return printJSON(projects)
			}
			for _, p := range projects {
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s  %s\n", p.Name, p.Path)
			}
			return nil
		},
	}
}
