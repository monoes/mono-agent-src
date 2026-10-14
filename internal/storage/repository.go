package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

// UpsertPerson inserts a new person or updates an existing one matched by
// platform_username + platform + profile_id (person.ProfileID; defaults to
// "default" if unset).
// personKGDisplayName returns the best available human-readable name for a
// knowledge-graph node representing a person.
func personKGDisplayName(p *Person) string {
	if p.FullName != "" {
		return p.FullName
	}
	return p.PlatformUsername
}

// personKGDescription summarizes a person's known fields for a
// knowledge-graph node description.
func personKGDescription(p *Person) string {
	desc := fmt.Sprintf("%s on %s", personKGDisplayName(p), p.Platform)
	if p.JobTitle != "" {
		desc += fmt.Sprintf(", %s", p.JobTitle)
	}
	if p.Introduction != "" {
		desc += fmt.Sprintf(" — %s", p.Introduction)
	}
	return desc
}

func (d *Database) UpsertPerson(person *Person) error {
	if person.ID == "" {
		person.ID = NewID()
	}
	if person.ProfileID == "" {
		person.ProfileID = "default"
	}
	now := time.Now().UTC()

	_, err := d.DB.Exec(`
		INSERT INTO people (
			id, platform_username, platform, full_name, image_url,
			contact_details, website, content_count, follower_count,
			following_count, introduction, is_verified, category, job_title,
			profile_id, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
			job_title       = COALESCE(excluded.job_title,               people.job_title),
			updated_at      = excluded.updated_at`,
		person.ID, person.PlatformUsername, person.Platform,
		nullStr(person.FullName), nullStr(person.ImageURL),
		nullStr(person.ContactDetails), nullStr(person.Website),
		person.ContentCount, nullStr(person.FollowerCount), person.FollowingCount,
		nullStr(person.Introduction), boolToInt(person.IsVerified),
		nullStr(person.Category), nullStr(person.JobTitle),
		person.ProfileID, now, now,
	)
	if err != nil {
		return fmt.Errorf("upserting person %s/%s: %w", person.Platform, person.PlatformUsername, err)
	}

	go func() {
		node := monomind.KGNode{Name: personKGDisplayName(person), Type: "person", Description: personKGDescription(person)}
		_ = monomind.SyncToKnowledgeGraph(context.Background(), d.DB, person.ProfileID, []monomind.KGNode{node}, nil, "person:"+person.ID)
	}()

	return nil
}

// GetPerson retrieves a single person by ID. Returns nil when not found.
func (d *Database) GetPerson(id string) (*Person, error) {
	p := &Person{}
	var fullName, imageURL, contactDetails, website, followerCount sql.NullString
	var introduction, category, jobTitle sql.NullString
	var isVerified, contentCount, followingCount sql.NullInt64

	err := d.DB.QueryRow(`
		SELECT id, COALESCE(platform_username, ''), COALESCE(platform, ''), full_name, image_url,
		       contact_details, website, COALESCE(content_count, 0), follower_count,
		       COALESCE(following_count, 0), introduction, COALESCE(is_verified, 0), category, job_title,
		       created_at, updated_at
		FROM people WHERE id = ?`, id,
	).Scan(
		&p.ID, &p.PlatformUsername, &p.Platform, &fullName, &imageURL,
		&contactDetails, &website, &contentCount, &followerCount,
		&followingCount, &introduction, &isVerified, &category, &jobTitle,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting person %s: %w", id, err)
	}

	p.FullName = fullName.String
	p.ImageURL = imageURL.String
	p.ContactDetails = contactDetails.String
	p.Website = website.String
	p.FollowerCount = followerCount.String
	p.ContentCount = int(contentCount.Int64)
	p.FollowingCount = int(followingCount.Int64)
	p.Introduction = introduction.String
	p.IsVerified = isVerified.Valid && isVerified.Int64 != 0
	p.Category = category.String
	p.JobTitle = jobTitle.String

	return p, nil
}

// GetPersonByUsername retrieves a single person by (platform_username,
// platform, profileID), the same unique key UpsertPerson conflicts on.
// Returns nil when not found.
func (d *Database) GetPersonByUsername(platformUsername, platform, profileID string) (*Person, error) {
	if profileID == "" {
		profileID = "default"
	}
	p := &Person{}
	var fullName, imageURL, contactDetails, website, followerCount sql.NullString
	var introduction, category, jobTitle sql.NullString
	var isVerified, contentCount, followingCount sql.NullInt64

	err := d.DB.QueryRow(`
		SELECT id, COALESCE(platform_username, ''), COALESCE(platform, ''), full_name, image_url,
		       contact_details, website, COALESCE(content_count, 0), follower_count,
		       COALESCE(following_count, 0), introduction, COALESCE(is_verified, 0), category, job_title,
		       profile_id, created_at, updated_at
		FROM people WHERE platform_username = ? AND platform = ? AND profile_id = ?`, platformUsername, platform, profileID,
	).Scan(
		&p.ID, &p.PlatformUsername, &p.Platform, &fullName, &imageURL,
		&contactDetails, &website, &contentCount, &followerCount,
		&followingCount, &introduction, &isVerified, &category, &jobTitle,
		&p.ProfileID, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting person %s/%s: %w", platform, platformUsername, err)
	}

	p.FullName = fullName.String
	p.ImageURL = imageURL.String
	p.ContactDetails = contactDetails.String
	p.Website = website.String
	p.FollowerCount = followerCount.String
	p.ContentCount = int(contentCount.Int64)
	p.FollowingCount = int(followingCount.Int64)
	p.Introduction = introduction.String
	p.IsVerified = isVerified.Valid && isVerified.Int64 != 0
	p.Category = category.String
	p.JobTitle = jobTitle.String

	return p, nil
}

