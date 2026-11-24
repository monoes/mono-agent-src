package browserctl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// State is what one context (ContextKey) remembers between commands:
// ~/.monoagent/browser/sessions/<context-key>.json.
type State struct {
	// TabID and Instance are the last tab the context used and the browser
	// it lives in; commands without --tab act on it.
	TabID    int    `json:"tabId,omitempty"`
	Instance string `json:"instance,omitempty"`
	// Profile is the profile that last used TabID. One browser instance can
	// serve several profiles, so a command naming another profile must not
	// land on that tab. Files written before this field existed have none
	// and are not reused for a command that names a profile.
	Profile string `json:"profile,omitempty"`
	// Reported is, per tab ("<instance>/<tabId>"), the submit_guard seq of
	// the last blocked submit already turned into a HIL item.
	Reported map[string]int `json:"reported,omitempty"`
	// Loads is, per tab, where its console and network buffers stood when
	// a browser command last loaded a page in it.
	Loads     map[string]LoadMark `json:"loads,omitempty"`
	UpdatedAt string              `json:"updatedAt,omitempty"`
}

// LoadMark is the newest console and network seq before a page load.
type LoadMark struct {
	Console int `json:"console"`
	Network int `json:"network"`
}

// StateDir is where the session state files live.
func StateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "browser", "sessions"), nil
}

// LoadState reads key's state; a missing or unreadable file is an empty one.
func LoadState(key string) State {
	var st State
	dir, err := StateDir()
	if err != nil {
		return st
	}
	raw, err := os.ReadFile(filepath.Join(dir, safeKey(key)+".json"))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	return st
}

// SaveState writes key's state (0600, replaced atomically) under the
// session-state lock, replacing what is on disk.
func SaveState(key string, st State) error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	unlock, err := lockStateDir(dir)
	if err != nil {
		return err
	}
	defer unlock()
	return writeState(dir, key, st)
}

// SaveStateFrom saves st, the state a command changed, which it loaded as
// base. Under the lock it re-reads the file and keeps what another command
// saved meanwhile: a field or map entry this command changed (or deleted)
// wins, the rest follows the file. Without it two commands of one context
// lose each other's Reported/Loads/TabID updates.
func SaveStateFrom(key string, base, st State) error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	unlock, err := lockStateDir(dir)
	if err != nil {
		return err
	}
	defer unlock()
	return writeState(dir, key, mergeState(base, st, LoadState(key)))
}

// cloneState copies st's maps, so a later change to one is not seen in the other.
func cloneState(st State) State {
	out := st
	out.Reported = mergeMap(nil, st.Reported, nil)
	out.Loads = mergeMap(nil, st.Loads, nil)
	return out
}

// mergeState is the three-way merge of ours and disk against base.
func mergeState(base, ours, disk State) State {
	out := disk
	if ours.TabID != base.TabID || ours.Instance != base.Instance || ours.Profile != base.Profile {
		out.TabID, out.Instance, out.Profile = ours.TabID, ours.Instance, ours.Profile
	}
	out.Reported = mergeMap(base.Reported, ours.Reported, disk.Reported)
	out.Loads = mergeMap(base.Loads, ours.Loads, disk.Loads)
	return out
}

func mergeMap[V comparable](base, ours, disk map[string]V) map[string]V {
	out := make(map[string]V, len(disk)+len(ours))
	for k, v := range disk {
		out[k] = v
	}
	for k, v := range ours {
		if bv, ok := base[k]; !ok || bv != v {
			out[k] = v
		}
	}
	for k := range base {
		if _, ok := ours[k]; !ok {
			delete(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// writeState writes key's file atomically; the caller holds the lock.
func writeState(dir, key string, st State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, safeKey(key)+".json")
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("saving browser session state: %w", err)
	}
	return nil
}

// tabKey keys State.Reported.
func tabKey(instance string, tabID int) string { return instance + "/" + strconv.Itoa(tabID) }
