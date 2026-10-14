package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/release"
	"github.com/spf13/cobra"
)

// extensionZipAsset is the browser extension's release asset (release.yml
// zips the chrome-extension/ folder under that name).
const extensionZipAsset = "monoagent-chrome-extension.zip"

// extensionReleasesURL is the releases page every extension download URL is
// built from (a variable so tests can point it at a fake server).
var extensionReleasesURL = "https://github.com/" + releasesRepo + "/releases"

// maxExtensionZip bounds the download and every unpacked file. The release
// zip is about 1.5 MB.
const maxExtensionZip = 64 << 20

func newExtensionInstallCmd() *cobra.Command {
	var zipPath string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download the MonoAgent Bridge extension for this version and unpack it for your browser",
		Long: "Downloads monoagent-chrome-extension.zip from the release that matches this\n" +
			"CLI (the latest release for a development build), checks it against the\n" +
			"release's signed manifest (Ed25519 signature, then the zip's sha256), and\n" +
			"unpacks it into ~/.monoagent/extension. An older release whose manifest has\n" +
			"no extension entry is checked against its SHA256SUMS.txt instead. The\n" +
			"folder stays at that path, so the browser keeps loading it from there:\n" +
			"after an update, run this again and click Reload on the extension.\n" +
			"\n" +
			"--zip installs a zip you already have instead. A local zip is not checked\n" +
			"against a release.",
		Example: "  monoagentcli extension install\n" +
			"  monoagentcli extension install --zip ~/Downloads/monoagent-chrome-extension.zip",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			var data []byte
			if zipPath != "" {
				b, err := os.ReadFile(zipPath)
				if err != nil {
					return fmt.Errorf("read %s: %w", zipPath, err)
				}
				fmt.Fprintf(out, "Using %s (a local file: not checked against a release).\n", zipPath)
				data = b
			} else {
				b, err := downloadExtensionZip(out, getVersion())
				if err != nil {
					return err
				}
				data = b
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir := filepath.Join(home, ".monoagent", "extension")
			if err := installExtensionZip(data, dir); err != nil {
				return err
			}
			printExtensionLoadSteps(out, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&zipPath, "zip", "", "install this extension zip instead of downloading one")
	return cmd
}

// extensionDownloadBase is where this version's release assets live: the
// tagged release, or the latest one for a development build.
func extensionDownloadBase(version string) string {
	if isDevVersion(version) {
		return extensionReleasesURL + "/latest/download"
	}
	return extensionReleasesURL + "/download/" + version
}

// extensionManifestClient is the release client that reads the signed
// manifest at manifestURL (a variable so tests can use a test key and server).
var extensionManifestClient = func(manifestURL string) *release.Client {
	c := release.DefaultClient()
	c.ManifestURLs = []string{manifestURL}
	// This reads the CLI's own release, which may be older than one `update`
	// already accepted, so it keeps no rollback record.
	c.StatePath = ""
	c.MaxAssetBytes = maxExtensionZip
	return c
}

// downloadExtensionZip returns the extension zip only after it is verified:
// against the release's signed manifest (signature, then the zip's sha256),
// or, when that manifest has no extension entry (releases before the
// extension was listed) or no signed manifest can be had, against the
// release's SHA256SUMS.txt. A manifest that fails a check is never a reason
// to fall back.
func downloadExtensionZip(out io.Writer, version string) ([]byte, error) {
	base := extensionDownloadBase(version)
	ctx := context.Background()
	rc := extensionManifestClient(base + "/manifest.json")
	m, err := rc.Latest(ctx)
	switch {
	case err == nil:
		if !isDevVersion(version) && m.Version != version {
			return nil, fmt.Errorf("the signed manifest at %s is for %s, not %s; nothing was installed", base, m.Version, version)
		}
		a, aerr := m.Asset(extensionZipAsset, "", "", "extension")
		if aerr != nil {
			fmt.Fprintf(out, "Manifest signature verified, but release %s lists no extension in it (an older release); checking against %s instead.\n", m.Version, sha256SumsAssetName)
			break
		}
		fmt.Fprintf(out, "Manifest signature verified (release %s).\n", m.Version)
		fmt.Fprintf(out, "Downloading %s (%s)...\n", extensionZipAsset, a.URL)
		data, err := rc.Download(ctx, a)
		if err != nil {
			return nil, fmt.Errorf("%w; nothing was installed", err)
		}
		fmt.Fprintf(out, "Checksum verified against the signed manifest: %s SHA256 %s\n", extensionZipAsset, a.SHA256)
		return data, nil
	case !errors.Is(err, release.ErrUnavailable):
		return nil, fmt.Errorf("the signed release manifest was rejected: %w; nothing was installed", err)
	case !release.AllowLegacyGitHubUpdates:
		return nil, fmt.Errorf("cannot verify the extension: %w", err)
	default:
		fmt.Fprintf(out, "No signed manifest to check against (%v); checking against %s only.\n", err, sha256SumsAssetName)
	}
	fmt.Fprintf(out, "Downloading %s (%s)...\n", extensionZipAsset, base)
	data, err := httpGetAll(base + "/" + extensionZipAsset)
	if err != nil {
		return nil, err
	}
	if len(data) > maxExtensionZip {
		return nil, fmt.Errorf("%s is larger than %d bytes; refusing it", extensionZipAsset, maxExtensionZip)
	}
	sums, err := httpGetAll(base + "/" + sha256SumsAssetName)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", sha256SumsAssetName, err)
	}
	if err := verifyReleaseDigest(data, sums, extensionZipAsset); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "Checksum verified: %s SHA256 %s\n", extensionZipAsset, sha256Hex(data))
	return data, nil
}