// ListPeople returns people optionally filtered by platform and a search term
// (matched against platform_username and full_name). Pass empty strings to skip
// filters. limit <= 0 defaults to 100.
func (d *Database) ListPeople(platform, search string, limit, offset int) ([]*Person, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var conditions []string
	var args []interface{}

	if platform != "" {
		conditions = append(conditions, "platform = ?")
		args = append(args, platform)
	}
	if search != "" {
		conditions = append(conditions, "(platform_username LIKE ? OR full_name LIKE ?)")
		like := "%" + search + "%"
		args = append(args, like, like)
	}

	query := `
		SELECT id, COALESCE(platform_username, ''), COALESCE(platform, ''), full_name, image_url,
		       contact_details, website, COALESCE(content_count, 0), follower_count,
		       COALESCE(following_count, 0), introduction, COALESCE(is_verified, 0), category, job_title,
		       created_at, updated_at
		FROM people`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing people: %w", err)
	}
	defer rows.Close()

	var people []*Person
	for rows.Next() {
		p := &Person{}
		var fullName, imageURL, contactDetails, website, followerCount sql.NullString
		var introduction, category, jobTitle sql.NullString
		var isVerified, contentCount, followingCount sql.NullInt64

		if err := rows.Scan(
			&p.ID, &p.PlatformUsername, &p.Platform, &fullName, &imageURL,
			&contactDetails, &website, &contentCount, &followerCount,
			&followingCount, &introduction, &isVerified, &category, &jobTitle,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning person row: %w", err)
		}

		p.FullName = fullName.String
		p.ImageURL = imageURL.String
		p.ContactDetails = contactDetails.String
		p.Website = website.String
		p.FollowerCount = followerCount.String
		p.ContentCount = int(contentCount.Int64)
		p.FollowingCount = int(followingCount.Int64)
		p.Introduction = introduction.String
		p.IsVerified = isVerified.Valid && isVerified.Int64 != 0
		p.Category = category.String
		p.JobTitle = jobTitle.String

		people = append(people, p)
	}
	return people, rows.Err()
}

// DeletePerson removes a person by ID.
// DeletePerson removes a person row. Knowledge-graph entries created for
// this person are intentionally left in place as a historical record —
// there is no KG delete/rollback wired up here (out of scope).
func (d *Database) DeletePerson(id string) error {
	result, err := d.DB.Exec("DELETE FROM people WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting person %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("person %s not found", id)
	}
	return nil
}

// BatchUpsertPeople inserts or updates multiple people within a single
// transaction. Like UpsertPerson, an empty field never overwrites a stored
// value, so re-saving a person with fewer fields keeps their enrichment.
func (d *Database) BatchUpsertPeople(people []*Person) error {
	if len(people) == 0 {
		return nil
	}

	tx, err := d.DB.Begin()
	if err != nil {
		return fmt.Errorf("beginning batch people transaction: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO people (
			id, platform_username, platform, full_name, image_url,
			contact_details, website, content_count, follower_count,
			following_count, introduction, is_verified, category, job_title,
			profile_id, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
			job_title       = COALESCE(excluded.job_title,               people.job_title),
			updated_at      = excluded.updated_at`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("preparing batch people upsert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, p := range people {
		if p.ID == "" {
			p.ID = NewID()
		}
		if p.ProfileID == "" {
			p.ProfileID = "default"
		}
		if _, err := stmt.Exec(
			p.ID, p.PlatformUsername, p.Platform,
			nullStr(p.FullName), nullStr(p.ImageURL),
			nullStr(p.ContactDetails), nullStr(p.Website),
			p.ContentCount, nullStr(p.FollowerCount), p.FollowingCount,
			nullStr(p.Introduction), boolToInt(p.IsVerified),
			nullStr(p.Category), nullStr(p.JobTitle),
			p.ProfileID, now, now,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("upserting person %s/%s: %w", p.Platform, p.PlatformUsername, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	go func() {
		// Batches are expected to belong to a single profile in practice;
		// group by profile defensively rather than assuming that.
		byProfile := make(map[string][]monomind.KGNode)
		for _, p := range people {
			node := monomind.KGNode{Name: personKGDisplayName(p), Type: "person", Description: personKGDescription(p)}
			byProfile[p.ProfileID] = append(byProfile[p.ProfileID], node)
		}
		for profileID, nodes := range byProfile {
			_ = monomind.SyncToKnowledgeGraph(context.Background(), d.DB, profileID, nodes, nil, "people-batch:"+people[0].ID)
		}
	}()

	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// nullStr converts an empty string to a sql.NullString with Valid=false,
// otherwise returns a valid NullString.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// boolToInt converts a Go bool to a SQLite-friendly integer (0 or 1).
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullTime converts a zero time.Time to a sql.NullTime with Valid=false,
// otherwise returns a valid NullTime.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
