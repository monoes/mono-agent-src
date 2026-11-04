package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// coderProjectsConfigName is the configs-table row mapping a site's origin
// to the folder its queued coder chats work in (`coder queue add` without
// --cwd).
const coderProjectsConfigName = "coder_projects"

// coderProject maps one origin (scheme://host[:port]) to a folder.
type coderProject struct {
	Origin string `json:"origin"`
	Dir    string `json:"dir"`
}

type coderProjectsSetting struct {
	Projects []coderProject `json:"projects"`
}

func loadCoderProjects(db *sql.DB) ([]coderProject, error) {
	var raw string
	err := db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, coderProjectsConfigName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []coderProject{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading coder projects: %w", err)
	}
	var s coderProjectsSetting
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("reading coder projects: %w", err)
	}
	if s.Projects == nil {
		s.Projects = []coderProject{}
	}
	return s.Projects, nil
}

func saveCoderProjects(db *sql.DB, projects []coderProject) error {
	sort.Slice(projects, func(i, j int) bool { return projects[i].Origin < projects[j].Origin })
	b, err := json.Marshal(coderProjectsSetting{Projects: projects})
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = excluded.updated_at`,
		coderProjectsConfigName, string(b))
	if err != nil {
		return fmt.Errorf("saving coder projects: %w", err)
	}
	return nil
}

// normalizeOrigin turns a URL or origin into scheme://host[:port], lower
// case. Only http and https pages have an origin a folder can belong to.
func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", clicore.InvalidInput("%q is not an http(s) origin (e.g. http://localhost:3000)", raw)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

// projectDirFor is the folder origin is mapped to, or "".
func projectDirFor(projects []coderProject, origin string) string {
	for _, p := range projects {
		if p.Origin == origin {
			return p.Dir
		}
	}
	return ""
}

type coderProjectsListing struct {
	Projects []coderProjectRow `json:"projects"`
	Recent   []coderRecentRow  `json:"recent"`
}

type coderProjectRow struct {
	coderProject
	Exists bool `json:"exists"`
}

type coderRecentRow struct {
	Path          string `json:"path"`
	LastUsed      string `json:"lastUsed"`
	Conversations int    `json:"conversations"`
	Exists        bool   `json:"exists"`
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func newCoderProjectsCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Which folder a site's queued coder chats work in (origin → folder)",
		Long: "`coder queue add` without --cwd works in the folder mapped to the page's origin, else in the " +
			"coder root. `list` also returns the folders recent coder conversations used, which the browser " +
			"extension offers as targets.",
	}
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List the origin → folder map and the recent coder folders",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printCoderProjects(cmd, cfg, limit)
		},
	}
	list.Flags().IntVar(&limit, "limit", 10, "Maximum recent folders")
	mapCmd := &cobra.Command{
		Use:   "map <origin> <dir>",
		Short: "Send a site's queued coder chats to a folder",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireProjectsOperator(cfg); err != nil {
				return err
			}
			origin, err := normalizeOrigin(args[0])
			if err != nil {
				return err
			}
			dir, err := filepath.Abs(expandHome(args[1]))
			if err != nil || !dirExists(dir) {
				return clicore.InvalidInput("%s is not a folder", args[1])
			}
			return changeCoderProjects(cmd, cfg, func(ps []coderProject) ([]coderProject, error) {
				out := []coderProject{{Origin: origin, Dir: dir}}
				for _, p := range ps {
					if p.Origin != origin {
						out = append(out, p)
					}
				}
				return out, nil
			})
		},
	}
	unmap := &cobra.Command{
		Use:   "unmap <origin>",
		Short: "Forget a site's folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireProjectsOperator(cfg); err != nil {
				return err
			}
			origin, err := normalizeOrigin(args[0])
			if err != nil {
				return err
			}
			return changeCoderProjects(cmd, cfg, func(ps []coderProject) ([]coderProject, error) {
				out := make([]coderProject, 0, len(ps))
				for _, p := range ps {
					if p.Origin != origin {
						out = append(out, p)
					}
				}
				if len(out) == len(ps) {
					return nil, clicore.NotFound("%s is not mapped to a folder", origin)
				}
				return out, nil
			})
		},
	}
	for _, c := range []*cobra.Command{list, mapCmd, unmap} {
		clicore.WithJSONErrors(cfg.JSONOutput, c)
		cmd.AddCommand(c)
	}
	return cmd
}

// requireProjectsOperator is the gate of the verbs that change the map. Only the
// operator may: the map decides which folder a queued chat's full-access agent
// works in. An agent, a workflow's processes and the chat's runs are refused,
// and so is a shell inside a coder turn (the CLI's turn-aware check). It comes
// before the arguments are looked at, so a refused caller is told it is refused.
func requireProjectsOperator(cfg *Deps) error {
	return requireUser(cfg, "change which folder a site's coder chats use")
}

// changeCoderProjects applies change to the map, for a caller requireProjectsOperator let through.
func changeCoderProjects(cmd *cobra.Command, cfg *Deps, change func([]coderProject) ([]coderProject, error)) error {
	db, err := cfg.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	ps, err := loadCoderProjects(db.DB)
	if err == nil {
		ps, err = change(ps)
	}
	if err == nil {
		err = saveCoderProjects(db.DB, ps)
	}
	db.Close()
	if err != nil {
		return err
	}
	return printCoderProjects(cmd, cfg, 10)
}

func printCoderProjects(cmd *cobra.Command, cfg *Deps, limit int) error {
	db, err := cfg.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	ps, err := loadCoderProjects(db.DB)
	if err != nil {
		return err
	}
	store, err := ai.NewAIStore(db.DB)
	if err != nil {
		return err
	}
	recent, err := store.ListCoderWorkspaces(cfg.ProfileID(), limit)
	if err != nil {
		return err
	}
	out := coderProjectsListing{Projects: []coderProjectRow{}, Recent: []coderRecentRow{}}
	for _, p := range ps {
		out.Projects = append(out.Projects, coderProjectRow{p, dirExists(p.Dir)})
	}
	for _, w := range recent {
		out.Recent = append(out.Recent, coderRecentRow{w.Path, w.LastUsed, w.Conversations, dirExists(w.Path)})
	}
	if cfg.JSONOutput() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	for _, p := range out.Projects {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", p.Origin, p.Dir)
	}
	for _, w := range out.Recent {
		fmt.Fprintf(cmd.OutOrStdout(), "recent\t%s\t%s\n", w.Path, w.LastUsed)
	}
	return nil
}
