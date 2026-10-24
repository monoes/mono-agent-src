package automation

import (
	_ "embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Official package hashes (#900). A package whose trust tier is "builtin"
// (an embedded built-in of an older release, or an official monoes.me item)
// is trusted for what an agent may run without the user, so the tier cannot
// rest on files an agent can also write: the index and the package files
// both live under ~/.monoagent. The hashes of the official packages ship in
// the binary instead, and a package whose files do not hash to one of them
// is treated as imported, whatever its index entry says.
//
// A legitimate change to a built-in never keeps the tier: editing it lowers
// the entry's trust (lowerTrust), and selector fixes go to the overlay. So a
// "builtin" entry that fails this check was altered outside the app.
//
// The check reads the version dir into memory once (verifiedOfficial): the
// tree must hold only regular files and directories (no symlinks, special
// files or dotfiles, which the hash would not cover but a run could read),
// its hash must be filed for the version the tree itself names, that version
// must be the one the index records, and it must not be below the package's
// minimum trusted version (so an old, vulnerable release is not built-in
// again). The package a run gets is that snapshot, so what was hashed is
// what runs.
//
// official_hashes.json maps package id to {"min_version", "hashes"}, hashes
// being tree hash -> release version ("*" for a hash filed before versions
// were recorded: any version at or above the minimum). `go test
// ./internal/automation -run TestOfficialHashes -update` adds the hashes of
// the automations/ sources; TestOfficialHashesCoverSources fails until a
// release has done so.

//go:embed official_hashes.json
var officialHashesJSON []byte

var officialHashes, officialMin = mustOfficialHashes(officialHashesJSON)

type officialEntry struct {
	MinVersion string            `json:"min_version,omitempty"`
	Hashes     map[string]string `json:"hashes"`
}

func mustOfficialHashes(b []byte) (map[string]map[string]string, map[string]string) {
	var raw map[string]officialEntry
	if err := json.Unmarshal(b, &raw); err != nil {
		panic("automation: official_hashes.json: " + err.Error())
	}
	hashes := make(map[string]map[string]string, len(raw))
	mins := make(map[string]string, len(raw))
	for id, e := range raw {
		hashes[id] = e.Hashes
		if e.MinVersion != "" {
			mins[id] = e.MinVersion
		}
	}
	return hashes, mins
}

// isOfficialRelease reports whether h is the hash of the official release
// of id at version, and that release is not below the package's minimum.
func isOfficialRelease(id, version, h string) bool {
	v, ok := officialHashes[id][h]
	if !ok || (v != "*" && v != version) {
		return false
	}
	if min := officialMin[id]; min != "" {
		have, ok1 := parseSemver(version)
		floor, ok2 := parseSemver(min)
		if !ok1 || !ok2 || have.cmp(floor) < 0 {
			return false
		}
	}
	return true
}

// readPlainTree reads dir into memory, refusing anything that is not a
// regular file or directory (the walk does not follow links) and any
// dot-named entry.
func readPlainTree(dir string) (map[string][]byte, bool) {
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		return nil, false
	}
	files := map[string][]byte{}
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return fs.ErrInvalid
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fs.ErrInvalid
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); info.Size() > MaxFileBytes || total > MaxTotalBytes || len(files) >= MaxFiles {
			return fs.ErrInvalid
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	return files, err == nil
}

// verifiedOfficial reads e's version dir and reports the snapshot of it when
// it is an official release of id at the version the index records.
func (r *Registry) verifiedOfficial(id string, e *indexEntry) (fs.FS, bool) {
	files, ok := readPlainTree(r.versionDir(id, e.Version))
	if !ok {
		return nil, false
	}
	delete(files, ChecksumsFile)
	snap := mapFS(files)
	m, err := readManifest(snap)
	if err != nil || m.ID != id || m.Version != e.Version {
		return nil, false
	}
	if !isOfficialRelease(id, m.Version, treeHash(files)) {
		return nil, false
	}
	return snap, true
}

// effectiveTrust is the trust tier e's package may act with. Anything but
// "builtin" is returned as recorded; "builtin" holds only while the
// version's files are an official release, else it falls to imported.
func (r *Registry) effectiveTrust(id string, e *indexEntry) string {
	t, _ := r.effectiveTrustFS(id, e)
	return t
}

// effectiveTrustFS is effectiveTrust plus, for a verified built-in, the
// snapshot that was hashed.
func (r *Registry) effectiveTrustFS(id string, e *indexEntry) (string, fs.FS) {
	t := e.trust()
	if t != TrustBuiltin {
		return t, nil
	}
	if snap, ok := r.verifiedOfficial(id, e); ok {
		return t, snap
	}
	return TrustImported, nil
}

// UnshippedOfficialWarning is the warning for an installed package that is
// filed as built-in (an official library item) but is not a release this
// binary ships a hash for, so agents are refused it: "" when it is trusted or
// not filed as built-in.
func (r *Registry) UnshippedOfficialWarning(id string) string {
	e, err := r.entry(id)
	if err != nil || e.Removed || e.trust() != TrustBuiltin {
		return ""
	}
	if r.effectiveTrust(id, e) == TrustBuiltin {
		return ""
	}
	return "official package " + id + " " + e.Version + " is newer than the releases this app knows (or its files differ), so agents are refused it as an imported package until you update the app"
}
