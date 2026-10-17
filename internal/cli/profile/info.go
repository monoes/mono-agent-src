package profile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/capturesummary"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/profileinfo"
)

// `profile info`: the personal details a profile keeps for filling web forms
// (internal/profileinfo). The desktop Info page and the extension use these
// commands; nothing else reads the table.
//
//	profile info fields                 the catalog: every field, grouped
//	profile info get [--reveal]         the stored values (ID and card numbers masked; --reveal is the operator's only)
//	profile info set <key> [value]      set one (--stdin reads a long text)
//	profile info rm <key>               clear one
//	profile info photo <key> <file>     set a photo or scan
//	profile info photo-path <key>       where a photo is stored
//	profile info ai-fill                fill the empty fields from the documents

func (d Deps) newProfileInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Short: "The details a profile keeps for filling web forms (name, address, IDs, photos, card)",
	}
	cmd.AddCommand(
		d.newInfoFieldsCmd(), d.newInfoGetCmd(), d.newInfoSetCmd(), d.newInfoRmCmd(),
		d.newInfoPhotoCmd(), d.newInfoPhotoPathCmd(), d.newInfoAIFillCmd(),
	)
	return cmd
}

// infoErr turns a profileinfo error into the CLI's invalid-input error.
func (d Deps) infoErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, profileinfo.ErrUnknownField), errors.Is(err, profileinfo.ErrUserOwned):
		return d.InvalidInput("%v", err)
	}
	return err
}

func (d Deps) newInfoFieldsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fields",
		Short: "List every field the profile can hold, grouped as the Info page shows them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.JSONOutput() {
				return printJSON(map[string]any{"groups": profileinfo.Groups, "fields": profileinfo.Catalog})
			}
			t := d.NewTable(cmd.OutOrStdout(), []string{"KEY", "LABEL", "GROUP", "KIND"})
			for _, f := range profileinfo.Catalog {
				t.Append([]string{f.Key, f.Label, f.Group, string(f.Kind)})
			}
			t.Render()
			return nil
		},
	}
}

func (d Deps) newInfoGetCmd() *cobra.Command {
	var reveal bool
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show the stored values; ID, bank and card numbers are masked unless --reveal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Every field --reveal unmasks is an ID, bank or card number: only
			// the operator's own terminal or the app sees those in full.
			if why := operator.Refusal(); reveal && why != "" {
				return d.InvalidInput("--reveal shows ID, bank and card numbers in full, and only you may see them: %s", why)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			entries, err := profileinfo.Get(cmd.Context(), db.DB, d.ProfileID(), reveal)
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return printJSON(map[string]any{"profile": d.ProfileID(), "entries": entries})
			}
			t := d.NewTable(cmd.OutOrStdout(), []string{"KEY", "VALUE", "SOURCE"})
			for _, e := range entries {
				t.Append([]string{e.Key, truncateStr(strings.ReplaceAll(e.Value, "\n", " "), 60), e.Source})
			}
			t.Render()
			return nil
		},
	}
	cmd.Flags().BoolVar(&reveal, "reveal", false, "show ID, bank and card numbers in full")
	return cmd
}

