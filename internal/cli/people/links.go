package people

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/peoplelinks"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// newPeopleLinksCmd manages cross-platform links between people who look
// like the same human. Links are suggestions for a human to confirm; people
// rows are never merged.
func (d Deps) newPeopleLinksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "links",
		Short: "Link people on different platforms who are the same human",
		Long: "Finds people on different platforms who share a name, website, email or phone, asks " +
			"TypeSafe Jev whether each pair is the same human (opt-in: jev enable people_links), and " +
			"keeps confident answers as suggestions to confirm or dismiss. People are never merged.",
	}
	cmd.AddCommand(d.newPeopleLinksSuggestCmd(), d.newPeopleLinksListCmd(),
		d.newPeopleLinksStatusCmd("confirm", storage.PersonLinkConfirmed, "Confirm a link: the two people are the same human"),
		d.newPeopleLinksStatusCmd("dismiss", storage.PersonLinkDismissed, "Dismiss a link: the two people are different"))
	return cmd
}

func (d Deps) newPeopleLinksSuggestCmd() *cobra.Command {
	var (
		limit  int
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Find likely same-person pairs and ask Jev about each",
		Long: "Builds candidate pairs from matching names, websites, emails and phones (at most --limit), " +
			"then sends each pair to TypeSafe Jev in its own request. A pair Jev is confident about is " +
			"stored as a suggested link; a pair it is confident is different is stored as dismissed so it " +
			"is never asked again. Needs: monoagentcli jev enable people_links. --dry-run only lists the " +
			"candidates and sends nothing.",
		Example: `  monoagentcli people links suggest --dry-run
  monoagentcli people links suggest --limit 50`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			ctx := cmd.Context()
			if !dryRun && !jevconf.Enabled(db.DB, d.ProfileID(), jevconf.PeopleLinks) {
				return d.InvalidInput("%v", peoplelinks.ErrDisabled)
			}
			pairs, err := peoplelinks.Candidates(ctx, db, d.ProfileID(), limit)
			if err != nil {
				return err
			}
			if dryRun {
				return d.printLinkCandidates(pairs)
			}
			if len(pairs) == 0 {
				return d.printLinkResult(peoplelinks.Result{})
			}
			c, err := jevconf.NewClient(ctx, db.DB, d.ProfileID(), "", "", jevconf.PeopleLinks)
			if err != nil {
				return d.AuthConnection("%v", err)
			}
			threshold := jevconf.Threshold(db.DB, d.ProfileID(), jevconf.PeopleLinks, jevconf.DefaultThreshold[jevconf.PeopleLinks])
			res, err := peoplelinks.Suggest(ctx, c, db, d.ProfileID(), pairs, threshold)
			if err != nil {
				if errors.Is(err, peoplelinks.ErrDisabled) {
					return d.InvalidInput("%v", err)
				}
				return fmt.Errorf("asking Jev: %w", err)
			}
			return d.printLinkResult(res)
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", peoplelinks.DefaultLimit, "Most candidate pairs to consider")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List candidate pairs without sending anything to Jev")
	return cmd
}

func (d Deps) printLinkCandidates(pairs []peoplelinks.Pair) error {
	if d.JSONOutput() {
		if pairs == nil {
			pairs = []peoplelinks.Pair{}
		}
		return printReviewJSON(pairs)
	}
	if len(pairs) == 0 {
		fmt.Println("No candidate pairs.")
		return nil
	}
	table := d.NewTable(os.Stdout, []string{"Person A", "Person B", "Matched on"}, nil)
	for _, p := range pairs {
		table.Append([]string{linkPersonLabel(p.A), linkPersonLabel(p.B), strings.Join(p.Reasons, ", ")})
	}
	table.Render()
	fmt.Fprintf(os.Stderr, "\n%d candidate pair(s). Nothing was sent to Jev.\n", len(pairs))
	return nil
}

func linkPersonLabel(p peoplelinks.Person) string {
	name := p.FullName
	if name == "" {
		name = p.PlatformUsername
	}
	return fmt.Sprintf("%s (%s @%s) %s", name, strings.ToLower(p.Platform), p.PlatformUsername, p.ID)
}

