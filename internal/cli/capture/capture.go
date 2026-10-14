package capturecli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/capture"
)

// newCaptureCmd returns the `capture` command group: save a page from the
// user's real, logged-in Chrome into the monomind inbox
// (~/.monomind/inbox/<timestamp>-<slug>/), and list what is already there.
//
// The capture itself happens in the browser — the extension holds the
// `debugger` permission, so an MHTML snapshot and a print-to-PDF of a page
// the user is signed into need no new permission and no second, cookie-less
// browser. This side sends the command, reassembles what comes back (large
// artifacts arrive in chunks), and writes the envelope. See
// docs/BROWSER_TRACK_PLAN.md and internal/capture.
func (d Deps) newCaptureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture web pages from your browser into the monomind inbox",
	}
	cmd.AddCommand(
		d.newCaptureListCmd(),
		d.newCaptureClassifyCmd(), // classify.go
	)
	cmd.AddCommand(d.captureGlueCmds()...) // export, import — see archive.go
	return cmd
}

func (d Deps) newCaptureListCmd() *cobra.Command {
	var (
		out         string
		allProfiles bool
		suggested   bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the captures waiting in the inbox",
		Long: "Lists the capture envelopes in ~/.monomind/inbox, newest first.\n" +
			"\n" +
			"Captures still being written (staging directories, and any directory\n" +
			"without a meta.json) are skipped: a capture is only real once it has been\n" +
			"renamed into place, which is what keeps a watcher from ingesting half of\n" +
			"one.\n" +
			"\n" +
			"A capture saved into a profile lands in that profile's own inbox, not this\n" +
			"one — pass --profile (the global flag, an id or a name) to read one of\n" +
			"those, or --all-profiles to read every inbox at once.",
		Example: "  monoagentcli capture list\n" +
			"  monoagentcli capture list --profile work\n" +
			"  monoagentcli capture list --all-profiles --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			sources, err := d.captureListSources(out, allProfiles)
			if err != nil {
				return err
			}

			entries := []capture.Entry{}
			for _, src := range sources {
				found, err := capture.List(src.inbox)
				if err != nil {
					return fmt.Errorf("reading inbox: %w", err)
				}
				for _, e := range found {
					// A capture written straight into a profile's inbox
					// (`--out`) may not name the profile itself; the
					// directory it was found in is the better answer than
					// a blank column.
					if e.Profile == "" {
						e.Profile = src.profile
					}
					entries = append(entries, e)
				}
			}
			sortCaptureEntries(entries)
			if suggested {
				return d.printSuggestedCaptures(cmd.OutOrStdout(), d.JSONOutput(), entries) // capture_classify.go
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			if len(entries) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No captures in %s\n", captureSourceNames(sources))
				return nil
			}
			columns := []string{"CAPTURED", "TITLE", "URL", "SIZE", "PATH"}
			withProfile := len(sources) > 1 || sources[0].profile != ""
			if withProfile {
				columns = append([]string{"PROFILE"}, columns...)
			}
			table := d.NewTable(cmd.OutOrStdout(), columns)
			for _, e := range entries {
				row := []string{
					e.CapturedAt,
					truncateCaptureCell(e.Title, 40),
					truncateCaptureCell(e.URL, 60),
					captureHumanBytes(e.Bytes),
					e.Path,
				}
				if withProfile {
					row = append([]string{captureProfileLabel(sources, e.Profile)}, row...)
				}
				_ = table.Append(row)
			}
			return table.Render()
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Inbox directory to read (default: ~/.monomind/inbox)")
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "Read the default inbox and every profile's inbox")
	cmd.Flags().BoolVar(&suggested, "suggested", false, "Only captures whose classification.json suggests a route (see capture classify)")
	return cmd
}

// captureListSource is one inbox `capture list` reads, and the profile it
// belongs to ("" for the unprofiled default inbox).
type captureListSource struct {
	inbox   string
	profile string
	name    string
}

// captureListSources decides which inboxes to read. --out is an explicit
// directory and wins outright; otherwise --profile names one profile's
// inbox, --all-profiles reads the default inbox and every profile's, and
// plain `capture list` reads the default inbox exactly as it always has.
func (d Deps) captureListSources(out string, allProfiles bool) ([]captureListSource, error) {
	if dir := expandPath(out); dir != "" {
		if allProfiles {
			return nil, d.InvalidInput("--out reads one directory; drop it to use --all-profiles")
		}
		return []captureListSource{{inbox: dir}}, nil
	}
	wanted := strings.TrimSpace(d.ProfileID())
	if wanted == "" && !allProfiles {
		return []captureListSource{{inbox: capture.DefaultInbox()}}, nil
	}
	if wanted != "" && allProfiles {
		return nil, d.InvalidInput("--profile names one profile; drop it to use --all-profiles")
	}

	db, err := openProfileDB(d.DBPath())
	if err != nil {
		return nil, fmt.Errorf("cannot read your profiles: %w", err)
	}
	defer db.Close()

	if wanted != "" {
		p, err := d.resolveCaptureProfile(db.DB, wanted)
		if err != nil {
			return nil, err
		}
		return []captureListSource{{inbox: p.Inbox, profile: p.ID, name: p.Name}}, nil
	}

	profiles, err := captureProfileInboxes(db.DB)
	if err != nil {
		return nil, err
	}
	sources := []captureListSource{{inbox: capture.DefaultInbox()}}
	for _, p := range profiles {
		sources = append(sources, captureListSource{inbox: p.Inbox, profile: p.ID, name: p.Name})
	}
	return sources, nil
}

// sortCaptureEntries restores "newest first" across several inboxes —
// capture.List only ever sorted within one.
func sortCaptureEntries(entries []capture.Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CapturedAt != entries[j].CapturedAt {
			return entries[i].CapturedAt > entries[j].CapturedAt
		}
		return entries[i].Path > entries[j].Path
	})
}

// captureProfileLabel renders a profile column: the name a person
// recognises when this listing knows it, the id otherwise.
func captureProfileLabel(sources []captureListSource, profile string) string {
	if profile == "" {
		return "-"
	}
	for _, s := range sources {
		if s.profile == profile && s.name != "" {
			return truncateCaptureCell(s.name, 20)
		}
	}
	return truncateCaptureCell(profile, 20)
}

// captureSourceNames names the inboxes that turned out to be empty, so the
// answer says where it looked.
func captureSourceNames(sources []captureListSource) string {
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.inbox)
	}
	return strings.Join(names, ", ")
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// captureHumanBytes renders a byte count the way a person reads it. The
// units run to exabytes because an int64 of bytes does: a count that
// outgrew the table used to index past the end of it and panic, which is a
// silly way to lose a listing.
func captureHumanBytes(n int64) string {
	const unit = 1024
	const units = "KMGTPE"
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < len(units)-1; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), units[exp])
}

// truncateCaptureCell keeps one table cell from wrapping the whole row.
func truncateCaptureCell(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