// installExtensionZip unpacks data into dir, replacing what is there. It
// unpacks into a sibling folder first and swaps the two only once the new
// one is complete, so a failure leaves the previous extension in place.
func installExtensionZip(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("read the extension zip: %w", err)
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(parent, ".extension-new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := unpackExtension(zr, staged); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(staged, "manifest.json")); err != nil {
		return fmt.Errorf("the zip holds no extension (no manifest.json at its top level or in its one top folder)")
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}

	old := ""
	if _, err := os.Stat(dir); err == nil {
		old = staged + ".old"
		if err := os.Rename(dir, old); err != nil {
			return fmt.Errorf("move the previous extension aside: %w", err)
		}
	}
	if err := os.Rename(staged, dir); err != nil {
		if old != "" {
			_ = os.Rename(old, dir)
		}
		return fmt.Errorf("install the extension into %s: %w", dir, err)
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return nil
}

// unpackExtension writes the zip's regular files under dst. The release zip
// keeps everything under one top folder (chrome-extension/); that folder is
// dropped so manifest.json lands directly in dst. Entries that would leave
// dst, links and other non-regular files are refused.
func unpackExtension(zr *zip.Reader, dst string) error {
	strip := commonTopDir(zr.File)
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, strip)
		if name == "" || strings.HasSuffix(f.Name, "/") {
			continue
		}
		clean := path.Clean(name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, `\`) {
			return fmt.Errorf("the extension zip has an unsafe path %q; nothing was installed", f.Name)
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("the extension zip has a non-regular file %q; nothing was installed", f.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("read %s from the zip: %w", f.Name, err)
	}
	defer rc.Close()
	w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(w, io.LimitReader(rc, maxExtensionZip+1))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("unpack %s: %w", f.Name, err)
	}
	if n > maxExtensionZip {
		return fmt.Errorf("%s in the extension zip is too large", f.Name)
	}
	return nil
}

// commonTopDir returns "dir/" when every entry sits under that one folder,
// else "".
func commonTopDir(files []*zip.File) string {
	top := ""
	for _, f := range files {
		i := strings.Index(f.Name, "/")
		if i <= 0 {
			return ""
		}
		d := f.Name[:i+1]
		if top == "" {
			top = d
		} else if d != top {
			return ""
		}
	}
	return top
}

func printExtensionLoadSteps(out io.Writer, dir string) {
	fmt.Fprintf(out, "Unpacked the MonoAgent Bridge extension into %s\n\n", dir)
	fmt.Fprintln(out, "Load it in your browser (once):")
	fmt.Fprintln(out, "  1. Open chrome://extensions (Edge: edge://extensions).")
	fmt.Fprintln(out, "  2. Turn on Developer mode.")
	fmt.Fprintf(out, "  3. Click Load unpacked and choose %s\n", dir)
	fmt.Fprintln(out, "  4. Pair it: monoagentcli extension pair --open")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "After an update, run `monoagentcli extension install` again and click Reload on the extension.")
}
