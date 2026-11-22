package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Social Lists
// ---------------------------------------------------------------------------

// GetSocialList retrieves a single social list by ID. Returns nil when not
// found.
func (d *Database) GetSocialList(id string) (*SocialList, error) {
	l := &SocialList{}
	var listType sql.NullString
	err := d.DB.QueryRow(`
		SELECT id, list_type, name, item_count, created_at, updated_at
		FROM social_lists WHERE id = ?`, id,
	).Scan(&l.ID, &listType, &l.Name, &l.ItemCount, &l.CreatedAt, &l.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting social list %s: %w", id, err)
	}
	l.ListType = listType.String
	return l, nil
}

// ListSocialLists returns all social lists ordered by creation time.
func (d *Database) ListSocialLists() ([]*SocialList, error) {
	rows, err := d.DB.Query(`
		SELECT id, list_type, name, item_count, created_at, updated_at
		FROM social_lists ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing social lists: %w", err)
	}
	defer rows.Close()

	var lists []*SocialList
	for rows.Next() {
		l := &SocialList{}
		var listType sql.NullString
		if err := rows.Scan(&l.ID, &listType, &l.Name, &l.ItemCount, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning social list row: %w", err)
		}
		l.ListType = listType.String
		lists = append(lists, l)
	}
	return lists, rows.Err()
}

// DeleteSocialList removes a social list by ID. Associated list items are
// removed automatically via ON DELETE CASCADE.
func (d *Database) DeleteSocialList(id string) error {
	result, err := d.DB.Exec("DELETE FROM social_lists WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting social list %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("social list %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Social List Items
// ---------------------------------------------------------------------------

// AddSocialListItem inserts a single item into a social list and increments
// the parent list's item_count.
func (d *Database) AddSocialListItem(item *SocialListItem) error {
	if item.ID == "" {
		item.ID = NewID()
	}

	tx, err := d.DB.Begin()
	if err != nil {
		return fmt.Errorf("beginning add list item transaction: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO social_list_items (
			id, list_id, platform, platform_username, image_url,
			url, full_name, contact_details, follower_count, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.ListID, item.Platform, item.PlatformUsername,
		nullStr(item.ImageURL), nullStr(item.URL), nullStr(item.FullName),
		nullStr(item.ContactDetails), item.FollowerCount, time.Now().UTC(),
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("inserting social list item %s: %w", item.ID, err)
	}

	_, err = tx.Exec("UPDATE social_lists SET item_count = item_count + 1, updated_at = ? WHERE id = ?",
		time.Now().UTC(), item.ListID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("incrementing item_count for list %s: %w", item.ListID, err)
	}

	return tx.Commit()
}

// ListSocialListItems returns all items in a social list.
func (d *Database) ListSocialListItems(listID string) ([]*SocialListItem, error) {
	rows, err := d.DB.Query(`
		SELECT id, list_id, platform, platform_username, image_url,
		       url, full_name, contact_details, follower_count, created_at
		FROM social_list_items
		WHERE list_id = ?
		ORDER BY created_at ASC`, listID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing social list items for %s: %w", listID, err)
	}
	defer rows.Close()

	var items []*SocialListItem
	for rows.Next() {
		it := &SocialListItem{}
		var imageURL, url, fullName, contactDetails sql.NullString
		if err := rows.Scan(
			&it.ID, &it.ListID, &it.Platform, &it.PlatformUsername,
			&imageURL, &url, &fullName, &contactDetails,
			&it.FollowerCount, &it.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning social list item row: %w", err)
		}
		it.ImageURL = imageURL.String
		it.URL = url.String
		it.FullName = fullName.String
		it.ContactDetails = contactDetails.String
		items = append(items, it)
	}
	return items, rows.Err()
}

// BatchAddSocialListItems inserts multiple items into their respective social
// lists within a single transaction and updates the item_count for each
// affected list.
func (d *Database) BatchAddSocialListItems(items []*SocialListItem) error {
	if len(items) == 0 {
		return nil
	}

	tx, err := d.DB.Begin()
	if err != nil {
		return fmt.Errorf("beginning batch list items transaction: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO social_list_items (
			id, list_id, platform, platform_username, image_url,
			url, full_name, contact_details, follower_count, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("preparing batch list item insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	listCounts := make(map[string]int)

	for _, it := range items {
		if it.ID == "" {
			it.ID = NewID()
		}
		if _, err := stmt.Exec(
			it.ID, it.ListID, it.Platform, it.PlatformUsername,
			nullStr(it.ImageURL), nullStr(it.URL), nullStr(it.FullName),
			nullStr(it.ContactDetails), it.FollowerCount, now,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("inserting social list item %s: %w", it.ID, err)
		}
		listCounts[it.ListID]++
	}

	// Update item_count for each affected list.
	for listID, count := range listCounts {
		if _, err := tx.Exec(
			"UPDATE social_lists SET item_count = item_count + ?, updated_at = ? WHERE id = ?",
			count, now, listID,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("updating item_count for list %s: %w", listID, err)
		}
	}

	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Threads
// ---------------------------------------------------------------------------

// GetThread retrieves a single thread by social user ID and platform. Returns
// nil when not found.
func (d *Database) GetThread(socialUserID, platform string) (*Thread, error) {
	t := &Thread{}
	var metadata, messages sql.NullString
	err := d.DB.QueryRow(`
		SELECT id, social_user_id, platform, metadata, messages, created_at, updated_at
		FROM threads
		WHERE social_user_id = ? AND platform = ?`, socialUserID, platform,
	).Scan(&t.ID, &t.SocialUserID, &t.Platform, &metadata, &messages, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting thread %s/%s: %w", platform, socialUserID, err)
	}
	t.Metadata = metadata.String
	t.Messages = messages.String
	return t, nil
}

// ListThreads returns all threads, optionally filtered by platform. Pass an
// empty string to return threads across all platforms.
func (d *Database) ListThreads(platform string) ([]*Thread, error) {
	query := `
		SELECT id, social_user_id, platform, metadata, messages, created_at, updated_at
		FROM threads`
	var args []interface{}
	if platform != "" {
		query += " WHERE platform = ?"
		args = append(args, platform)
	}
	query += " ORDER BY updated_at DESC"

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing threads: %w", err)
	}
	defer rows.Close()

	var threads []*Thread
	for rows.Next() {
		t := &Thread{}
		var metadata, messages sql.NullString
		if err := rows.Scan(&t.ID, &t.SocialUserID, &t.Platform, &metadata, &messages, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning thread row: %w", err)
		}
		t.Metadata = metadata.String
		t.Messages = messages.String
		threads = append(threads, t)
	}
	return threads, rows.Err()
}
