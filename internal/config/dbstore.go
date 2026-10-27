package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
)

// DBConfigStore adapts *storage.Database to satisfy the config.ConfigStore
// interface. It bridges the storage layer's ConfigEntry (raw JSON string) into
// the config package's *Config type.
type DBConfigStore struct {
	DB *storage.Database
}

// GetConfig retrieves a config by name from the database, parsing the stored
// JSON into a *Config. Returns (nil, nil) when not found.
func (s *DBConfigStore) GetConfig(name string) (*Config, error) {
	entry, err := s.DB.GetConfig(name)
	if err != nil {
		return nil, fmt.Errorf("db get config %s: %w", name, err)
	}
	if entry == nil {
		return nil, nil
	}

	var cfg Config
	if err := json.Unmarshal([]byte(entry.ConfigData), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config %s from db: %w", name, err)
	}
	return &cfg, nil
}

// SaveConfig persists a config to the database. It is the store for the
// selector configs the engine generates, never the operator's settings
// path, so a reserved setting (operator.ReservedConfig) is refused,
// whoever names it.
func (s *DBConfigStore) SaveConfig(name string, configData string) error {
	if operator.ReservedConfig(name) {
		return reservedConfigError(name)
	}
	return s.DB.SaveConfig(name, configData)
}

// reservedConfigError refuses a write of reserved setting name here.
func reservedConfigError(name string) error {
	return fmt.Errorf("config %q is a reserved setting (it changes what agents may do or what runs unattended): only the operator writes it, with `monoagentcli config set` or the command made for it in their own terminal", strings.TrimSpace(name))
}
