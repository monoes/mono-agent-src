package extension

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Per-browser pairing tokens, minted by the zero-step claim flow
// (autopair.go). Each browser that claims gets its own token, recorded here
// with a label so it can be listed and revoked on its own
// (`extension browsers list|revoke`) without resetting the shared
// extension.token every other browser and CLI process uses.
//
// Only a SHA-256 of each token is stored: the file says who is paired, it
// cannot be used to connect. The bridge and the CLI are different processes,
// so the file is the source of truth and every check reads it; a revocation
// takes effect at the next handshake and, for a socket that is already open,
// at the next sweepRevoked (every ping interval).

// maxPairedBrowsers bounds the file. At the cap a new pairing is REFUSED
// (ErrBrowserLimit), never made by evicting an existing one: a loop of claims
// must not be able to push a valid browser out. The owner revokes to make room.
const maxPairedBrowsers = 32

// maxFieldLen caps the stored Label/Origin/Agent, which a claimant chooses.
const maxFieldLen = 256

// PairedBrowser is one browser the claim flow has paired.
type PairedBrowser struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Origin    string    `json:"origin"`
	Agent     string    `json:"agent,omitempty"`
	Instance  string    `json:"instance,omitempty"`
	TokenHash string    `json:"tokenHash"`
	PairedAt  time.Time `json:"pairedAt"`
	// Restricted limits which profiles this browser's token may claim to
	// AllowedProfiles (allowed_profiles.go). A record written before the
	// limit existed has neither field and is unrestricted.
	Restricted      bool     `json:"restricted,omitempty"`
	AllowedProfiles []string `json:"allowedProfiles,omitempty"`
}

// ErrNoSuchBrowser means a revoke named no paired browser (or several).
var ErrNoSuchBrowser = errors.New("no such paired browser")

var (
	// ErrBrowserLimit: the file holds maxPairedBrowsers entries.
	ErrBrowserLimit = errors.New("the limit of paired browsers is reached: revoke one with `monoagentcli extension browsers revoke <id>`")
	// ErrInstancePaired: a browser with this instance id is already paired and
	// the request neither holds its token nor was approved by the owner, so it
	// may not replace it (a forged claim must not kill a victim's token).
	ErrInstancePaired = errors.New("a browser with this instance id is already paired: revoke it first with `monoagentcli extension browsers revoke <id>`")
	// ErrClaimConsumed: the approved claim already produced its token.
	ErrClaimConsumed = errors.New("this pairing claim was already used")
)

// SafeText removes what makes text unsafe to print: control characters (an
// ESC sequence rewrites a terminal; tabs and newlines become a space) and
// format characters (the bidi overrides U+202A-202E and U+2066-2069 reorder
// what follows), plus the line and paragraph separators. A browser chooses its
// label and its Origin header, so everything it supplied goes through here
// before it is stored, logged or shown.
func SafeText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			return -1
		}
		return r
	}, s)
}

// clip is SafeText bounded to maxFieldLen bytes.
func clip(s string) string {
	s = strings.ToValidUTF8(SafeText(s), "")
	if len(s) > maxFieldLen {
		return strings.ToValidUTF8(s[:maxFieldLen], "")
	}
	return s
}

func browsersPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".monoagent", "extension.browsers.json"), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func readBrowsersLocked() ([]PairedBrowser, error) {
	path, err := browsersPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read paired browsers: %w", err)
	}
	var list []PairedBrowser
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("paired browsers file is unreadable: %w", err)
	}
	return list, nil
}

func writeBrowsersLocked(list []PairedBrowser) error {
	path, err := browsersPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write paired browsers: %w", err)
	}
	return os.Rename(tmp, path)
}

// ListPairedBrowsers returns the browsers the claim flow has paired, oldest first.
func ListPairedBrowsers() ([]PairedBrowser, error) {
	unlock, err := lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	list, err := readBrowsersLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].PairedAt.Before(list[j].PairedAt) })
	return list, nil
}

// RevokePairedBrowser removes one paired browser, named by its id or a unique
// prefix of it (4+ characters). Its token stops working at once for new
// connections.
func RevokePairedBrowser(idOrPrefix string) (PairedBrowser, error) {
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if len(idOrPrefix) < 4 {
		return PairedBrowser{}, fmt.Errorf("%w: give at least 4 characters of the id", ErrNoSuchBrowser)
	}
	unlock, err := lockState()
	if err != nil {
		return PairedBrowser{}, err
	}
	defer unlock()
	list, err := readBrowsersLocked()
	if err != nil {
		return PairedBrowser{}, err
	}
	hit := -1
	for i, b := range list {
		if b.ID == idOrPrefix {
			hit = i
			break
		}
		if strings.HasPrefix(b.ID, idOrPrefix) {
			if hit != -1 {
				return PairedBrowser{}, fmt.Errorf("%w: %q matches more than one browser", ErrNoSuchBrowser, idOrPrefix)
			}
			hit = i
		}
	}
	if hit == -1 {
		return PairedBrowser{}, fmt.Errorf("%w: %q", ErrNoSuchBrowser, idOrPrefix)
	}
	gone := list[hit]
	list = append(list[:hit], list[hit+1:]...)
	if err := writeBrowsersLocked(list); err != nil {
		return PairedBrowser{}, err
	}
	return gone, nil
}

