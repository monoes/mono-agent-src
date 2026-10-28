package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------

// CreateTemplate inserts a new template row. The auto-incremented ID is set on
// the provided struct after insertion.
func (d *Database) CreateTemplate(tmpl *Template) error {
	now := time.Now().UTC()
	result, err := d.DB.Exec(`
		INSERT INTO templates (name, subject, body, metadata, created_at, updated_at)
		VALUES (?,?,?,?,?,?)`,
		tmpl.Name, nullStr(tmpl.Subject), tmpl.Body, nullStr(tmpl.Metadata), now, now,
	)
	if err != nil {
		return fmt.Errorf("creating template: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("getting template ID: %w", err)
	}
	tmpl.ID = int(id)
	tmpl.CreatedAt = now
	tmpl.UpdatedAt = now
	return nil
}

// GetTemplate retrieves a single template by ID. Returns nil when not found.
func (d *Database) GetTemplate(id int) (*Template, error) {
	t := &Template{}
	var subject, metadata sql.NullString
	err := d.DB.QueryRow(`
		SELECT id, name, subject, body, metadata, created_at, updated_at
		FROM templates WHERE id = ?`, id,
	).Scan(&t.ID, &t.Name, &subject, &t.Body, &metadata, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting template %d: %w", id, err)
	}
	t.Subject = subject.String
	t.Metadata = metadata.String
	return t, nil
}

// ListTemplates returns all templates ordered by name.
func (d *Database) ListTemplates() ([]*Template, error) {
	rows, err := d.DB.Query(`
		SELECT id, name, subject, body, metadata, created_at, updated_at
		FROM templates ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	defer rows.Close()

	var templates []*Template
	for rows.Next() {
		t := &Template{}
		var subject, metadata sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &subject, &t.Body, &metadata, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning template row: %w", err)
		}
		t.Subject = subject.String
		t.Metadata = metadata.String
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

// DeleteTemplate removes a template by ID.
func (d *Database) DeleteTemplate(id int) error {
	result, err := d.DB.Exec("DELETE FROM templates WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting template %d: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("template %d not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Configs
// ---------------------------------------------------------------------------

// ConfigWriteGuard, when set, is asked before every write of a configs row
// through this package and refuses the write with its error. Whoever owns the
// rule that says which rows only the operator may write installs it once at
// startup (internal/config does; this package cannot import it). Nil, as in a
// test that builds a raw database, allows every write.
var ConfigWriteGuard func(name string) error

// SaveConfig inserts or replaces a named configuration entry. It consults
// ConfigWriteGuard first.
func (d *Database) SaveConfig(name, configData string) error {
	if g := ConfigWriteGuard; g != nil {
		if err := g(name); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	_, err := d.DB.Exec(`
		INSERT INTO configs (name, config_data, created_at, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(name)
		DO UPDATE SET config_data = excluded.config_data,
		              updated_at  = excluded.updated_at`,
		name, configData, now, now,
	)
	if err != nil {
		return fmt.Errorf("saving config %s: %w", name, err)
	}
	return nil
}

// GetConfig retrieves a single configuration entry by name. Returns nil when
// not found.
func (d *Database) GetConfig(name string) (*ConfigEntry, error) {
	c := &ConfigEntry{}
	err := d.DB.QueryRow(`
		SELECT name, config_data, created_at, updated_at
		FROM configs WHERE name = ?`, name,
	).Scan(&c.Name, &c.ConfigData, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting config %s: %w", name, err)
	}
	return c, nil
}

// ListConfigs returns all configuration entries.
func (d *Database) ListConfigs() ([]*ConfigEntry, error) {
	rows, err := d.DB.Query("SELECT name, config_data, created_at, updated_at FROM configs ORDER BY name ASC")
	if err != nil {
		return nil, fmt.Errorf("listing configs: %w", err)
	}
	defer rows.Close()

	var configs []*ConfigEntry
	for rows.Next() {
		c := &ConfigEntry{}
		if err := rows.Scan(&c.Name, &c.ConfigData, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning config row: %w", err)
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// GetSetting retrieves a single setting value by key. Returns an error if the
// key does not exist.
func (d *Database) GetSetting(key string) (string, error) {
	var value string
	err := d.DB.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("setting %q not found", key)
	}
	if err != nil {
		return "", fmt.Errorf("getting setting %s: %w", key, err)
	}
	return value, nil
}

// SetSetting inserts or replaces a key-value setting.
func (d *Database) SetSetting(key, value string) error {
	_, err := d.DB.Exec(`
		INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("setting %s: %w", key, err)
	}
	return nil
}
