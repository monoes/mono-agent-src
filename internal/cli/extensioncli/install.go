package extensioncli

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	monoagent "github.com/monoes/mono-agent"
)

// maxExtensionZip bounds every unpacked file (and a --zip), far above the
// extension's real size of about 1.5 MB.
const maxExtensionZip = 64 << 20

// bundledExtension is the extension folder that ships inside this binary (a
// variable so tests can bring their own).
var bundledExtension = func() fs.FS {
	sub, err := fs.Sub(monoagent.ChromeExtension, "chrome-extension")
	if err != nil {
		panic(err)
	}
	return sub
}()

// extensionDir is where the extension is unpacked: the folder the browser
// loads it from, so it stays put across updates.
func extensionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "extension"), nil
}

// shipsWithExtension reports whether a bundled file goes into the unpacked
// folder: the unit tests stay in the repo.
func shipsWithExtension(name string) bool { return !strings.HasSuffix(name, ".test.mjs") }

func newExtensionInstallCmd(cfg *Deps) *cobra.Command {
	var zipPath string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Unpack the MonoAgent Bridge extension that ships with this CLI for your browser",
		Long: "Unpacks the extension folder built into this CLI into ~/.monoagent/extension,\n" +
			"the exact version that matches this CLI, so nothing is downloaded. The folder\n" +
			"stays at that path, so the browser keeps loading it from there: after an\n" +
			"update, run this again and click Reload on the extension.\n" +
			"\n" +
			"--zip installs a zip you already have instead. A local zip is not part of\n" +
			"this CLI, so --zip is the user's own: an agent, the chat or a workflow is\n" +
			"refused (exit 3, operator_only).",
		Example: "  monoagentcli extension install\n" +
			"  monoagentcli extension install --zip ~/Downloads/monoagent-chrome-extension.zip",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			dir, err := extensionDir()
			if err != nil {
				return err
			}
			if zipPath != "" {
				if err := cfg.operatorOnly("extension install --zip puts code the CLI does not ship where the browser loads the extension from"); err != nil {
					return err
				}
				b, err := os.ReadFile(zipPath)
				if err != nil {
					return fmt.Errorf("read %s: %w", zipPath, err)
				}
				fmt.Fprintf(out, "Using %s (a local file: not the extension this CLI ships).\n", zipPath)
				if err := installExtensionZip(b, dir); err != nil {
					return err
				}
			} else if err := installExtensionFS(bundledExtension, dir); err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return writeExtensionInfoJSON(out, readExtensionInfo(dir))
			}
			printExtensionLoadSteps(out, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&zipPath, "zip", "", "install this extension zip instead of the one built into this CLI")
	return cmd
}

// extensionInfo is `extension info`: where the extension is and whether the
// copy there is the one this CLI ships.
type extensionInfo struct {
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	// Current is true when the unpacked folder matches the bundled extension
	// file for file; false means run `extension install`.
	Current bool   `json:"current"`
	Version string `json:"version,omitempty"`
}

func newExtensionInfoCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show where the extension is unpacked and whether it is the version this CLI ships",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := extensionDir()
			if err != nil {
				return err
			}
			info := readExtensionInfo(dir)
			out := cmd.OutOrStdout()
			if cfg.JSONOutput() {
				return writeExtensionInfoJSON(out, info)
			}
			switch {
			case !info.Installed:
				fmt.Fprintf(out, "Not installed. Run `monoagentcli extension install` to unpack it into %s\n", dir)
			case !info.Current:
				fmt.Fprintf(out, "Installed in %s but out of date. Run `monoagentcli extension install`, then click Reload on the extension.\n", dir)
			default:
				fmt.Fprintf(out, "Installed in %s (up to date, version %s).\n", dir, info.Version)
			}
			return nil
		},
	}
	return cmd
}

// readExtensionInfo inspects the unpacked folder at dir.
func readExtensionInfo(dir string) extensionInfo {
	info := extensionInfo{Path: dir}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		info.Installed = true
		info.Version = manifestVersion(os.DirFS(dir))
		want, werr := extensionDigest(bundledExtension)
		got, gerr := extensionDigest(os.DirFS(dir))
		info.Current = werr == nil && gerr == nil && want == got
	}
	return info
}

func writeExtensionInfoJSON(out io.Writer, info extensionInfo) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

func manifestVersion(fsys fs.FS) string {
	b, err := fs.ReadFile(fsys, "manifest.json")
	if err != nil {
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Version
}

// extensionDigest is one sha256 over the names and contents of every file
// that ships, so an unpacked folder can be compared with the bundled one.
func extensionDigest(fsys fs.FS) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !shipsWithExtension(name) {
			return err
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// installExtensionFS copies the extension in fsys into dir, replacing what is there.
func installExtensionFS(fsys fs.FS, dir string) error {
	return swapExtensionIn(dir, func(staged string) error {
		return fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !shipsWithExtension(name) {
				return err
			}
			b, err := fs.ReadFile(fsys, name)
			if err != nil {
				return err
			}
			target := filepath.Join(staged, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.WriteFile(target, b, 0o644)
		})
	})
}

// installExtensionZip unpacks data into dir, replacing what is there.
func installExtensionZip(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("read the extension zip: %w", err)
	}
	return swapExtensionIn(dir, func(staged string) error { return unpackExtension(zr, staged) })
}

// swapExtensionIn fills a sibling folder with fill and swaps it in for dir
// only once it is complete, so a failure leaves the previous extension in place.
func swapExtensionIn(dir string, fill func(staged string) error) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(parent, ".extension-new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := fill(staged); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(staged, "manifest.json")); err != nil {
		return fmt.Errorf("the extension holds no manifest.json at its top level or in its one top folder")
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
