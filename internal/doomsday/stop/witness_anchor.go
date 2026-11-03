package stop

import (
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The anchor (note 13.5 section 5): a second witness for the generation, kept in the OS secret store, outside
// the state folder. It is best effort and never a trust root: a run entered where no store can be written is
// `anchor-bound: no`, and the entry dialog says rollback detection is weaker. It never blocks a Stop.

// Errors of a secret store; Anchor maps them onto the rows of the recovery table.
var (
	// ErrNoSecretStore: no secret store exists here (a headless machine with no secret service).
	ErrNoSecretStore = errors.New("stop: there is no OS secret store here, so the run cannot be anchor-bound")
	// ErrStoreLocked: a store exists but cannot be read now (locked keyring). Row 10: hold.
	ErrStoreLocked = errors.New("stop: the secret store is locked or unreachable")
	// ErrItemMissing: the store is readable and the item is not there. Row 11 for a bound generation.
	ErrItemMissing = errors.New("stop: the anchor item is missing")
)

// SecretStore is the OS secret store seen by the anchor.
type SecretStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// NoSecretStore is the refusal used where no secret store exists.
type NoSecretStore struct{}

func (NoSecretStore) Get(string) (string, error) { return "", ErrNoSecretStore }
func (NoSecretStore) Set(string, string) error   { return ErrNoSecretStore }

const anchorService = "monoagent-doomsday-anchor"

// OSSecretStore is the platform keyring (go-keyring). A failed write is reported as ErrNoSecretStore (so the
// run is not anchor-bound); a failed read of an existing store as ErrStoreLocked (hold). It is not exercised by
// the tests, which cannot reach a real secret service.
type OSSecretStore struct{}

func (OSSecretStore) Get(key string) (string, error) {
	v, err := keyring.Get(anchorService, key)
	switch {
	case err == nil:
		return v, nil
	case errors.Is(err, keyring.ErrNotFound):
		return "", ErrItemMissing
	}
	return "", fmt.Errorf("%w: %v", ErrStoreLocked, err)
}

func (OSSecretStore) Set(key, value string) error {
	if err := keyring.Set(anchorService, key, value); err != nil {
		return fmt.Errorf("%w: %v", ErrNoSecretStore, err)
	}
	return nil
}

// FileSecretStore keeps items as files in a directory. It exists for tests: a file store in the home directory
// is rolled back together with it by a home-directory restore, which is the documented limit of the anchor.
type FileSecretStore struct {
	Dir    string
	Locked bool // simulate a locked keyring: reads fail
	mu     sync.Mutex
}

func (f *FileSecretStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Locked {
		return "", ErrStoreLocked
	}
	b, err := os.ReadFile(filepath.Join(f.Dir, key))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrItemMissing
	}
	return string(b), err
}

func (f *FileSecretStore) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return os.WriteFile(filepath.Join(f.Dir, key), []byte(value), 0o600)
}

// Anchor states: forward only.
const (
	AnchorEntering = "entering"
	AnchorRunning  = "running"
	AnchorEnded    = "ended"
)

func anchorRank(s string) int {
	switch s {
	case AnchorEntering:
		return 1
	case AnchorRunning:
		return 2
	case AnchorEnded:
		return 3
	}
	return 0
}

// Anchor reads and writes the profile's anchor item. The value carries a MAC over (profile, generation, state)
// so an edited item is not believed.
type Anchor struct {
	Store   SecretStore
	Keys    store.Keyer
	Profile string
}

type anchorItem struct {
	Gen   int64  `json:"g"`
	State string `json:"s"`
	MAC   string `json:"m"`
}

func (a Anchor) key() string { return "anchor-" + a.Profile }

func (a Anchor) mac(gen int64, state string) (string, error) {
	m, err := a.Keys.MAC("anchor", []byte(a.Profile), []byte(strconv.FormatInt(gen, 10)), []byte(state))
	return hex.EncodeToString(m), err
}

// Read returns the anchor as the recovery table needs it. A missing item is Found=false; an item whose MAC
// does not verify is also Found=false (a forged or edited anchor is worth nothing, and a bound generation then
// fails closed as row 11); a store that cannot be read is an error (row 10: hold).
func (a Anchor) Read() (AnchorState, error) {
	raw, err := a.Store.Get(a.key())
	switch {
	case errors.Is(err, ErrItemMissing):
		return AnchorState{}, nil
	case err != nil:
		return AnchorState{}, err
	}
	var it anchorItem
	if json.Unmarshal([]byte(raw), &it) != nil || anchorRank(it.State) == 0 {
		return AnchorState{}, nil
	}
	want, err := a.mac(it.Gen, it.State)
	if err != nil || !hmac.Equal([]byte(want), []byte(it.MAC)) {
		return AnchorState{}, nil
	}
	return AnchorState{Found: true, Generation: it.Gen, Ended: it.State == AnchorEnded}, nil
}

// Func adapts the anchor to Freshness.Anchor.
func (a Anchor) Func() func() (AnchorState, error) { return a.Read }

// Set writes the state for a generation. It refuses to go backwards: a lower generation, or a lower state of
// the same generation.
func (a Anchor) Set(gen int64, state string) error {
	if anchorRank(state) == 0 {
		return fmt.Errorf("stop: unknown anchor state %q", state)
	}
	if raw, err := a.Store.Get(a.key()); err == nil {
		var it anchorItem
		if json.Unmarshal([]byte(raw), &it) == nil && anchorRank(it.State) > 0 {
			if want, _ := a.mac(it.Gen, it.State); hmac.Equal([]byte(want), []byte(it.MAC)) &&
				(gen < it.Gen || (gen == it.Gen && anchorRank(state) < anchorRank(it.State))) {
				return fmt.Errorf("stop: the anchor only moves forward (%d/%s -> %d/%s)", it.Gen, it.State, gen, state)
			}
		}
	}
	m, err := a.mac(gen, state)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(anchorItem{Gen: gen, State: state, MAC: m})
	return a.Store.Set(a.key(), string(b))
}

// Enter is step 1 of the entry (section 5): write the item for the new generation in state `entering` and read
// it back. `bound` is true only if both succeeded; otherwise the run is `anchor-bound: no` and why says so for
// the entry dialog. It never fails the entry.
func (a Anchor) Enter(gen int64) (bound bool, why string) {
	if err := a.Set(gen, AnchorEntering); err != nil {
		return false, "rollback detection is weaker: the anchor could not be written (" + err.Error() + ")"
	}
	got, err := a.Read()
	if err != nil || !got.Found || got.Generation != gen {
		return false, "rollback detection is weaker: the anchor could not be read back"
	}
	return true, ""
}