// mintOpts says how addPairedBrowser may treat an existing entry.
type mintOpts struct {
	// ID, when set, is the new entry's id (the approved claim's), so a claim
	// can mint at most once.
	ID string
	// RotateToken is the current token of the entry sharing b.Instance; a
	// match lets the new token replace it.
	RotateToken string
	// ReplaceInstance lets the owner's approval replace the entry sharing b.Instance.
	ReplaceInstance bool
}

// addPairedBrowser mints a token for b. It never evicts: at the cap it fails
// with ErrBrowserLimit, and an entry sharing b.Instance is replaced only with
// its own token (o.RotateToken) or the owner's approval (o.ReplaceInstance).
func addPairedBrowser(b PairedBrowser, o mintOpts) (token string, _ PairedBrowser, _ error) {
	unlock, err := lockState()
	if err != nil {
		return "", PairedBrowser{}, err
	}
	defer unlock()
	return addPairedBrowserLocked(b, o)
}

// addPairedBrowserLocked is addPairedBrowser for a caller that holds the state lock.
func addPairedBrowserLocked(b PairedBrowser, o mintOpts) (token string, _ PairedBrowser, _ error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", PairedBrowser{}, fmt.Errorf("generate token: %w", err)
	}
	idBuf := make([]byte, 8)
	if _, err := rand.Read(idBuf); err != nil {
		return "", PairedBrowser{}, fmt.Errorf("generate id: %w", err)
	}
	token = hex.EncodeToString(buf)
	b.ID = hex.EncodeToString(idBuf)
	if o.ID != "" {
		b.ID = o.ID
	}
	b.Label, b.Origin, b.Agent = clip(b.Label), clip(b.Origin), clip(b.Agent)
	b.TokenHash = hashToken(token)
	b.PairedAt = time.Now().UTC()

	list, err := readBrowsersLocked()
	if err != nil {
		return "", PairedBrowser{}, err
	}
	for _, e := range list {
		if o.ID != "" && e.ID == o.ID {
			return "", PairedBrowser{}, ErrClaimConsumed
		}
	}
	if b.Instance != "" {
		kept := make([]PairedBrowser, 0, len(list))
		for _, e := range list {
			if e.Instance != b.Instance {
				kept = append(kept, e)
				continue
			}
			owns := o.RotateToken != "" && subtle.ConstantTimeCompare([]byte(e.TokenHash), []byte(hashToken(o.RotateToken))) == 1
			if !owns && !o.ReplaceInstance {
				return "", PairedBrowser{}, ErrInstancePaired
			}
		}
		list = kept
	}
	if len(list) >= maxPairedBrowsers {
		return "", PairedBrowser{}, ErrBrowserLimit
	}
	list = append(list, b)
	if err := writeBrowsersLocked(list); err != nil {
		return "", PairedBrowser{}, err
	}
	return token, b, nil
}

// pairedBrowserCount is how many browsers are paired.
func pairedBrowserCount() int {
	unlock, err := lockState()
	if err != nil {
		return 0
	}
	defer unlock()
	list, _ := readBrowsersLocked()
	return len(list)
}

// pairedBrowserForToken finds the entry a presented token belongs to,
// comparing hashes in constant time.
func pairedBrowserForToken(token string) (PairedBrowser, bool) {
	if token == "" {
		return PairedBrowser{}, false
	}
	want := hashToken(token)
	unlock, err := lockState()
	if err != nil {
		return PairedBrowser{}, false
	}
	defer unlock()
	list, err := readBrowsersLocked()
	if err != nil {
		return PairedBrowser{}, false
	}
	found := PairedBrowser{}
	ok := false
	for _, e := range list {
		if subtle.ConstantTimeCompare([]byte(e.TokenHash), []byte(want)) == 1 {
			found, ok = e, true
		}
	}
	return found, ok
}

// pairedBrowserIDs is the set of ids still paired, or nil when the state
// cannot be read (a caller must not treat that as "nobody is paired").
func pairedBrowserIDs() map[string]bool {
	unlock, err := lockState()
	if err != nil {
		return nil
	}
	defer unlock()
	list, _ := readBrowsersLocked()
	out := make(map[string]bool, len(list))
	for _, e := range list {
		out[e.ID] = true
	}
	return out
}

// PairedBrowserForInstance is the paired browser that holds this instance id.
func PairedBrowserForInstance(instance string) (PairedBrowser, bool) {
	if instance == "" {
		return PairedBrowser{}, false
	}
	unlock, err := lockState()
	if err != nil {
		return PairedBrowser{}, false
	}
	defer unlock()
	list, _ := readBrowsersLocked()
	for _, e := range list {
		if e.Instance == instance {
			return e, true
		}
	}
	return PairedBrowser{}, false
}
