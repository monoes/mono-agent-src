package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
)

// browser upload — files into a page's file input, or through the control
// that opens the file chooser, with no native dialog.

func newBrowserUploadCmd(cfg *globalConfig) *cobra.Command {
	var chooser bool
	var hilID string
	cmd := &cobra.Command{
		Use:   "upload <selector> <file>...",
		Short: "Put files into a file input, or through the button that opens the file chooser",
		Long: "Set the files on the <input type=file> a CSS selector matches, as if they were picked in\n" +
			"the browser's own dialog (the page gets its input and change events). With --chooser the\n" +
			"selector is the control that opens the chooser — a styled \"Upload\" button or label —\n" +
			"and the input it would open is filled instead, so the dialog never appears. Files are\n" +
			"read by this command (~ is expanded), at most 10 and 20 MB together.\n\n" +
			"Run by an agent, an upload to an allowed page that is not local waits for approval\n" +
			"(except at full-no-approvals): exit 3 with {\"status\":\"awaiting_approval\",\"hil_id\":…}. The\n" +
			"card names every file with its size and digest. Once the user approved it, run\n" +
			"`browser upload --hil <id>`: it uploads exactly those files, once, on the same page.\n" +
			"A file that changed since the approval is refused (submit_changed).",
		Example: `  monoagentcli --profile <id> browser upload "input[type=file]" ~/Documents/cv.pdf
  monoagentcli --profile <id> browser upload --chooser "button.upload" photo.jpg
  monoagentcli --profile <id> --json browser upload --hil 1a2b3c4d`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if hilID != "" {
				if len(args) != 0 || chooser {
					return errInvalidInput("--hil takes no selector, files or --chooser: they are in the approved item")
				}
				return runApprovedUpload(cmd, cfg, hilID)
			}
			if len(args) < 2 {
				return errInvalidInput("usage: browser upload <selector> <file>... (or --hil <id>)")
			}
			scope, err := applyScope(cfg)
			if err != nil {
				return err
			}
			if scope != nil {
				if err := scope.CheckUpload(args[1:]); err != nil {
					return scopeError(err)
				}
			}
			files, err := browserctl.ReadUploadFiles(args[1:])
			if err != nil {
				return errInvalidInput("%v", err)
			}
			r, err := openBrowser(cmd, cfg, false)
			if err != nil {
				return err
			}
			defer r.Close()
			sel, n := args[0], 0
			err = r.act(func() error {
				if r.s.Gated() {
					aw, err := r.s.HoldUpload(cmd.Context(), r.db.DB, sel, chooser, files)
					if err != nil {
						return err
					}
					return aw
				}
				var err error
				n, err = r.s.Upload(sel, chooser, files)
				return err
			})
			if err != nil {
				return err
			}
			return r.out(uploadResult(r.s.TabID(), sel, n, r.s.URL()), func() {
				fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %d file(s) to %s\n", n, sel)
			})
		},
	}
	cmd.Flags().BoolVar(&chooser, "chooser", false, "The selector is the control that opens the file chooser, not the file input")
	cmd.Flags().StringVar(&hilID, "hil", "", "Carry out an upload the user approved: the held item's id")
	return cmd
}

func uploadResult(tab int, selector string, n int, url string) map[string]any {
	return map[string]any{"tabId": tab, "uploaded": n, "selector": selector, "url": url}
}

// runApprovedUpload performs an upload a person approved, once.
func runApprovedUpload(cmd *cobra.Command, cfg *globalConfig, hilID string) error {
	ctx := cmd.Context()
	db, err := initDB(cfg)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	it, err := browserctl.ClaimSubmitItem(ctx, db.DB, cfg.ProfileID, hilID)
	if err != nil {
		return browserErr(cmd, cfg, err)
	}
	giveBack := func(err error) error {
		_ = browserctl.ReleaseSubmitItem(ctx, db.DB, cfg.ProfileID, it.ID)
		return browserErr(cmd, cfg, err)
	}
	tgt, err := extensionApproved(cfg, db.DB, it)
	if err != nil {
		return giveBack(err)
	}
	sender, err := browserBridge()
	if err != nil {
		return giveBack(err)
	}
	perms, err := browserctl.LoadPermissions(db.DB)
	if err != nil {
		return giveBack(err)
	}
	// As for a submit: the approval covers the page it was given for.
	var allow []string
	if o, err := browserctl.NormalizeAllowEntry(it.Origin); err == nil {
		allow = append(allow, o)
	}
	s, err := browserctl.Open(browserctl.Options{
		Browser: it.Instance, Profile: cfg.ProfileID, Tab: it.TabID, Permissions: &perms, Allowlist: allow, Sender: sender, PinOrigin: pinOriginOf(tgt),
	})
	if err != nil {
		return giveBack(err)
	}
	if now := browserctl.Origin(s.URL()); now != it.Origin {
		return giveBack(errInvalidInput("tab %d is now on %s, but the approval was for an upload on %s", it.TabID, now, it.Origin))
	}
	sel, chooser, files, err := browserctl.UploadFromItem(s.Page(), it)
	if err != nil {
		if errors.Is(err, browserctl.ErrNotSent) {
			return giveBack(err)
		}
		return browserErr(cmd, cfg, err)
	}
	if scope, err := applyScope(cfg); err != nil {
		return giveBack(err)
	} else if scope != nil {
		var paths []string
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		if err := scope.CheckUpload(paths); err != nil {
			return giveBack(scopeError(err))
		}
	}
	n, err := s.Upload(sel, chooser, files)
	_ = browserctl.FinishSubmitItem(ctx, db.DB, cfg.ProfileID, it.ID, err)
	if err != nil {
		return browserErr(cmd, cfg, err)
	}
	url, _ := s.Refresh()
	out := uploadResult(s.TabID(), sel, n, url)
	out["hil_id"], out["status"] = it.ID, "uploaded"
	return browserOut(cmd, cfg, out, func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %d file(s) on %s (approval %s used)\n", n, it.Origin, it.ID)
	})
}
