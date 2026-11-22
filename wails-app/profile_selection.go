// wails-app/profile_selection.go
//
// The profile the window is looking at. It is the app's own UI state: a small
// JSON file next to the database, written and read only by this app. The CLI,
// the MCP server and the extension never learn it, and no setting in the
// database holds it; every CLI call the app makes names the profile with
// --profile (profile_args.go).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/profileorder"
)

// errNoProfile is what every binding that needs a profile returns while none
// is selected, which is only the case before the first profile is created.
var errNoProfile = errors.New("no profile selected: create a profile first")

// errNoProfileForStop is Stop's own no-profile answer: it names the way out that needs no selection.
var errNoProfileForStop = errors.New("no profile selected: select a profile or run `monoagentcli doomsday stop --profile <id>`")

// guiStateFile is the app's UI state file, kept beside the database.
const guiStateFile = "gui-state.json"

type guiState struct {
	SelectedProfile string `json:"selected_profile"`
	// Onboarding holds the first-run onboarding's marks (onboarding_store.go);
	// nil means they were never written.
	Onboarding *onboardingMarks `json:"onboarding,omitempty"`
}

func guiStatePath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), guiStateFile)
}

// loadSelectedProfile returns the saved selection, or "" when nothing is
// saved or the file cannot be read (a damaged file is the same as no choice).
func loadSelectedProfile(path string) string {
	return loadGuiState(path).SelectedProfile
}

// Bounds for the state file Stop reads cold: the file holds one short id and the onboarding's few marks, so anything
// bigger is not ours.
const (
	guiStateMaxBytes = 64 << 10
	profileIDMaxLen  = 128
)

// persistedProfileForStop returns the last selected profile id from the state file, for a Stop that has nothing
// selected in memory (a cold start while signed out). It reads one local file and calls nothing else: no account
// gate, no CLI, no network, and not guiStateMu: Stop holds its busy lock here and must never wait on a writer (a
// rename swaps the whole file, so the read sees the old file or the new one). A symlink, a non-regular or oversized file, bad JSON, or an id that fails the profile-id
// syntax check (profiledir.ValidProfileID plus: no leading dash, no space or control characters, at most
// profileIDMaxLen bytes) gives "": the id goes into the CLI argv, so a tampered file must not pass anything through.
func persistedProfileForStop(path string) string {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > guiStateMaxBytes {
		return ""
	}
	// No-follow, non-blocking open, then check the OPENED file: a path swapped for a symlink or a FIFO between the
	// Lstat above and this open is refused, never followed and never waited on (Stop holds its busy lock here).
	f, err := openChatFileNoFollow(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if ofi, err := f.Stat(); err != nil || !ofi.Mode().IsRegular() || ofi.Size() > guiStateMaxBytes {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, guiStateMaxBytes+1))
	if err != nil || len(b) > guiStateMaxBytes {
		return ""
	}
	var st guiState
	if json.Unmarshal(b, &st) != nil || !stopProfileIDOK(st.SelectedProfile) {
		return ""
	}
	return st.SelectedProfile
}

func stopProfileIDOK(id string) bool {
	if !profiledir.ValidProfileID(id) || len(id) > profileIDMaxLen || strings.HasPrefix(id, "-") || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f || r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// stopProfileID is the profile Stop acts on: the one selected in memory, else the persisted one, else
// errNoProfileForStop. It never changes the selection.
func (a *App) stopProfileID() (string, error) {
	if id, err := a.profileID(); err == nil {
		return id, nil
	}
	if id := persistedProfileForStop(guiStatePath(a.dbPath)); id != "" {
		return id, nil
	}
	return "", errNoProfileForStop
}

// saveSelectedProfile writes the selection and keeps everything else in the
// state file (updateGuiState: a temp file and a rename, so a crash never
// leaves half a file).
func saveSelectedProfile(path, id string) error {
	return updateGuiState(path, func(st *guiState) { st.SelectedProfile = id })
}

// chooseProfile picks the selection from the profiles that exist: the saved
// one while it still exists, otherwise the oldest profile (by created_at in either stored format, ties broken by id,
// so the choice is the same on every start). No profiles is errNoProfile.
func chooseProfile(saved string, profiles []cliProfile) (string, error) {
	if len(profiles) == 0 {
		return "", errNoProfile
	}
	for _, p := range profiles {
		if saved != "" && p.ID == saved {
			return saved, nil
		}
	}
	sorted := append([]cliProfile(nil), profiles...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return profileorder.Older(sorted[i].CreatedAt, sorted[i].ID, sorted[j].CreatedAt, sorted[j].ID)
	})
	return sorted[0].ID, nil
}

// resolveSelectedProfile applies chooseProfile to the saved state and saves
// the result when it differs, returning the selected id.
func resolveSelectedProfile(path string, profiles []cliProfile) (string, error) {
	saved := loadSelectedProfile(path)
	id, err := chooseProfile(saved, profiles)
	if err != nil {
		return "", err
	}
	if id != saved {
		if err := saveSelectedProfile(path, id); err != nil {
			return id, fmt.Errorf("saving the selected profile: %w", err)
		}
	}
	return id, nil
}

// profileID is the selected profile, or errNoProfile. Every binding that acts
// on a profile asks for it here; there is no fallback.
func (a *App) profileID() (string, error) {
	if p := a.selectedProfilePtr.Load(); p != nil && *p != "" {
		return *p, nil
	}
	return "", errNoProfile
}

// selectedProfileOrEmpty is the selected profile for an event payload that
// only tells open pages which profile changed; it never builds a CLI call.
func (a *App) selectedProfileOrEmpty() string {
	id, _ := a.profileID()
	return id
}

// scopedArgs is scopedCLIArgs for the selected profile.
func (a *App) scopedArgs(args ...string) ([]string, error) {
	id, err := a.profileID()
	if err != nil {
		return nil, err
	}
	return scopedCLIArgs(id, args...)
}

// chatCLI is chatSupervisor.cli for the selected profile.
func (a *App) chatCLI(result any, args ...string) error {
	id, err := a.profileID()
	if err != nil {
		return err
	}
	return a.chatSup.cli(id, result, args...)
}

// setSelectedProfile sets the selection in memory and on disk.
func (a *App) setSelectedProfile(id string) error {
	a.selectedProfilePtr.Store(&id)
	return saveSelectedProfile(guiStatePath(a.dbPath), id)
}

// selectProfileAtStartup reads the saved selection and the profile list, and
// selects the saved profile, else the oldest. With no profiles nothing is
// selected and the create-first-profile screen shows.
func (a *App) selectProfileAtStartup() error {
	var rows []cliProfile
	if err := a.listProfilesInto(&rows); err != nil {
		return err
	}
	id, err := resolveSelectedProfile(guiStatePath(a.dbPath), rows)
	if err != nil {
		if errors.Is(err, errNoProfile) {
			a.selectedProfilePtr.Store(nil)
		}
		return err
	}
	a.selectedProfilePtr.Store(&id)
	return nil
}