func (d Deps) newInfoSetCmd() *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "set <key> [value]",
		Short: "Set one field; an empty value clears it",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			value := ""
			switch {
			case fromStdin:
				b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64<<10))
				if err != nil {
					return err
				}
				value = string(b)
			case len(args) == 2:
				value = args[1]
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			if err := profileinfo.Set(cmd.Context(), db.DB, d.ProfileID(), args[0], value, profileinfo.SourceUser, ""); err != nil {
				return d.infoErr(wrapInvalid(d, err))
			}
			if d.JSONOutput() {
				return printJSON(map[string]string{"key": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s.\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the value from standard input (for long text)")
	return cmd
}

// wrapInvalid makes a validation message (not a database failure) invalid input.
func wrapInvalid(d Deps, err error) error {
	if errors.Is(err, profileinfo.ErrUnknownField) || errors.Is(err, profileinfo.ErrUserOwned) {
		return err
	}
	msg := err.Error()
	for _, marker := range []string{"must be", "longer than", "is a photo", "does not fill"} {
		if strings.Contains(msg, marker) {
			return d.InvalidInput("%s", msg)
		}
	}
	return err
}

func (d Deps) newInfoRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <key>",
		Short: "Clear one field (and delete a photo's file)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			if err := profileinfo.Remove(cmd.Context(), db.DB, d.ProfileID(), args[0]); err != nil {
				return d.infoErr(err)
			}
			if d.JSONOutput() {
				return printJSON(map[string]string{"key": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Cleared %s.\n", args[0])
			return nil
		},
	}
}

func (d Deps) newInfoPhotoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "photo <key> <file>",
		Short: "Set a photo or scan (png, jpg, webp, gif or pdf, up to 10 MB)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			if err := profileinfo.SetPhoto(cmd.Context(), db.DB, d.ProfileID(), args[0], args[1]); err != nil {
				if os.IsNotExist(err) {
					return d.NotFound("no such file: %s", args[1])
				}
				return d.infoErr(wrapPhoto(d, err))
			}
			if d.JSONOutput() {
				return printJSON(map[string]string{"key": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s.\n", args[0])
			return nil
		},
	}
}

func wrapPhoto(d Deps, err error) error {
	msg := err.Error()
	for _, marker := range []string{"must be", "larger than", "not a photo", "no folder"} {
		if strings.Contains(msg, marker) {
			return d.InvalidInput("%s", msg)
		}
	}
	return err
}

func (d Deps) newInfoPhotoPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "photo-path <key>",
		Short: "Print where a photo is stored (empty when there is none)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			p := profileinfo.PhotoPath(cmd.Context(), db.DB, d.ProfileID(), args[0])
			if d.JSONOutput() {
				return printJSON(map[string]string{"key": args[0], "path": p})
			}
			fmt.Fprintln(cmd.OutOrStdout(), p)
			return nil
		},
	}
}

// aiFillTimeout bounds the search and the model turn together.
const aiFillTimeout = 4 * time.Minute

func (d Deps) newInfoAIFillCmd() *cobra.Command {
	var runtime, model string
	cmd := &cobra.Command{
		Use:   "ai-fill",
		Short: "Fill the empty fields from the profile's documents with an AI agent",
		Long: "Searches the profile's documents and captures, asks an agent runtime (default: " +
			"$MONOAGENT_SUMMARY_RUNTIME, else claude) which empty fields they answer, and stores the " +
			"answers marked as AI-filled. Values you typed, photos and card details are never touched.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			if runtime == "" {
				runtime = strings.TrimSpace(os.Getenv("MONOAGENT_SUMMARY_RUNTIME"))
			}
			if runtime == "" {
				runtime = capturesummary.DefaultRuntime
			}
			if !capturesummary.ValidRuntimeID(runtime) {
				return d.InvalidInput("invalid runtime %q", runtime)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), aiFillTimeout)
			defer cancel()
			facts, err := profileinfo.GatherFacts(ctx, db.DB, d.ProfileID(), profileinfo.Queries)
			if err != nil {
				return fmt.Errorf("searching the profile's documents: %w", err)
			}
			run := capturesummary.ExecRunner(0)
			res, err := profileinfo.AIFill(ctx, db.DB, d.ProfileID(), facts, func(ctx context.Context, prompt string) (string, error) {
				a, err := run(ctx, capturesummary.Target{Runtime: runtime, Model: model}, prompt)
				return a.Text, err
			})
			if err != nil {
				return fmt.Errorf("the AI could not fill the profile: %w", err)
			}
			if d.JSONOutput() {
				return printJSON(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Filled %d of %d empty fields.\n", len(res.Filled), res.Asked)
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "agent runtime to use")
	cmd.Flags().StringVar(&model, "model", "", "model for that runtime")
	return cmd
}
