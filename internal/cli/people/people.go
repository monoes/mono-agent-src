package people

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/personphoto"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"
)

func (d Deps) newPeopleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "people",
		Aliases: []string{"person"},
		Short:   "Manage discovered people/profiles",
		Long:    "List, view, delete, and import people discovered during search and interaction actions.",
	}

	cmd.AddCommand(
		d.newPeopleListCmd(),
		d.newPeopleCountCmd(),
		d.newPeopleGetCmd(),
		d.newPeopleInteractionsCmd(),
		d.newPeoplePostsCmd(),
		d.newPeopleDeleteCmd(),
		d.newPeopleImportCmd(),
		d.newPeopleMessagesCmd(),
		d.newPeopleStatusCmd(),
		d.newPeopleReviewCmd(),
		d.newPeopleTagCmd(),
		d.newPeopleLinksCmd(),
		d.newPeoplePhotosCmd(),
	)

	return cmd
}

// peopleFilter is the platform/search filter `people list` and `people count`
// share, so a page of results and its total always agree.
type peopleFilter struct {
	platform string
	search   string
}

// where returns the WHERE clause (profile-scoped) and its parameters. The
// platform matches case-insensitively; the search matches username or name.
func (f peopleFilter) where(profileID string) (string, []interface{}) {
	clause := " WHERE profile_id = ?"
	params := []interface{}{profileID}
	if f.platform != "" {
		clause += " AND UPPER(platform) = ?"
		params = append(params, strings.ToUpper(f.platform))
	}
	if f.search != "" {
		clause += " AND (platform_username LIKE ? OR full_name LIKE ?)"
		s := "%" + f.search + "%"
		params = append(params, s, s)
	}
	return clause, params
}

func (f *peopleFilter) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.platform, "platform", "p", "", "Filter by platform (case-insensitive)")
	cmd.Flags().StringVarP(&f.search, "search", "s", "", "Only people whose username or name contains this")
}

