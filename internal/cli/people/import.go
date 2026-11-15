package people

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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
				followerCount, err := followerText(raw, row)
				if err != nil {
					tx.Rollback()
					return d.InvalidInput("%v; nothing was imported", err)
				}
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

				// The row of this person under any spelling of the platform is
				// the one to update; a new person gets the canonical spelling.
				storedPlatform, err := storage.ResolvePlatform(tx, d.ProfileID(), platform, username)
				if err != nil {
					tx.Rollback()
					return fmt.Errorf("looking up %s: %w", username, err)
				}

				if notTheUser != "" {
					stored, err := peoplereview.StoredPerson(cmd.Context(), tx, d.ProfileID(), storedPlatform, username)
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
					personID, username, storedPlatform,
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

// maxCount bounds a count a row may carry; anything above is not a count.
const maxCount = 1e12

// sane reports whether f is a usable count: finite, not negative, not absurd.
func sane(f float64) bool {
	return !math.IsNaN(f) && f >= 0 && f <= maxCount
}

// followerText reads follower_count as the text it was written as: a JSON
// string (kept as is, "1.2K" included, unless it is a number out of range) or a
// JSON number. Anything else is empty (not given); a nonsense number is an
// error naming the row.
func followerText(raw map[string]interface{}, row int) (string, error) {
	switch n := raw["follower_count"].(type) {
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil && !sane(f) {
			return "", fmt.Errorf("row %d: follower_count %q is not a usable count", row, n)
		}
		return n, nil
	case float64:
		if !sane(n) {
			return "", fmt.Errorf("row %d: follower_count %v is not a usable count", row, n)
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	}
	return "", nil
}

// countField reads a count that arrives as a JSON number or a numeric string.
// A missing key is 0 (not given); a value that is neither, or that is NaN,
// infinite, negative or above maxCount, is an error naming the row, never a
// silent zero or a platform-dependent int conversion.
func countField(raw map[string]interface{}, key string, row int) (int, error) {
	var f float64
	switch n := raw[key].(type) {
	case nil:
		return 0, nil
	case float64:
		f = n
	case string:
		if strings.TrimSpace(n) == "" {
			return 0, nil
		}
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(n), 64); err != nil {
			return 0, fmt.Errorf("row %d: %s %q is not a number", row, key, n)
		}
	default:
		return 0, fmt.Errorf("row %d: %s is not a number", row, key)
	}
	if !sane(f) {
		return 0, fmt.Errorf("row %d: %s %v is not a usable count", row, key, f)
	}
	return int(f), nil
}
