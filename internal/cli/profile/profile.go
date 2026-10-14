package profile

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// printJSON writes v to stdout as indented JSON. Small shared helper for the
// commands that honor the global --json flag.
func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (d Deps) newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage profiles (sessions, credentials, and workflows are profile-scoped)",
	}
	cmd.AddCommand(
		d.newProfileListCmd(),
		d.newProfileGetCmd(),
		d.newProfileCreateCmd(),
		d.newProfileSwitchCmd(),
		d.newProfileCurrentCmd(),
		d.newProfileFolderCmd(),
		d.newProfileMoveCmd(),
		d.newProfileProjectsCmd(),
		d.newProfileUploadDocumentCmd(),
		d.newProfileDocumentsCmd(),
		d.newProfileSearchKnowledgeCmd(),
		d.newProfileContextCmd(),
	)
	return cmd
}

// profileJSON is one profile as `profile list/get/create --json` print it.
// RootDir is the resolved folder (the default location when no override is
// set), so callers never need the fallback rule themselves.
type profileJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	RootDir   string `json:"root_dir"`
	Icon      string `json:"icon"`
	Active    bool   `json:"active"`
}

// activeProfileSetting reads the persisted active profile id ("" if unset).
func activeProfileSetting(db *sql.DB) string {
	var id string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&id)
	return id
}

// lookupProfile resolves nameOrID to a profile row. An exact id wins over a
// name, so a profile whose name happens to be another's id can't shadow it.
func (d Deps) lookupProfile(db *sql.DB, nameOrID string) (*profileJSON, error) {
	var p profileJSON
	err := db.QueryRow(`SELECT id, name, created_at, icon FROM profiles
		WHERE id = ? OR LOWER(name) = LOWER(?)
		ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END LIMIT 1`,
		nameOrID, nameOrID, nameOrID).Scan(&p.ID, &p.Name, &p.CreatedAt, &p.Icon)
	if err == sql.ErrNoRows {
		return nil, d.NotFound("profile %q not found", nameOrID)
	}
	if err != nil {
		return nil, fmt.Errorf("look up profile: %w", err)
	}
	p.RootDir = profiledir.Root(db, p.ID)
	p.Active = p.ID == activeProfileSetting(db)
	return &p, nil
}

func (d Deps) newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()

			// Resolve the active profile before opening the profiles cursor
			// below — running a second query while `rows` is still open (not
			// yet closed/drained) can hold the connection pool's only
			// connection when the pool is capped to one, deadlocking.
			activeID := activeProfileSetting(db.DB)

			rows, err := db.DB.Query(`SELECT id, name, created_at, icon FROM profiles ORDER BY created_at ASC`)
			if err != nil {
				return fmt.Errorf("list profiles: %w", err)
			}
			profiles := []profileJSON{}
			for rows.Next() {
				var p profileJSON
				if rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.Icon) == nil {
					p.Active = p.ID == activeID
					profiles = append(profiles, p)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			// Root reads profiles.root_dir, so only once the cursor is closed.
			for i := range profiles {
				profiles[i].RootDir = profiledir.Root(db.DB, profiles[i].ID)
			}

			if d.JSONOutput() {
				return printJSON(profiles)
			}

			fmt.Printf("%-36s  %-20s  %s\n", "ID", "NAME", "CREATED")
			for _, p := range profiles {
				marker := ""
				if p.Active {
					marker = " *"
				}
				fmt.Printf("%-36s  %-20s  %s%s\n", p.ID, p.Name, p.CreatedAt, marker)
			}
			return nil
		},
	}
}

func (d Deps) newProfileGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name-or-id>",
		Short: "Show one profile (its folder, icon, and whether it is active)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			p, err := d.lookupProfile(db.DB, args[0])
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return printJSON(p)
			}
			fmt.Printf("%s (%s)\nfolder: %s\ncreated: %s\n", p.Name, p.ID, p.RootDir, p.CreatedAt)
			return nil
		},
	}
}

// profileCreateResult is `profile create --json`: the new profile, plus
// why its folder could not be prepared, if it couldn't (the profile itself
// still exists; the next start of the app retries the folder).
type profileCreateResult struct {
	profileJSON
	LayoutError string `json:"layout_error,omitempty"`
}

func (d Deps) newProfileCreateCmd() *cobra.Command {
	var rootDir, icon string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new profile",
		Long: "Create a new profile. --root-dir points its data at a folder of your choice instead of " +
			"~/.monoagent/profiles/<id>/ (an absolute path; an existing, non-empty folder such as a coding " +
			"project is fine). --icon is an id from the shared agent-avatars manifest.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return d.InvalidInput("profile name cannot be empty")
			}
			rootDir = strings.TrimSpace(rootDir)
			if rootDir != "" {
				if err := d.validateFolderChoice(rootDir); err != nil {
					return err
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()

			id := uuid.New().String()
			now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
			icon = strings.TrimSpace(icon)
			if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at, root_dir, icon) VALUES (?, ?, ?, ?, ?)`,
				id, name, now, rootDir, icon); err != nil {
				return fmt.Errorf("create profile: %w", err)
			}
			res := profileCreateResult{profileJSON: profileJSON{
				ID: id, Name: name, CreatedAt: now, RootDir: profiledir.Root(db.DB, id), Icon: icon,
			}}
			if err := profiledir.EnsureLayout(db.DB, id); err != nil {
				res.LayoutError = err.Error()
				fmt.Fprintf(os.Stderr, "warning: profile %s: creating folder layout: %v\n", id, err)
			}
			if d.JSONOutput() {
				return printJSON(res)
			}
			fmt.Printf("Created profile: %s (%s)\n", name, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&rootDir, "root-dir", "", "Absolute folder for this profile's data (default ~/.monoagent/profiles/<id>/)")
	cmd.Flags().StringVar(&icon, "icon", "", "Icon id from the agent-avatars manifest")
	return cmd
}

func (d Deps) newProfileSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <name-or-id>",
		Short: "Switch the active profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()

			p, err := d.lookupProfile(db.DB, args[0])
			if err != nil {
				return err
			}
			id := p.ID
			_, err = db.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('active_profile_id', ?)`, id)
			if err != nil {
				return fmt.Errorf("switch profile: %w", err)
			}
			if d.JSONOutput() {
				return printJSON(map[string]string{"id": id})
			}
			fmt.Printf("Switched to profile: %s\n", id)
			return nil
		},
	}
}

func (d Deps) newProfileCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the active profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()

			var id, name string
			err = db.DB.QueryRow(`SELECT p.id, p.name FROM profiles p
			                      INNER JOIN settings s ON s.value = p.id AND s.key = 'active_profile_id'`).Scan(&id, &name)
			if err != nil {
				if d.JSONOutput() {
					return printJSON(map[string]string{"id": "default", "name": "default"})
				}
				fmt.Println("default")
				return nil
			}
			if d.JSONOutput() {
				return printJSON(map[string]string{"id": id, "name": name})
			}
			fmt.Printf("%s (%s)\n", name, id)
			return nil
		},
	}
}
