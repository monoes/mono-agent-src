// Command release-manifest is a maintainer tool, never shipped in a release.
//
//	release-manifest manifest -dir DIR -version vX.Y.Z [-repo OWNER/REPO] [-notes-url URL] [-expires-days N]
//	    writes DIR/SHA256SUMS and DIR/manifest.json (unsigned; safe to run in CI).
//	    -expires-days N adds the signed "expires_at" field (N days from now); 0 omits it.
//	release-manifest sign -manifest DIR/manifest.json [-key-file FILE | -keyring | -key-env NAME]
//	    [-assets-dir DIR -expect-version vX.Y.Z [-url-prefix URL] [-min-version vX.Y.Z]] [-pubkey-out FILE]
//	    writes manifest.json.sig and manifest.json.sshsig (sshsig.go; what install.sh checks
//	    with ssh-keygen) next to it. -keyring and -key-file are the owner's local
//	    break-glass path. -key-env NAME is the CI path: it reads the base64 private key from
//	    that environment variable (never a flag value), and then requires -assets-dir,
//	    -expect-version and -url-prefix so the manifest is checked against the real assets
//	    (and against where its URLs may point) before signing.
//	release-manifest check -manifest M -assets-dir DIR -expect-version vX.Y.Z -url-prefix URL
//	    runs the same checks on an already signed manifest, and with -strict also requires that
//	    DIR holds nothing but the listed assets (plus the manifest.json* files, SHA256SUMS and NOTICE).
//
// The signature file is one line, "<key id> <base64 Ed25519 signature>", made over
// the exact bytes of manifest.json. The key id is the first 8 bytes of the SHA-256
// of the public key, hex encoded. The private key is the base64 of the 64-byte
// Ed25519 private key (a bare 32-byte seed is accepted too), either in a file or in the OS keyring entry written by
// `monoagentcli release keygen` (service "monoagent-release-signing", account
// "ed25519-v1").
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "monoagent-release-signing"
	keyringAccount = "ed25519-v1"
	defaultRepo    = "monoes/mono-agent"
)

type Asset struct {
	OS     string `json:"os,omitempty"`   // empty for the extension, which runs on every platform
	Arch   string `json:"arch,omitempty"` // empty for the extension
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind"`
}

// extensionAsset is the browser extension zip release.yml builds. It is a
// manifest asset of kind "extension" with no os/arch, so `monoagentcli
// extension install` can check it against the signed manifest. Clients that
// pick by os/arch/kind never select it.
const extensionAsset = "monoagent-chrome-extension.zip"

type Manifest struct {
	Schema     int     `json:"schema"`
	Version    string  `json:"version"`
	ReleasedAt string  `json:"released_at"`
	NotesURL   string  `json:"notes_url"`
	ExpiresAt  string  `json:"expires_at,omitempty"`
	Assets     []Asset `json:"assets"`
}

