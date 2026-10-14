package profile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"
)

// A profile's folder: where it is (`profile folder`) and moving it
// (`profile move`). Orgs run out of the folder, so a move of a folder with
// running orgs is the caller's to sequence around it:
//
//	org serve --stop → profile move → org reconcile → org serve
//
// (see org_profile_lifecycle.go); the desktop app's "Move profile folder"
// runs exactly that.

func (d Deps) newProfileFolderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "folder <name-or-id>",
		Short: "Print a profile's folder, creating its layout if it is missing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			p, err := d.lookupProfile(db.DB, args[0])
			if err != nil {
				return err
			}
			if err := profiledir.EnsureLayout(db.DB, p.ID); err != nil {
				return fmt.Errorf("preparing profile folder: %w", err)
			}
			if d.JSONOutput() {
				return printJSON(map[string]string{"id": p.ID, "root_dir": p.RootDir})
			}
			fmt.Println(p.RootDir)
			return nil
		},
	}
}

func (d Deps) newProfileMoveCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "move <name-or-id> <new-folder>",
		Short: "Move a profile's data (vault files and .monomind) to another folder",
		Long: "Moves the profile's vault files and its .monomind knowledge-graph folder into <new-folder>, " +
			"then points the profile at it. The profile's folder setting only changes once both moves " +
			"succeeded, so a failure leaves the old folder authoritative and the move can be retried. " +
			"Stop the folder's orgs first (`org serve --stop`) and run `org reconcile` and `org serve` " +
			"afterwards. --check only validates the destination.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			newRoot := strings.TrimSpace(args[1])
			if newRoot == "" {
				return d.InvalidInput("no folder chosen")
			}
			if err := d.validateFolderChoice(newRoot); err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			p, err := d.lookupProfile(db.DB, args[0])
			if err != nil {
				return err
			}
			oldRoot := p.RootDir
			if newRoot == oldRoot {
				return d.InvalidInput("that's already this profile's folder")
			}
			out := map[string]interface{}{"id": p.ID, "old_root_dir": oldRoot, "root_dir": newRoot, "moved": !check}
			if check {
				if d.JSONOutput() {
					return printJSON(out)
				}
				fmt.Printf("%s can move to %s\n", p.Name, newRoot)
				return nil
			}

			ctx := cmd.Context()
			oldVaultDir := profiledir.VaultDir(db.DB, p.ID)
			oldMonomindDir := profiledir.MonomindDir(db.DB, p.ID)
			newVaultDir := filepath.Join(newRoot, ".monoagent", "vault")
			newMonomindDir := filepath.Join(newRoot, ".monomind")
			if err := os.MkdirAll(newVaultDir, 0700); err != nil {
				return fmt.Errorf("creating new vault folder: %w", err)
			}
			if err := os.MkdirAll(newMonomindDir, 0700); err != nil {
				return fmt.Errorf("creating new monomind folder: %w", err)
			}

			imgMoved, errs := vault.MoveFiles(ctx, db.DB, p.ID, oldVaultDir, newVaultDir)
			docMoved, docErrs := vault.MoveDocumentFiles(ctx, db.DB, p.ID, filepath.Join(oldVaultDir, "documents"), filepath.Join(newVaultDir, "documents"))
			errs = append(errs, docErrs...)
			if len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintf(os.Stderr, "warning: profile %s: moving vault file: %v\n", p.ID, e)
				}
				return fmt.Errorf("moving vault files: %v (%d file(s) failed)", errs[0], len(errs))
			}

			// .monomind holds only monograph/KG SQLite files with no
			// cross-references elsewhere in the database (unlike
			// vault_images.path), so moving the directory itself is enough.
			// os.Rename fails across filesystems (e.g. onto an external
			// drive) — fall back to copy-then-remove.
			if err := os.Rename(oldMonomindDir, newMonomindDir); err != nil {
				if err := copyDirThenRemove(oldMonomindDir, newMonomindDir); err != nil {
					return fmt.Errorf("moving monomind folder: %w", err)
				}
			}

			if _, err := db.DB.Exec(`UPDATE profiles SET root_dir = ? WHERE id = ?`, newRoot, p.ID); err != nil {
				return fmt.Errorf("updating profile folder: %w", err)
			}
			out["moved_files"] = imgMoved + docMoved
			if d.JSONOutput() {
				return printJSON(out)
			}
			fmt.Printf("Moved %s to %s\n", p.Name, newRoot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Only check that the destination is usable; move nothing")
	return cmd
}

// validateFolderChoice rejects a folder that isn't safe to hand a
// profile's data to. The folder need not be empty or new — pointing a
// profile at an existing, non-empty folder (e.g. a coding project that
// already has its own .monomind/ from a prior `monomind init`) is fine:
// EnsureLayout only ever adds a .monoagent/ and a .monomind/ subfolder
// inside it and never touches anything else there, so nothing pre-existing
// is at risk except those two specific names. What's actually unsafe, and
// rejected here, is a `.monoagent` or `.monomind` entry that already exists
// as a plain file rather than a directory — os.MkdirAll would fail
// confusingly on that, so it's caught up front with a clear message instead.
func (d Deps) validateFolderChoice(dir string) error {
	if !filepath.IsAbs(dir) {
		return d.InvalidInput("folder path must be absolute: %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // doesn't exist yet — fine, EnsureLayout creates it
		}
		return fmt.Errorf("checking folder %q: %w", dir, err)
	}
	if !info.IsDir() {
		return d.InvalidInput("%q is not a folder", dir)
	}
	for _, name := range []string{".monoagent", ".monomind"} {
		entryInfo, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue // doesn't exist — fine, EnsureLayout creates it
		}
		if !entryInfo.IsDir() {
			return d.InvalidInput("%q already contains a file named %q — move or rename it first", dir, name)
		}
	}
	return nil
}

// copyDirThenRemove copies src's tree into dst (which must already exist),
// then removes src — the move's fallback for a .monomind move that crosses
// filesystems, where os.Rename fails.
func copyDirThenRemove(src, dst string) error {
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(destPath, 0700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(destPath)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(src)
}
