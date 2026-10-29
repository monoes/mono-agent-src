package agentpurposes

import (
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/storage"
)

// Prefix starts the name of every configs row of a purpose setting. It is a
// reserved prefix (internal/operator): no agent writes it.
const Prefix = "agent_purposes."

// Store reads and writes one profile's purpose settings.
type Store struct {
	DB        *storage.Database
	ProfileID string
}

func (s Store) name(purposeID string) string {
	return Prefix + s.ProfileID + "." + purposeID
}

// Loaded is one purpose's stored setting. Err is set when the row is
// unreadable; Setting is then the default.
type Loaded struct {
	Setting Setting
	Err     error
}

// Load reads one purpose's setting: the default when nothing is stored or
// the row is corrupt.
func (s Store) Load(purposeID string) (Loaded, error) {
	if _, ok := Lookup(purposeID); !ok {
		return Loaded{}, unknown(purposeID)
	}
	e, err := s.DB.GetConfig(s.name(purposeID))
	if err != nil {
		return Loaded{}, err
	}
	if e == nil {
		return Loaded{Setting: Default()}, nil
	}
	set, derr := Decode(e.ConfigData)
	return Loaded{Setting: set, Err: derr}, nil
}

// LoadAll reads every purpose's setting, by id, from one snapshot (a single
// statement): a change made while it runs, such as moving a model from one
// purpose to another, is seen whole or not at all.
func (s Store) LoadAll() (map[string]Loaded, error) {
	rows, err := s.DB.ConfigsByPrefix(s.name(""))
	if err != nil {
		return nil, err
	}
	stored := make(map[string]string, len(rows))
	for _, r := range rows {
		stored[r.Name] = r.ConfigData
	}
	out := make(map[string]Loaded, len(builtins))
	for _, p := range builtins {
		data, ok := stored[s.name(p.ID)]
		if !ok {
			out[p.ID] = Loaded{Setting: Default()}
			continue
		}
		set, derr := Decode(data)
		out[p.ID] = Loaded{Setting: set, Err: derr}
	}
	return out, nil
}

// Usable is the settings the resolver uses, by id. A row that cannot be read
// is an error, never the default: the operator's restriction would be
// unknown, and a spawn must not run unrestricted by mistake. The error names
// every unreadable purpose so the operator can reset it; `list` still shows
// such a row as the default with a warning.
func (s Store) Usable() (map[string]Setting, error) {
	all, err := s.LoadAll()
	if err != nil {
		return nil, err
	}
	out := map[string]Setting{}
	var bad []string
	for _, p := range builtins {
		l := all[p.ID]
		if l.Err != nil {
			bad = append(bad, p.ID)
			continue
		}
		out[p.ID] = l.Setting
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("the model set for %s is unreadable: reset it with `agent purposes reset PURPOSE`", strings.Join(bad, ", "))
	}
	return out, nil
}

// Save validates and stores a setting through the storage layer's guarded
// writer, which refuses every caller but the operator.
func (s Store) Save(purposeID string, set Setting) error {
	if _, ok := Lookup(purposeID); !ok {
		return unknown(purposeID)
	}
	v, err := Encode(set)
	if err != nil {
		return err
	}
	return s.DB.SaveConfig(s.name(purposeID), v)
}

// Reset puts a purpose back to the default (nothing chosen).
func (s Store) Reset(purposeID string) error { return s.Save(purposeID, Default()) }

func unknown(id string) error {
	return fmt.Errorf("unknown purpose %q; use one of: %s", id, strings.Join(IDs(), ", "))
}