var (
	cliRe        = regexp.MustCompile(`^monoagentcli-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)
	bundledRe    = regexp.MustCompile(`^monoagentcli-(windows)-(amd64)-bundled\.exe$`)
	appArchiveRe = regexp.MustCompile(`^MonoAgent-(linux|darwin|windows)-(amd64|arm64)(\.tar\.gz|\.zip|\.exe)$`)
	versionRe    = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
)

// classify maps a release artifact name (as named by release.yml) to its
// platform. The Chrome extension zip is kind "extension" with no platform. ok
// is false for files that are not downloads, such as NOTICE or the checksum
// files.
func classify(name string) (osName, arch, kind string, ok bool) {
	if name == extensionAsset {
		return "", "", "extension", true
	}
	if m := cliRe.FindStringSubmatch(name); m != nil {
		return m[1], m[2], "cli", true
	}
	// The Windows app ships beside its bundled CLI; both belong to the app.
	if m := bundledRe.FindStringSubmatch(name); m != nil {
		return m[1], m[2], "app", true
	}
	if m := appArchiveRe.FindStringSubmatch(name); m != nil {
		return m[1], m[2], "app", true
	}
	return "", "", "", false
}

func buildManifest(dir, version, repo, notesURL string, now time.Time, expiresDays int) (*Manifest, []byte, error) {
	if !versionRe.MatchString(version) {
		return nil, nil, fmt.Errorf("version %q must look like v1.2.3", version)
	}
	if expiresDays < 0 {
		return nil, nil, errors.New("-expires-days must not be negative")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, version)
	if notesURL == "" {
		notesURL = fmt.Sprintf("https://github.com/%s/releases/tag/%s", repo, version)
	}
	m := &Manifest{Schema: 1, Version: version, ReleasedAt: now.UTC().Format(time.RFC3339), NotesURL: notesURL, Assets: []Asset{}}
	if expiresDays > 0 {
		m.ExpiresAt = now.UTC().AddDate(0, 0, expiresDays).Format(time.RFC3339)
	}
	var sums strings.Builder
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && e.Name() != "SHA256SUMS" && e.Name() != "SHA256SUMS.txt" &&
			!strings.HasPrefix(e.Name(), "manifest.json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(data)
		hexSum := hex.EncodeToString(sum[:])
		fmt.Fprintf(&sums, "%s  %s\n", hexSum, name)
		if o, a, k, ok := classify(name); ok {
			m.Assets = append(m.Assets, Asset{OS: o, Arch: a, Name: name, URL: base + "/" + name, SHA256: hexSum, Size: int64(len(data)), Kind: k})
		}
	}
	if len(m.Assets) == 0 {
		return nil, nil, errors.New("no recognised release artifacts in " + dir)
	}
	return m, []byte(sums.String()), nil
}

func keyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

func parsePrivateKey(s string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("private key is not base64: %w", err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	}
	return nil, fmt.Errorf("private key is %d bytes, want %d (or a %d-byte seed)", len(raw), ed25519.PrivateKeySize, ed25519.SeedSize)
}

// signManifest returns the contents of manifest.json.sig for the given bytes.
func signManifest(manifest []byte, priv ed25519.PrivateKey) string {
	pub := priv.Public().(ed25519.PublicKey)
	return keyID(pub) + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)) + "\n"
}

// verifySignature checks a .sig file's contents against manifest bytes and the
// given public keys. It backs the round-trip test; the client has its own verifier.
func verifySignature(manifest []byte, sig string, keys []ed25519.PublicKey) error {
	f := strings.Fields(sig)
	if len(f) != 2 {
		return errors.New("signature file must be '<key id> <base64 signature>'")
	}
	raw, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return err
	}
	for _, k := range keys {
		if keyID(k) == f[0] && ed25519.Verify(k, manifest, raw) {
			return nil
		}
	}
	return errors.New("no pinned key verifies this signature")
}

func parseSemver(v string) ([3]int, error) {
	var out [3]int
	if !versionRe.MatchString(v) {
		return out, fmt.Errorf("version %q must look like v1.2.3", v)
	}
	for i, p := range strings.Split(v[1:], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("version %q: %w", v, err)
		}
		out[i] = n
	}
	return out, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release-manifest:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: release-manifest manifest|sign [flags]")
	}
	switch args[0] {
	case "manifest":
		fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
		dir := fs.String("dir", "", "directory of release artifacts")
		version := fs.String("version", "", "release tag, vX.Y.Z")
		repo := fs.String("repo", defaultRepo, "releases repo (owner/name) the asset URLs point at")
		notes := fs.String("notes-url", "", "release notes URL (default: the release page in -repo)")
		expDays := fs.Int("expires-days", 0, "days until the manifest expires (writes expires_at); 0 omits the field")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || *version == "" {
			return errors.New("manifest needs -dir and -version")
		}
		m, sums, err := buildManifest(*dir, *version, *repo, *notes, time.Now(), *expDays)
		if err != nil {
			return err
		}
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*dir, "SHA256SUMS"), sums, 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(*dir, "manifest.json"), append(out, '\n'), 0o644)
	case "sign":
		fs := flag.NewFlagSet("sign", flag.ContinueOnError)
		path := fs.String("manifest", "", "path to manifest.json")
		keyFile := fs.String("key-file", "", "file holding the base64 64-byte private key")
		useKeyring := fs.Bool("keyring", false, "read the key from the OS keyring entry written by 'monoagentcli release keygen'")
		keyEnv := fs.String("key-env", "", "NAME of an environment variable holding the base64 private key (64-byte key or 32-byte seed)")
		assetsDir := fs.String("assets-dir", "", "directory of the built release files; the manifest is checked against them before signing")
		expectVer := fs.String("expect-version", "", "version being released; the manifest must carry exactly this")
		urlPrefix := fs.String("url-prefix", "", "https://github.com/OWNER/REPO/releases/download/TAG/ that every asset url must start with (required with -key-env)")
		minVer := fs.String("min-version", "", "latest published version; the manifest must not be lower (empty skips)")
		pubOut := fs.String("pubkey-out", "", "write the public key line '<id> <base64>' (public, safe to log) to this file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		sources := 0
		for _, set := range []bool{*keyFile != "", *useKeyring, *keyEnv != ""} {
			if set {
				sources++
			}
		}
		if *path == "" || sources != 1 {
			return errors.New("sign needs -manifest and exactly one of -key-file, -keyring or -key-env")
		}
		if *keyEnv != "" && (*assetsDir == "" || *expectVer == "" || *urlPrefix == "") {
			return errors.New("-key-env requires -assets-dir, -expect-version and -url-prefix: a CI-held key only signs a manifest verified against the real assets and the expected release url")
		}
		if *urlPrefix != "" && *assetsDir == "" {
			return errors.New("-url-prefix needs -assets-dir and -expect-version")
		}
		if (*assetsDir == "") != (*expectVer == "") {
			return errors.New("-assets-dir and -expect-version go together")
		}
		var secret string
		if *keyEnv != "" {
			secret = os.Getenv(*keyEnv)
			if strings.TrimSpace(secret) == "" {
				return fmt.Errorf("environment variable %s is empty or unset: refusing to sign", *keyEnv)
			}
			os.Unsetenv(*keyEnv)
		} else if *useKeyring {
			s, err := keyring.Get(keyringService, keyringAccount)
			if err != nil {
				return fmt.Errorf("keyring entry %s/%s: %w", keyringService, keyringAccount, err)
			}
			secret = s
		} else {
			if err := checkKeyFileMode(*keyFile); err != nil {
				return err
			}
			b, err := os.ReadFile(*keyFile)
			if err != nil {
				return err
			}
			secret = string(b)
		}
		priv, err := parsePrivateKey(secret)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		if *assetsDir != "" {
			if err := checkManifestAgainstAssets(data, *assetsDir, *expectVer, *minVer, *urlPrefix, false, time.Now()); err != nil {
				return fmt.Errorf("refusing to sign: %w", err)
			}
		}
		if *pubOut != "" {
			pub := priv.Public().(ed25519.PublicKey)
			line := keyID(pub) + " " + base64.StdEncoding.EncodeToString(pub) + "\n"
			if err := os.WriteFile(*pubOut, []byte(line), 0o644); err != nil {
				return err
			}
		}
		sshsig, err := sshSignature(data, priv)
		if err != nil {
			return fmt.Errorf("SSH signature: %w", err)
		}
		if err := os.WriteFile(*path+".sig", []byte(signManifest(data, priv)), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(*path+".sshsig", []byte(sshsig), 0o644); err != nil {
			return err
		}
		fmt.Printf("signed %s with key %s\n", *path, keyID(priv.Public().(ed25519.PublicKey)))
		return nil
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		path := fs.String("manifest", "", "path to the (signed) manifest.json")
		assetsDir := fs.String("assets-dir", "", "directory of the files about to be uploaded")
		expectVer := fs.String("expect-version", "", "version being released")
		urlPrefix := fs.String("url-prefix", "", "https://github.com/OWNER/REPO/releases/download/TAG/")
		strict := fs.Bool("strict", false, "also require that -assets-dir holds nothing but the listed assets")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || *assetsDir == "" || *expectVer == "" || *urlPrefix == "" {
			return errors.New("check needs -manifest, -assets-dir, -expect-version and -url-prefix")
		}
		data, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		if err := checkManifestAgainstAssets(data, *assetsDir, *expectVer, "", *urlPrefix, *strict, time.Now()); err != nil {
			return fmt.Errorf("check failed: %w", err)
		}
		fmt.Printf("%s matches the files in %s\n", *path, *assetsDir)
		return nil
	}
	return fmt.Errorf("unknown mode %q", args[0])
}
