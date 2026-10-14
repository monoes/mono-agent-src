package people

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/peopletags"
	"github.com/spf13/cobra"
)

// newPeopleTagCmd manages people's tags. The GUI's People page makes every
// tag change through these commands.
func (d Deps) newPeopleTagCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tag",
		Short: "Tag people: list, add, remove, recolour",
		Long: "Tags belong to the profile and are matched by name, case-insensitively. A person " +
			"has at most 10. Tags are addressed by id or name.",
	}
	cmd.AddCommand(d.newPeopleTagListCmd(), d.newPeopleTagMapCmd(), d.newPeopleTagAddCmd(), d.newPeopleTagRemoveCmd(), d.newPeopleTagColorCmd())
	return cmd
}

func (d Deps) newPeopleTagListCmd() *cobra.Command {
	var person string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the profile's tags, or one person's",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withTagDB(func(db *sql.DB) error {
				tags, err := peopletags.List(cmd.Context(), db, d.ProfileID(), person)
				if err != nil {
					return d.tagErr(err)
				}
				if d.JSONOutput() {
					return printReviewJSON(tags)
				}
				return d.printTags(tags...)
			})
		},
	}
	cmd.Flags().StringVar(&person, "person", "", "Only this person's tags")
	return cmd
}

// newPeopleTagMapCmd bulk-loads tags for a page of people in one query.
func (d Deps) newPeopleTagMapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "map <person-id>...",
		Short: "Show several people's tags at once, keyed by person id",
		Long: "Prints {person_id: [tags]} for the given people. People without tags, or not in " +
			"the profile, are left out.",
		Example: `  monoagentcli --json people tag map 3f2a… 9b1c…`,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withTagDB(func(db *sql.DB) error {
				byPerson, err := peopletags.ByPerson(cmd.Context(), db, d.ProfileID(), args)
				if err != nil {
					return d.tagErr(err)
				}
				if d.JSONOutput() {
					return printReviewJSON(byPerson)
				}
				if len(byPerson) == 0 {
					fmt.Println("No tags.")
					return nil
				}
				table := d.NewTable(os.Stdout, []string{"Person", "Tags"}, nil)
				for _, id := range args {
					tags, ok := byPerson[id]
					if !ok {
						continue
					}
					names := make([]string, len(tags))
					for i, t := range tags {
						names[i] = t.Name
					}
					table.Append([]string{id, strings.Join(names, ", ")})
				}
				table.Render()
				return nil
			})
		},
	}
}

func (d Deps) newPeopleTagAddCmd() *cobra.Command {
	var color string
	cmd := &cobra.Command{
		Use:   "add <person-id> <tag-name>",
		Short: "Tag a person, creating the tag when new",
		Long: "Tags a person with the profile's tag of that name, creating it when new. --color " +
			"sets a new tag's colour, and recolours an existing tag everywhere.",
		Example: `  monoagentcli people tag add 3f2a… "hot lead" --color "#ff5500"`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withTagDB(func(db *sql.DB) error {
				t, err := peopletags.Add(cmd.Context(), db, d.ProfileID(), args[0], args[1], color)
				if err != nil {
					return d.tagErr(err)
				}
				return d.printTags(t)
			})
		},
	}
	cmd.Flags().StringVar(&color, "color", "", "Hex colour, e.g. #ff5500")
	return cmd
}

func (d Deps) newPeopleTagRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <person-id> <tag>",
		Short: "Untag a person (the tag itself stays)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withTagDB(func(db *sql.DB) error {
				if err := peopletags.Remove(cmd.Context(), db, d.ProfileID(), args[0], args[1]); err != nil {
					return d.tagErr(err)
				}
				if d.JSONOutput() {
					return printReviewJSON(map[string]string{"person_id": args[0], "removed": args[1]})
				}
				fmt.Printf("Removed %s from %s.\n", args[1], args[0])
				return nil
			})
		},
	}
}

func (d Deps) newPeopleTagColorCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "color <tag> <hex-colour>",
		Short:   "Recolour a tag for everyone who has it",
		Example: `  monoagentcli people tag color "hot lead" "#ff5500"`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withTagDB(func(db *sql.DB) error {
				t, err := peopletags.SetColor(cmd.Context(), db, d.ProfileID(), args[0], args[1])
				if err != nil {
					return d.tagErr(err)
				}
				return d.printTags(t)
			})
		},
	}
}

func (d Deps) withTagDB(fn func(db *sql.DB) error) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	return fn(db.DB)
}

func (d Deps) printTags(tags ...peopletags.Tag) error {
	if d.JSONOutput() && len(tags) == 1 {
		return printReviewJSON(tags[0])
	}
	if len(tags) == 0 {
		fmt.Println("No tags.")
		return nil
	}
	table := d.NewTable(os.Stdout, []string{"ID", "Name", "Color"}, nil)
	for _, t := range tags {
		table.Append([]string{t.ID, t.Name, t.Color})
	}
	table.Render()
	return nil
}

func (d Deps) tagErr(err error) error {
	switch {
	case errors.Is(err, peopletags.ErrNotFound):
		return d.NotFound("%v", err)
	case errors.Is(err, peopletags.ErrInvalid):
		return d.InvalidInput("%v", err)
	}
	return err
}
