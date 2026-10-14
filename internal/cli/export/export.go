package export

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

func (d Deps) newExportCmd() *cobra.Command {
	var outputDir string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export all data to JSON files",
		Long: `Exports all people to JSON files.
Files are written to the output directory, defaulting to the global --output-dir.`,
		Example: `  monoagentcli export
  monoagentcli export --output-dir ./backup`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			dir := outputDir
			if dir == "" {
				dir = d.OutputDir()
			}
			dir = expandPath(dir)

			if err := ensureDir(dir); err != nil {
				return fmt.Errorf("creating output directory: %w", err)
			}

			// Export people.
			peopleCount, err := exportPeopleData(db, dir, d.ProfileID())
			if err != nil {
				return fmt.Errorf("exporting people: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{
					"output_dir":   dir,
					"people_count": peopleCount,
				})
			}

			fmt.Fprintf(os.Stdout, "Exported %d people to %s\n", peopleCount, dir)
			return nil
		},
	}

	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Output directory (defaults to global --output-dir)")

	return cmd
}

func exportPeopleData(db *storage.Database, outputDir, profileID string) (int, error) {
	if profileID == "" {
		profileID = "default"
	}
	rows, err := db.DB.Query(
		`SELECT id, platform_username, platform, COALESCE(full_name,''),
		        COALESCE(image_url,''), COALESCE(contact_details,''),
		        COALESCE(website,''), COALESCE(content_count, 0), COALESCE(follower_count,''),
		        COALESCE(following_count, 0), COALESCE(introduction,''), COALESCE(is_verified, 0),
		        COALESCE(category,''), COALESCE(job_title,''),
		        created_at, updated_at
		 FROM people WHERE profile_id = ?
		 ORDER BY created_at DESC`,
		profileID,
	)
	if err != nil {
		return 0, fmt.Errorf("querying people: %w", err)
	}
	defer rows.Close()

	var people []storage.Person
	for rows.Next() {
		var p storage.Person
		var verified, contentCount, followingCount sql.NullInt64
		if err := rows.Scan(
			&p.ID, &p.PlatformUsername, &p.Platform, &p.FullName,
			&p.ImageURL, &p.ContactDetails, &p.Website, &contentCount,
			&p.FollowerCount, &followingCount, &p.Introduction,
			&verified, &p.Category, &p.JobTitle, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return 0, fmt.Errorf("scanning person: %w", err)
		}
		p.ContentCount = int(contentCount.Int64)
		p.FollowingCount = int(followingCount.Int64)
		p.IsVerified = verified.Valid && verified.Int64 != 0
		people = append(people, p)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterating people: %w", err)
	}

	envelope := map[string]interface{}{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"count":       len(people),
		"people":      people,
	}

	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshaling people: %w", err)
	}

	path := filepath.Join(outputDir, "people_export.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, fmt.Errorf("writing people export: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Written %d people to %s\n", len(people), path)
	return len(people), nil
}
