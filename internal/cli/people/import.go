package people

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/peoplereview"
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
				   full_name       = COALESCE(excluded.full_name,              people.full_name),
				   image_url       = COALESCE(excluded.image_url,              people.image_url),
				   contact_details = COALESCE(excluded.contact_details,        people.contact_details),
				   website         = COALESCE(excluded.website,                people.website),
				   content_count   = COALESCE(NULLIF(excluded.content_count,   0), people.content_count),
				   follower_count  = COALESCE(excluded.follower_count,         people.follower_count),
				   following_count = COALESCE(NULLIF(excluded.following_count, 0), people.following_count),
				   introduction    = COALESCE(excluded.introduction,           people.introduction),
				   is_verified     = COALESCE(NULLIF(excluded.is_verified,     0), people.is_verified),
				   category        = COALESCE(excluded.category,               people.category),
				   job_title       = COALESCE(excluded.job_title,              people.job_title),
				   updated_at      = excluded.updated_at`,
			)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("preparing statement: %w", err)
			}
			defer stmt.Close()

			now := time.Now().UTC()
			var imported int
			var nodes []monomind.KGNode
			// Run by anyone but the user, the import may not approve anyone
			// for outreach (peoplereview.NotOperatorCategory).
			notTheUser := opgate.Refusal()

			for i, raw := range rawPeople {
				row := i + 1
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
				followerCount := numberOrString(raw["follower_count"])
				contentCount, err := countField(raw, "content_count", row)
				if err != nil {
					tx.Rollback()
					return d.InvalidInput("%v; nothing was imported", err)
				}
				followingCount, err := countField(raw, "following_count", row)
				if err != nil {
					tx.Rollback()
					return d.InvalidInput("%v; nothing was imported", err)
				}

				var isVerified int
				if v, ok := raw["is_verified"].(bool); ok && v {
					isVerified = 1
				}

				personID := storage.NewID()
				if id, ok := raw["id"].(string); ok && id != "" {
					personID = id
				}

				if notTheUser != "" {
					stored, err := peoplereview.StoredPerson(cmd.Context(), tx, d.ProfileID(), strings.ToLower(platform), username)
					if err != nil {
						tx.Rollback()
						return err
					}
					// A row that leaves the draft out keeps the stored one (the
					// upsert coalesces), so it is not a change of the text.
					intro := introduction
					if intro == "" && stored.Found {
						intro = stored.Introduction
					}
					category, err = peoplereview.NotOperatorCategory(stored, category, category != "", intro, "an import an agent, the chat or a workflow runs")
					if err != nil {
						tx.Rollback()
						return d.InvalidInput("importing %s: %v (%s); nothing was imported", username, err, notTheUser)
					}
				}

				_, err = stmt.Exec(
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
				name := fullName
				if name == "" {
					name = username
				}
				desc := name + " on " + strings.ToLower(platform)
				if jobTitle != "" {
					desc += ", " + jobTitle
				}
				if introduction != "" {
					desc += " — " + introduction
				}
				nodes = append(nodes, monomind.KGNode{Name: name, Type: "person", Description: desc})
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("committing import: %w", err)
			}

			// Raw SQL above, so the storage layer's own sync did not run: send the
			// people to the knowledge graph and let it land before the process exits.
			if len(nodes) > 0 {
				var kg monomind.SyncGroup
				kg.Go(func(started func()) {
					_ = monomind.SyncToKnowledgeGraphStarted(cmd.Context(), db.DB, d.ProfileID(), nodes, nil, "people-import:"+nodes[0].Name, started)
				})
				kg.Settle()
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

// numberOrString reads a JSON string or number as the text it was written as;
// anything else is empty (not given).
func numberOrString(v interface{}) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return ""
}

// countField reads a count that arrives as a JSON number or a numeric string.
// A missing key is 0 (not given); a value that is neither is an error naming
// the row, never a silent zero.
func countField(raw map[string]interface{}, key string, row int) (int, error) {
	switch n := raw[key].(type) {
	case nil:
		return 0, nil
	case float64:
		return int(n), nil
	case string:
		if strings.TrimSpace(n) == "" {
			return 0, nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, fmt.Errorf("row %d: %s %q is not a number", row, key, n)
		}
		return int(f), nil
	}
	return 0, fmt.Errorf("row %d: %s is not a number", row, key)
}
