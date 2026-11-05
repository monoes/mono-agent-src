package stop

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunDir is the per-profile run directory: the stop marker, the per-run secret, the key-free unit file and the
// clean-shutdown record. Nothing in it needs the database or the vault key.
type RunDir struct {
	Dir  string
	FS   FS
	Hook Hook
}

func (r RunDir) fsys() FS {
	if r.FS == nil {
		return OSFS{}
	}
	return r.FS
}

// StopPath is named by generation, and a generation is never reused, so a stale marker cannot stop a new run.
func (r RunDir) StopPath(gen uint64) string { return filepath.Join(r.Dir, fmt.Sprintf("%d.stop", gen)) }
func (r RunDir) SecretPath(gen uint64) string {
	return filepath.Join(r.Dir, fmt.Sprintf("%d.secret", gen))
}
func (r RunDir) UnitPath(gen uint64) string { return filepath.Join(r.Dir, fmt.Sprintf("%d.unit", gen)) }

// HasRunTraces says a run may have been entered on this install: the run directory holds a per-run secret, the witness or the
// generation counter (each made by a run and none deleted by one), or it cannot be listed for a reason other than not
// existing. A directory that does not exist is a clean "no": nothing creates it before a run does. It is one directory
// listing, made only when the database could not be read.
//
// It is evidence, not proof: it assumes the run directory is not cleaned by hand. Someone who can delete all three kinds of
// file as the same user, while the database is unreadable and this process never saw the run, turns the answer back into "no".
func (r RunDir) HasRunTraces() bool {
	names, err := r.fsys().ReadDir(r.Dir)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".secret") || n == "witness" || n == "generation-counter" {
			return true
		}
	}
	return false
}

// CreateSecret generates the per-run random secret (not derived from the vault key) with mode 0600.
func (r RunDir) CreateSecret(gen uint64) error {
	if err := r.fsys().MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	return writeDurable(r.fsys(), r.Dir, r.SecretPath(gen), []byte(hex.EncodeToString(b)))
}

// ReadSecret returns the secret, or an explicit error (the caller then writes an unsigned marker).
func (r RunDir) ReadSecret(gen uint64) ([]byte, error) {
	r.Hook.at("secret.read")
	b, err := r.fsys().ReadFile(r.SecretPath(gen))
	if err != nil {
		return nil, fmt.Errorf("stop: the per-run secret is unreadable: %w", err)
	}
	if len(b) == 0 {
		return nil, errors.New("stop: the per-run secret is empty")
	}
	return b, nil
}

// MarkerStatus is what reading the marker of one generation found.
type MarkerStatus int

const (
	MarkerAbsent MarkerStatus = iota + 1
	MarkerValid
	// MarkerUnsigned: written while the secret was unreadable; durable, and it still stops its generation.
	MarkerUnsigned
	// MarkerCorrupt: unreadable, unparsable, wrong generation or a bad MAC. It counts as a stop for its
	// generation (the run is refused, not resumed) and is reported.
	MarkerCorrupt
)

func (s MarkerStatus) String() string {
	return [...]string{"?", "absent", "valid", "unsigned", "corrupt"}[s]
}

// Stops reports whether the status means the generation is stopped: everything except absent. Unsigned is not
// harmless and corrupt is not ignored: both fail closed for their own generation.
func (s MarkerStatus) Stops() bool { return s != MarkerAbsent }

type markerFile struct {
	Version    int    `json:"v"`
	Generation uint64 `json:"generation"`
	CharterID  string `json:"charter_id"`
	Cause      string `json:"cause"`
	Unsigned   bool   `json:"unsigned,omitempty"`
	MAC        string `json:"mac,omitempty"`
}

func markerMAC(secret []byte, m markerFile) string {
	h := hmac.New(sha256.New, secret)
	fmt.Fprintf(h, "monoagent-doomsday-stop-marker-v1\x00%d\x00%s\x00%s", m.Generation, m.CharterID, m.Cause)
	return hex.EncodeToString(h.Sum(nil))
}

// MarkerResult is the outcome of WriteMarker.
type MarkerResult struct {
	Status  MarkerStatus
	Existed bool // an earlier Stop had already created it (a repeated Stop is idempotent)
}

// WriteMarker creates the marker atomically (create-exclusive, then fsync of the file and the directory). It
// needs no database and no key. An error means NOT RECORDED. A repeated Stop whose create hits an existing
// marker is idempotent success after the content is verified; unverified content is the corrupt case, which is
// reported but still counts as a stop.
func (r RunDir) WriteMarker(gen uint64, charterID, cause string) (MarkerResult, error) {
	m := markerFile{Version: 1, Generation: gen, CharterID: charterID, Cause: cause}
	secret, serr := r.ReadSecret(gen)
	if serr != nil {
		m.Unsigned = true
	} else {
		m.MAC = markerMAC(secret, m)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return MarkerResult{}, err
	}
	if err := r.fsys().MkdirAll(r.Dir, 0o700); err != nil {
		return MarkerResult{}, err
	}
	err = writeDurable(r.fsys(), r.Dir, r.StopPath(gen), data)
	switch {
	case err == nil:
		r.Hook.at("marker.created")
		if m.Unsigned {
			return MarkerResult{Status: MarkerUnsigned}, nil
		}
		return MarkerResult{Status: MarkerValid}, nil
	case errors.Is(err, os.ErrExist):
		return MarkerResult{Status: r.ReadMarker(gen, charterID), Existed: true}, nil
	}
	return MarkerResult{}, err
}

// ReadMarker classifies the marker of one generation. A marker belongs to its generation only.
func (r RunDir) ReadMarker(gen uint64, charterID string) MarkerStatus {
	b, err := r.fsys().ReadFile(r.StopPath(gen))
	if errors.Is(err, os.ErrNotExist) {
		return MarkerAbsent
	}
	if err != nil {
		return MarkerCorrupt
	}
	var m markerFile
	if json.Unmarshal(b, &m) != nil || m.Version != 1 || m.Generation != gen {
		return MarkerCorrupt
	}
	if charterID != "" && m.CharterID != charterID {
		return MarkerCorrupt
	}
	if m.Unsigned {
		return MarkerUnsigned
	}
	secret, err := r.ReadSecret(gen)
	if err != nil {
		return MarkerUnsigned // cannot be authenticated: reported, and still a stop
	}
	if !hmac.Equal([]byte(m.MAC), []byte(markerMAC(secret, m))) {
		return MarkerCorrupt
	}
	return MarkerValid
}

// Action is what the host does when the marker and the database disagree.
type Action int

const (
	ActionNone Action = iota + 1
	// ActionAdvanceDB: the marker wins; the host reconciles the database forward.
	ActionAdvanceDB
	// ActionRecreateMarker: the database wins; the marker is recreated for the record.
	ActionRecreateMarker
)

// Reconcile decides by the marker and the database state of one generation. Every disagreement fails closed
// for the affected generation only.
func Reconcile(db RunState, marker MarkerStatus) Action {
	switch {
	case marker.Stops() && db == StateRunning:
		return ActionAdvanceDB
	case !marker.Stops() && db != StateRunning:
		return ActionRecreateMarker
	}
	return ActionNone
}