func (d Deps) printLinkResult(res peoplelinks.Result) error {
	if d.JSONOutput() {
		return printReviewJSON(res)
	}
	fmt.Printf("Asked Jev about %d pair(s): %d suggested, %d dismissed as different, %d undecided, %d failed.\n",
		res.Asked, res.Suggested, res.Dismissed, res.Undecided, res.Failed)
	if res.Suggested > 0 {
		fmt.Println("Review them with: monoagentcli people links list --status suggested")
	}
	return nil
}

// personLinkView is a link with both people's display labels.
type personLinkView struct {
	*storage.PersonLink
	LabelA string `json:"label_a"`
	LabelB string `json:"label_b"`
}

func (d Deps) newPeopleLinksListCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List people links",
		Example: `  monoagentcli people links list --status suggested
  monoagentcli --json people links list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch status {
			case "", storage.PersonLinkSuggested, storage.PersonLinkConfirmed, storage.PersonLinkDismissed:
			default:
				return d.InvalidInput("--status must be suggested, confirmed or dismissed")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			links, err := db.ListPersonLinks(d.ProfileID(), status)
			if err != nil {
				return err
			}
			views := make([]personLinkView, 0, len(links))
			labels := map[string]string{}
			label := func(id string) string {
				if l, ok := labels[id]; ok {
					return l
				}
				l := id
				if p, err := db.GetPerson(id); err == nil && p != nil {
					name := p.FullName
					if name == "" {
						name = p.PlatformUsername
					}
					l = fmt.Sprintf("%s (%s @%s)", name, strings.ToLower(p.Platform), p.PlatformUsername)
				}
				labels[id] = l
				return l
			}
			for _, l := range links {
				views = append(views, personLinkView{PersonLink: l, LabelA: label(l.PersonA), LabelB: label(l.PersonB)})
			}
			if d.JSONOutput() {
				return printReviewJSON(views)
			}
			if len(views) == 0 {
				fmt.Println("No links.")
				return nil
			}
			table := d.NewTable(os.Stdout, []string{"ID", "Status", "Person A", "Person B", "Confidence", "Source"}, nil)
			for _, v := range views {
				table.Append([]string{v.ID, v.Status, v.LabelA, v.LabelB, fmt.Sprintf("%.2f", v.Confidence), v.Source})
			}
			table.Render()
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Only links with this status: suggested, confirmed or dismissed")
	return cmd
}

func (d Deps) newPeopleLinksStatusCmd(use, status, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <link-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if err := db.SetPersonLinkStatus(d.ProfileID(), args[0], status); err != nil {
				if errors.Is(err, storage.ErrPersonLinkNotFound) {
					return d.NotFound("no link %q in this profile", args[0])
				}
				return err
			}
			if d.JSONOutput() {
				return printReviewJSON(map[string]string{"id": args[0], "status": status})
			}
			fmt.Printf("Link %s is now %s.\n", args[0], status)
			return nil
		},
	}
}

// confirmedLinks returns every person
// confirmed as the same human as personID. Errors yield no labels: the
// links are an addition to `people get`, never a reason for it to fail.
func confirmedLinks(db *storage.Database, profileID, personID string) []confirmedLink {
	links, err := db.PersonLinksFor(profileID, personID)
	if err != nil {
		return nil
	}
	var out []confirmedLink
	for _, l := range links {
		if l.Status != storage.PersonLinkConfirmed || l.Relation != storage.PersonLinkSame {
			continue
		}
		other := l.Other(personID)
		c := confirmedLink{LinkID: l.ID, PersonID: other}
		if p, err := db.GetPerson(other); err == nil && p != nil {
			c.Platform, c.PlatformUsername, c.FullName = p.Platform, p.PlatformUsername, p.FullName
		}
		out = append(out, c)
	}
	return out
}

// confirmedLink is one confirmed same-person link as `people get` shows it.
type confirmedLink struct {
	LinkID           string `json:"link_id"`
	PersonID         string `json:"person_id"`
	Platform         string `json:"platform,omitempty"`
	PlatformUsername string `json:"platform_username,omitempty"`
	FullName         string `json:"full_name,omitempty"`
}

func (c confirmedLink) String() string {
	name := c.FullName
	if name == "" {
		name = c.PlatformUsername
	}
	if c.Platform == "" {
		return c.PersonID
	}
	return fmt.Sprintf("%s (%s @%s) %s", name, strings.ToLower(c.Platform), c.PlatformUsername, c.PersonID)
}