func (d Deps) newPeopleListCmd() *cobra.Command {
	var (
		filter peopleFilter
		limit  int
		offset int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List people in the database, newest first",
		Example: `  monoagentcli people list
  monoagentcli people list --platform instagram --limit 20
  monoagentcli people list --search sam --limit 50 --offset 50 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 || offset < 0 {
				return d.InvalidInput("--limit and --offset must not be negative")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			where, params := filter.where(d.ProfileID())
			query := `SELECT id, platform_username, platform, COALESCE(full_name,''),
			                 COALESCE(image_url,''), COALESCE(profile_url,''),
			                 COALESCE(follower_count,''), COALESCE(following_count,0), COALESCE(is_verified,0),
			                 COALESCE(category,''), COALESCE(job_title,''), COALESCE(created_at,'')
			          FROM people` + where + " ORDER BY created_at DESC"
			switch {
			case limit > 0:
				query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
			case offset > 0:
				query += fmt.Sprintf(" LIMIT -1 OFFSET %d", offset)
			}

			rows, err := db.DB.Query(query, params...)
			if err != nil {
				return fmt.Errorf("querying people: %w", err)
			}
			defer rows.Close()

			type personSummary struct {
				ID               string `json:"id"`
				PlatformUsername string `json:"platform_username"`
				Platform         string `json:"platform"`
				FullName         string `json:"full_name"`
				ImageURL         string `json:"image_url"`
				ProfileURL       string `json:"profile_url"`
				FollowerCount    string `json:"follower_count"`
				FollowingCount   int    `json:"following_count"`
				IsVerified       bool   `json:"is_verified"`
				Category         string `json:"category,omitempty"`
				JobTitle         string `json:"job_title,omitempty"`
				CreatedAt        string `json:"created_at"`
			}

			people := []personSummary{}
			for rows.Next() {
				var p personSummary
				var verified sql.NullInt64
				if err := rows.Scan(
					&p.ID, &p.PlatformUsername, &p.Platform, &p.FullName,
					&p.ImageURL, &p.ProfileURL,
					&p.FollowerCount, &p.FollowingCount,
					&verified, &p.Category, &p.JobTitle, &p.CreatedAt,
				); err != nil {
					return fmt.Errorf("scanning person: %w", err)
				}
				p.IsVerified = verified.Valid && verified.Int64 != 0
				people = append(people, p)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating people: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(people)
			}

			if len(people) == 0 {
				fmt.Println("No people found.")
				return nil
			}

			table := d.NewTable(os.Stdout, []string{"ID", "Username", "Platform", "Name", "Followers", "Verified"}, nil)

			for _, p := range people {
				shortID := p.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				verifiedStr := ""
				if p.IsVerified {
					verifiedStr = "yes"
				}
				table.Append([]string{
					shortID,
					truncateStr(p.PlatformUsername, 20),
					p.Platform,
					truncateStr(p.FullName, 20),
					p.FollowerCount,
					verifiedStr,
				})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d person(s)\n", len(people))
			return nil
		},
	}

	filter.addFlags(cmd)
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum number of results (0 = no limit)")
	cmd.Flags().IntVar(&offset, "offset", 0, "Skip this many results first, for paging")

	return cmd
}

// newPeopleCountCmd counts the people `people list` would page through with
// the same filters — the People page's total.
func (d Deps) newPeopleCountCmd() *cobra.Command {
	var filter peopleFilter
	cmd := &cobra.Command{
		Use:     "count",
		Short:   "Count people, with the same filters as people list",
		Example: `  monoagentcli --json people count --platform linkedin --search sam`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			where, params := filter.where(d.ProfileID())
			var count int
			if err := db.DB.QueryRow("SELECT COUNT(*) FROM people"+where, params...).Scan(&count); err != nil {
				return fmt.Errorf("counting people: %w", err)
			}
			if d.JSONOutput() {
				return printReviewJSON(map[string]int{"count": count})
			}
			fmt.Println(count)
			return nil
		},
	}
	filter.addFlags(cmd)
	return cmd
}

func (d Deps) newPeoplePhotosCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "photos",
		Short: "Download the profile photos still stored as remote URLs",
		Long: "Saves a local copy (in the image vault) of every person's photo that is still a remote URL, " +
			"and points the person at it. Photos that can no longer be fetched keep their URL.",
		Example: `  monoagentcli --json people photos`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			saved, errs := personphoto.Localize(cmd.Context(), db.DB, d.ProfileID(), nil)
			vault.Wait()
			if d.JSONOutput() {
				failed := make([]string, len(errs))
				for i, e := range errs {
					failed[i] = e.Error()
				}
				return printReviewJSON(map[string]interface{}{"saved": saved, "failed": failed})
			}
			fmt.Printf("saved %d photos, %d could not be fetched\n", saved, len(errs))
			for _, e := range errs {
				fmt.Fprintln(cmd.ErrOrStderr(), e)
			}
			return nil
		},
	}
}

func (d Deps) newPeopleGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show detailed information about a person",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			personID := args[0]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			var p storage.Person
			var verified, contentCount, followingCount sql.NullInt64
			var fullName, imageURL, contactDetails, website, profileURL sql.NullString
			var followerCount, introduction, category, jobTitle sql.NullString
			var headline, location, about, experience, education, details sql.NullString

			err = db.DB.QueryRow(
				`SELECT id, COALESCE(platform_username, ''), COALESCE(platform, ''), full_name,
				        image_url, contact_details,
				        website, COALESCE(content_count, 0), follower_count,
				        COALESCE(following_count, 0), introduction, COALESCE(is_verified, 0),
				        category, job_title,
				        created_at, updated_at, profile_url,
				        headline, location, about, experience, education, profile_details
				 FROM people WHERE id = ? AND profile_id = ?`, personID, d.ProfileID(),
			).Scan(
				&p.ID, &p.PlatformUsername, &p.Platform, &fullName,
				&imageURL, &contactDetails, &website, &contentCount,
				&followerCount, &followingCount, &introduction,
				&verified, &category, &jobTitle, &p.CreatedAt, &p.UpdatedAt, &profileURL,
				&headline, &location, &about, &experience, &education, &details,
			)
			if err == sql.ErrNoRows {
				return d.NotFound("person %q not found", personID)
			}
			if err != nil {
				return fmt.Errorf("querying person: %w", err)
			}

			p.FullName = fullName.String
			p.ImageURL = imageURL.String
			p.ContactDetails = contactDetails.String
			p.Website = website.String
			p.FollowerCount = followerCount.String
			p.ContentCount = int(contentCount.Int64)
			p.FollowingCount = int(followingCount.Int64)
			p.Introduction = introduction.String
			p.IsVerified = verified.Valid && verified.Int64 != 0
			p.Category = category.String
			p.JobTitle = jobTitle.String
			p.Headline = headline.String
			p.Location = location.String
			p.About = about.String
			p.Experience = jsonArrayOrNil(experience.String)
			p.Education = jsonArrayOrNil(education.String)
			p.ProfileDetails = jsonObjectOrNil(details.String)

			links := confirmedLinks(db, d.ProfileID(), p.ID)
			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					storage.Person
					ProfileURL string          `json:"profile_url,omitempty"`
					Links      []confirmedLink `json:"links,omitempty"`
				}{p, profileURL.String, links})
			}

			table := d.NewTable(os.Stdout, []string{"Field", "Value"}, []tw.Align{tw.AlignRight, tw.AlignLeft})

			table.Append([]string{"ID", p.ID})
			table.Append([]string{"Username", p.PlatformUsername})
			table.Append([]string{"Platform", p.Platform})
			table.Append([]string{"Full Name", p.FullName})
			table.Append([]string{"Verified", fmt.Sprintf("%v", p.IsVerified)})
			table.Append([]string{"Category", p.Category})
			table.Append([]string{"Job Title", p.JobTitle})
			if p.Headline != "" {
				table.Append([]string{"Headline", p.Headline})
			}
			if p.Location != "" {
				table.Append([]string{"Location", p.Location})
			}
			table.Append([]string{"Followers", p.FollowerCount})
			table.Append([]string{"Following", fmt.Sprintf("%d", p.FollowingCount)})
			table.Append([]string{"Content Count", fmt.Sprintf("%d", p.ContentCount)})
			if p.Website != "" {
				table.Append([]string{"Website", p.Website})
			}
			if p.ContactDetails != "" {
				table.Append([]string{"Contact", p.ContactDetails})
			}
			if p.About != "" {
				table.Append([]string{"About", truncateStr(p.About, 60)})
			}
			if p.Introduction != "" {
				table.Append([]string{"Introduction", truncateStr(p.Introduction, 60)})
			}
			if p.ImageURL != "" {
				table.Append([]string{"Image", truncateStr(p.ImageURL, 60)})
			}
			for i, line := range profileEntryLines(p.Experience, "title", "company", "date_range") {
				table.Append([]string{labelOnFirst(i, "Experience"), line})
			}
			for i, line := range profileEntryLines(p.Education, "school", "degree", "date_range") {
				table.Append([]string{labelOnFirst(i, "Education"), line})
			}
			for _, row := range profileDetailRows(p.ProfileDetails) {
				table.Append(row)
			}
			for _, l := range links {
				table.Append([]string{"Same person as", l.String()})
			}
			table.Append([]string{"Created", p.CreatedAt.Format("2006-01-02 15:04:05")})
			table.Append([]string{"Updated", p.UpdatedAt.Format("2006-01-02 15:04:05")})
			table.Render()

			return nil
		},
	}
}

func (d Deps) newPeopleDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>...",
		Short:   "Delete people from the database, with their saved photos",
		Example: `  monoagentcli --json people delete <id> [<id>...]`,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			deleted := 0
			for _, personID := range args {
				// The photo goes first (the row says where it is); a person
				// that isn't there has no photo to lose.
				personphoto.Delete(cmd.Context(), db.DB, d.ProfileID(), []string{personID})
				result, err := db.DB.Exec("DELETE FROM people WHERE id = ? AND profile_id = ?", personID, d.ProfileID())
				if err != nil {
					return fmt.Errorf("deleting person %s: %w", personID, err)
				}
				if n, _ := result.RowsAffected(); n == 0 {
					return fmt.Errorf("person %q not found (%d deleted)", personID, deleted)
				}
				deleted++
			}
			vault.Wait()
			if d.JSONOutput() {
				return printReviewJSON(map[string]int{"deleted": deleted})
			}
			fmt.Fprintf(os.Stdout, "Deleted %d people.\n", deleted)
			return nil
		},
	}
}
