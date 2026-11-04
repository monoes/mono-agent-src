package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/profileorder"
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
		d.newProfileUpdateCmd(),
		d.newProfileDeleteCmd(),
		d.newProfileBackupsCmd(),
		d.newProfileFolderCmd(),
		d.newProfileMoveCmd(),
		d.newProfileProjectsCmd(),
		d.newProfileUploadDocumentCmd(),
		d.newProfileDocumentsCmd(),
		d.newProfileSearchKnowledgeCmd(),
		d.newProfileContextCmd(),
		d.newProfileInfoCmd(),
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
}

// lookupProfile resolves nameOrID to a profile row with the rule every other
// surface uses (profiledir.Resolve): an exact id wins over a name, and a name
// shared by several profiles is an error rather than a guess.
func (d Deps) lookupProfile(db *sql.DB, nameOrID string) (*profileJSON, error) {
	id, err := profiledir.Resolve(context.Background(), db, nameOrID)
	var ambiguous *profiledir.AmbiguousError
	switch {
	case errors.As(err, &ambiguous):
		return nil, d.InvalidInput("%s", ambiguous.Error())
	case errors.Is(err, profiledir.ErrUnknownProfile):
		return nil, d.NotFound("profile %q not found", nameOrID)
	case err != nil:
		return nil, fmt.Errorf("look up profile: %w", err)
	}
	var p profileJSON
	err = db.QueryRow(`SELECT id, name, created_at, icon FROM profiles WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.CreatedAt, &p.Icon)
	if err != nil {
		return nil, fmt.Errorf("look up profile: %w", err)
	}
	p.RootDir = profiledir.Root(db, p.ID)
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

			rows, err := db.DB.Query(`SELECT id, name, created_at, icon FROM profiles`)
			if err != nil {
				return fmt.Errorf("list profiles: %w", err)
			}
			profiles := []profileJSON{}
			for rows.Next() {
				var p profileJSON
				if rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.Icon) == nil {
					profiles = append(profiles, p)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			profileorder.Sort(profiles, func(p profileJSON) string { return p.CreatedAt }, func(p profileJSON) string { return p.ID })
			// Root reads profiles.root_dir, so only once the cursor is closed.
			for i := range profiles {
				profiles[i].RootDir = profiledir.Root(db.DB, profiles[i].ID)
			}

			if d.JSONOutput() {
				return printJSON(profiles)
			}

			fmt.Printf("%-36s  %-20s  %s\n", "ID", "NAME", "CREATED")
			for _, p := range profiles {
				fmt.Printf("%-36s  %-20s  %s\n", p.ID, p.Name, p.CreatedAt)
			}
			return nil
		},
	}
}

func (d Deps) newProfileGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name-or-id>",
		Short: "Show one profile (its folder and icon)",
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
			"project is fine). Only the user chooses that folder (" + folderIsTheUsers + "): an agent, the " +
			"chat or a workflow run is refused --root-dir (exit 3). --icon is an id from the shared " +
			"agent-avatars manifest.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(rootDir) != "" {
				if err := d.refuseFolderChoice(); err != nil {
					return err
				}
			}
			name := norm.NFC.String(strings.TrimSpace(args[0]))
			if err := validateProfileName(name); err != nil {
				return d.InvalidInput("%v", err)
			}
			if err := d.validateIcon(strings.TrimSpace(icon)); err != nil {
				return err
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
			if err := d.checkNameFree(db.DB, name, ""); err != nil {
				return err
			}

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
