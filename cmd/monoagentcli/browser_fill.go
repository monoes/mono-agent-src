package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
)

// browser fill — the side panel's Fill form, from the command line.

func newBrowserFillCmd(cfg *globalConfig) *cobra.Command {
	var files, generate bool
	var hilID string
	cmd := &cobra.Command{
		Use:   "fill",
		Short: "Fill the page's form from the profile's Info page and documents (never submits)",
		Long: "Fill the form on the tab from --profile: its Info page and its own documents and captures\n" +
			"answer the page's fields, the way the side panel's Fill form does. Nothing is submitted: the\n" +
			"extension asks the person to review the form and press Submit. Passwords, card numbers and\n" +
			"one-time codes are never sent to the model nor filled; a field that already has text is left\n" +
			"alone. The AI runtime is the one chosen in the extension's \"AI for summaries\".\n\n" +
			"--generate also lets the model write an answer to an open question in a text area (a message,\n" +
			"a cover letter, why you are a fit) from the profile's facts only; those fields are counted in\n" +
			"\"generated\" so they can be reviewed. --files attaches the profile's stored photos and scans to\n" +
			"the page's file boxes instead (secure pages only). To attach any other file, or one made with\n" +
			"an AI image node, use `browser upload`.\n\n" +
			"Run by an agent, a fill on an allowed page that is not local waits for approval (except at\n" +
			"full-no-approvals): exit 3 with {\"status\":\"awaiting_approval\",\"hil_id\":…}; once approved, run\n" +
			"`browser fill --hil <id>`.",
		Example: `  monoagentcli --profile <id> browser fill
  monoagentcli --profile <id> --json browser fill --generate
  monoagentcli --profile <id> browser fill --files
  monoagentcli --profile <id> browser fill --hil 1a2b3c4d`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if hilID != "" {
				if files || generate {
					return errInvalidInput("--hil takes no other flag: they are in the approved item")
				}
				return runApprovedFill(cmd, cfg, hilID)
			}
			if files && generate {
				return errInvalidInput("--generate writes text, --files attaches files: use one at a time")
			}
			what := browserctl.FillText
			if files {
				what = browserctl.FillFiles
			}
			r, err := openBrowser(cmd, cfg, false)
			if err != nil {
				return err
			}
			defer r.Close()
			var res browserctl.FillResult
			err = r.act(func() error {
				if r.s.Gated() {
					aw, err := r.s.HoldFill(cmd.Context(), r.db.DB, what, generate)
					if err != nil {
						return err
					}
					return aw
				}
				var err error
				res, err = r.s.Fill(what, generate)
				return err
			})
			if err != nil {
				return err
			}
			return printFill(r, res, what)
		},
	}
	cmd.Flags().BoolVar(&files, "files", false, "Attach the profile's stored photos and scans to the page's file boxes (secure pages only)")
	cmd.Flags().BoolVar(&generate, "generate", false, "Let the model write answers to open questions in text areas, from the profile's facts only")
	cmd.Flags().StringVar(&hilID, "hil", "", "Carry out a fill the user approved: the held item's id")
	return cmd
}

func printFill(r *browserRun, res browserctl.FillResult, what string) error {
	return r.out(map[string]any{"tabId": r.s.TabID(), "mode": what, "filled": res.Filled, "attached": res.Attached,
		"found": res.Found, "generated": res.Generated, "none": res.None, "sources": res.Sources, "url": r.s.URL()}, func() {
		switch {
		case res.None || res.Found == 0:
			fmt.Fprintln(r.cmd.OutOrStdout(), "Nothing on the page to fill")
		case what == browserctl.FillFiles:
			fmt.Fprintf(r.cmd.OutOrStdout(), "Attached %d of %d file box(es)\n", res.Attached, res.Found)
		default:
			fmt.Fprintf(r.cmd.OutOrStdout(), "Filled %d of %d field(s) (%d written by AI) — review the form and press Submit yourself\n", res.Filled, res.Found, res.Generated)
		}
	})
}

// runApprovedFill performs a fill a person approved, once.
func runApprovedFill(cmd *cobra.Command, cfg *globalConfig, hilID string) error {
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
		return giveBack(errInvalidInput("tab %d is now on %s, but the approval was for a fill on %s", it.TabID, now, it.Origin))
	}
	what, generate, err := browserctl.FillFromItem(s.Page(), cfg.ProfileID, it)
	if err != nil {
		if errors.Is(err, browserctl.ErrNotSent) {
			return giveBack(err)
		}
		return browserErr(cmd, cfg, err)
	}
	res, err := s.Fill(what, generate)
	_ = browserctl.FinishSubmitItem(ctx, db.DB, cfg.ProfileID, it.ID, err)
	if err != nil {
		return browserErr(cmd, cfg, err)
	}
	s.Refresh()
	r := &browserRun{cfg: cfg, cmd: cmd, s: s, db: db}
	return printFill(r, res, what)
}
