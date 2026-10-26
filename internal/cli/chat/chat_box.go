package chat

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/monoes/mono-agent/internal/agentbox"
	"github.com/monoes/mono-agent/internal/ai"
)

// The values of coder.box.
const (
	BoxOff    = "off"
	BoxCompat = "compat"
	BoxStrict = "strict"
)

// Codes of the typed errors a boxed coder turn can end in.
const (
	CodeBoxNeedsAPIMode = "box_needs_api_mode"
	CodeBoxOrgUnsupport = "box_org_unsupported"
	CodeBoxInvalid      = "box_setting_invalid"
	CodeBoxDBPath       = "box_db_path_unresolved"
)

type boxError struct{ code, msg string }

func (e *boxError) Error() string                   { return e.msg }
func (e *boxError) JSONErrorFields() map[string]any { return map[string]any{"code": e.code} }

// boxMode is coder.box as stored: absent is off, and any other value is returned as it is,
// so status shows a tampered one and coderBox refuses it.
func (s coderSettings) boxMode() string {
	if s.Box == "" {
		return BoxOff
	}
	return s.Box
}

// coderBox is the sandbox a coder lead turn runs in, or nil when coder.box is off (the
// default, which leaves the turn exactly as it was). Only coding access can run boxed:
// workspace and full access use monoagentcli with the user's database, which the box
// hides until the API client mode exists, and a dynamic org's workers would run outside
// the box the lead is in.
func coderBox(s coderSettings, access, cwd, runtime, orgMode, dbPath string) (*agentbox.Box, error) {
	mode := s.boxMode()
	switch mode {
	case BoxOff:
		return nil, nil
	case BoxCompat, BoxStrict:
	default:
		return nil, &boxError{CodeBoxInvalid, "coder.box holds " + strconv.Quote(mode) + ", which is not off, compat or strict: " +
			"no turn runs until the operator sets it again (coder set --box)"}
	}
	if access == "" {
		access = ai.AccessFull // an empty access is full
	}
	if access != ai.AccessCoding {
		return nil, &boxError{CodeBoxNeedsAPIMode, "the sandbox (coder.box) is on, and a " + access + "-access turn needs monoagentcli with your database, " +
			"which the sandbox hides until the API client mode exists: use coding access or turn coder.box off"}
	}
	if orgMode == ai.OrgModeDynamic {
		return nil, &boxError{CodeBoxOrgUnsupport, "the sandbox (coder.box) is on, and a dynamic org's workers would run outside it: turn the org mode off or coder.box off"}
	}
	box := &agentbox.Box{Mode: agentbox.Mode(mode), Cwd: cwd, Runtime: runtime}
	if dbPath != "" {
		prot, err := protectedDatabase(dbPath)
		if err != nil {
			return nil, &boxError{CodeBoxDBPath, "the sandbox (coder.box) cannot tell where the database is, so it cannot hide it: " + err.Error()}
		}
		box.Protect = prot
	}
	return box, nil
}

// protectedDatabase is every name under which the database in use and its journal files can
// be reached: the path as the opener understands it (a leading ~ expanded, made absolute) and
// the path it really is after symlinks, each with its -wal and -shm. Anything but a missing
// file that stops the path from resolving is an error: an unhidden database is worse than no
// turn.
func protectedDatabase(dbPath string) ([]string, error) {
	abs, err := filepath.Abs(expandHome(dbPath))
	if err != nil {
		return nil, err
	}
	names := []string{abs}
	real, err := filepath.EvalSymlinks(abs)
	switch {
	case err == nil:
		names = append(names, real)
	case errors.Is(err, fs.ErrNotExist):
		// Not created yet: the directory it will be created in must resolve, and the
		// database lands in its real place.
		dir, derr := filepath.EvalSymlinks(filepath.Dir(abs))
		if derr != nil && !errors.Is(derr, fs.ErrNotExist) {
			return nil, derr
		}
		if derr == nil {
			names = append(names, filepath.Join(dir, filepath.Base(abs)))
		}
	default:
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		for _, p := range []string{n, n + "-wal", n + "-shm"} {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out, nil
}
