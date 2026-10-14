package people

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

func (d Deps) newPeopleImportCmd() *cobra.Command {
	var (
		filePath string
		platform string
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import people from a JSON array file",
		Example: `  monoagentcli people import --file people.json --platform instagram
  monoagentcli people import --file contacts.json --platform linkedin`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}
			if platform == "" {
				return fmt.Errorf("--platform is required")
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("reading file %s: %w", filePath, err)
			}

			var rawPeople []map[string]interface{}
			if err := json.Unmarshal(data, &rawPeople); err != nil {
				return fmt.Errorf("parsing JSON array: %w", err)
			}

			if len(rawPeople) == 0 {
				fmt.Fprintln(os.Stderr, "No people found in file.")
				return nil
			}

			tx, err := db.DB.Begin()
			if err != nil {
				return fmt.Errorf("beginning transaction: %w", err)
			}

			stmt, err := tx.Prepare(
				`INSERT INTO people (id, platform_username, platform, full_name, image_url,
				        contact_details, website, content_count, follower_count,
				        following_count, introduction, is_verified, category, job_title,
				        profile_id, created_at, updated_at)
				 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
				 ON CONFLICT(platform_username, platform, profile_id)
				 DO UPDATE SET
				   full_name       = excluded.full_name,
				   image_url       = excluded.image_url,
				   contact_details = excluded.contact_details,
				   website         = excluded.website,
				   content_count   = excluded.content_count,
				   follower_count  = excluded.follower_count,
				   following_count = excluded.following_count,
				   introduction    = excluded.introduction,
				   is_verified     = excluded.is_verified,
				   category        = excluded.category,
				   job_title       = excluded.job_title,
				   updated_at      = excluded.updated_at`,
			)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("preparing statement: %w", err)
			}
			defer stmt.Close()

			now := time.Now().UTC()
			var imported int

			for _, raw := range rawPeople {
				username, _ := raw["platform_username"].(string)
				if username == "" {
					username, _ = raw["username"].(string)
				}
				if username == "" {
					continue
				}

				fullName, _ := raw["full_name"].(string)
				imageURL, _ := raw["image_url"].(string)
				contactDetails, _ := raw["contact_details"].(string)
				website, _ := raw["website"].(string)
				introduction, _ := raw["introduction"].(string)
				category, _ := raw["category"].(string)
				jobTitle, _ := raw["job_title"].(string)
				followerCount, _ := raw["follower_count"].(string)

				var contentCount, followingCount int
				if v, ok := raw["content_count"].(float64); ok {
					contentCount = int(v)
				}
				if v, ok := raw["following_count"].(float64); ok {
					followingCount = int(v)
				}

				var isVerified int
				if v, ok := raw["is_verified"].(bool); ok && v {
					isVerified = 1
				}

				personID := storage.NewID()
				if id, ok := raw["id"].(string); ok && id != "" {
					personID = id
				}

				_, err := stmt.Exec(
					personID, username, strings.ToLower(platform),
					nullableStr(fullName), nullableStr(imageURL),
					nullableStr(contactDetails), nullableStr(website),
					contentCount, nullableStr(followerCount), followingCount,
					nullableStr(introduction), isVerified,
					nullableStr(category), nullableStr(jobTitle),
					d.ProfileID(), now, now,
				)
				if err != nil {
					tx.Rollback()
					return fmt.Errorf("importing person %s: %w", username, err)
				}
				imported++
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("committing import: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Imported %d person(s) for platform %s.\n", imported, platform)
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "Path to JSON file containing people array (required)")
	cmd.Flags().StringVar(&platform, "platform", "", "Platform for imported people (required)")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("platform")

	return cmd
}

// jsonArrayOrNil returns a stored JSON array as raw JSON, or nil when it is
// empty or not an array (so it drops out of the JSON output).
func jsonArrayOrNil(s string) json.RawMessage {
	var arr []json.RawMessage
	if s == "" || json.Unmarshal([]byte(s), &arr) != nil || len(arr) == 0 {
		return nil
	}
	return json.RawMessage(s)
}

// jsonObjectOrNil returns a stored JSON object as raw JSON, or nil when it
// is empty or not an object.
func jsonObjectOrNil(s string) json.RawMessage {
	var obj map[string]json.RawMessage
	if s == "" || json.Unmarshal([]byte(s), &obj) != nil || len(obj) == 0 {
		return nil
	}
	return json.RawMessage(s)
}

// nullableStr returns a sql.NullString; empty strings map to NULL.
func nullableStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
