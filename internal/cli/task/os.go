package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/tasks/osmenu"
)

// What the tests replace: the OS, this binary's path, and the refresh that
// makes the Services menu read ~/Library/Services again.
var (
	taskOSGOOS       = runtime.GOOS
	taskOSExecutable = os.Executable
	refreshServices  = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "/System/Library/CoreServices/pbs", "-update").Run()
	}
)

// newTaskOSCmd is `task os`: the macOS Services menu item that files the text
// selected in any app into one profile's Inbox (spec section 12).
func (d Deps) newTaskOSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "os",
		Short: "The macOS menu: select text in any app, then Services, Add to MonoAgent Tasks",
		Long: `Installs a macOS Quick Action, "Add to MonoAgent Tasks: <profile>", in the
Services menu of every app (right-click selected text, or the app's menu,
Services). It runs

  monoagentcli --profile <id> task add --stdin --source os

with the selected text on standard input (never on a command line), so the
text lands in the Inbox of one profile: the one --profile names, else the
active profile when you install. Run install once per profile. No app or
daemon has to be running. macOS may list the item only after you enable it
once in System Settings, Keyboard, Keyboard Shortcuts, Services, Text.

Windows and Linux have no such menu: bind a global hotkey to a command that
pipes the selected text into that same command (see monoagentcli ref tasks).`,
	}
	cmd.PersistentFlags().String("dest", "", "The Services folder (default ~/Library/Services)")
	cmd.AddCommand(d.newTaskOSInstallCmd(), d.newTaskOSStatusCmd(), d.newTaskOSUninstallCmd())
	// The task group wraps only its own children; these are its grandchildren.
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}
	return cmd
}

// taskOSOnly refuses the os commands away from macOS, with what to do there
// instead (spec D27).
func (d Deps) taskOSOnly() error {
	if taskOSGOOS == "darwin" {
		return nil
	}
	return d.InvalidInput("task os installs a macOS Services menu, and this is %s: bind a global hotkey to a command that pipes the selected text into monoagentcli --profile <id> task add --stdin --source os (see monoagentcli ref tasks)", taskOSGOOS)
}

// taskOSDir is --dest, else ~/Library/Services. The bool says it is the real
// Services folder, which the Services menu is asked to read again.
func taskOSDir(cmd *cobra.Command) (string, bool, error) {
	if d, _ := cmd.Flags().GetString("dest"); d != "" {
		abs, err := filepath.Abs(clicore.ExpandPath(d))
		return abs, false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("finding your home folder: %w", err)
	}
	return filepath.Join(home, "Library", "Services"), true, nil
}

// taskOSDBPath is the absolute path of the database this command uses: a menu
// files into it, and with the profile id it is the menu's identity.
func (d Deps) taskOSDBPath() (string, error) { return filepath.Abs(clicore.ExpandPath(d.DBPath())) }

// taskOSMenuSpec is the menu of profile p as install writes it: running cli,
// filing into the database this command uses.
func (d Deps) taskOSMenuSpec(cli string, p tasks.Profile) (osmenu.Spec, error) {
	db, err := d.taskOSDBPath()
	if err != nil {
		return osmenu.Spec{}, err
	}
	return osmenu.Spec{CLI: cli, DBPath: db, ProfileID: p.ID, ProfileName: p.Name}, nil
}

// taskOSRunnable reports whether path is a program the menu can run.
func taskOSRunnable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// withTaskStore opens the database without resolving --profile: status and
// uninstall take their profiles from the bundles, and some may be deleted.
// active is the active profile's id.
func (d Deps) withTaskStore(cmd *cobra.Command, fn func(ctx context.Context, store *tasks.Store, db *sql.DB, active string) error) error {
	db, active, err := d.OpenDBActive()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	return fn(cmd.Context(), tasks.NewStore(db.DB), db.DB, active)
}

func (d Deps) newTaskOSInstallCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: `Add "Add to MonoAgent Tasks: <profile>" to the macOS Services menu (you only)`,
		Long: `Adds the menu item for one profile: --profile (an id or a name), else the
active profile, which the output names. Run it again after renaming the profile
or moving monoagentcli; a menu that is already current is left alone. --force
replaces a bundle of the same name that this command did not write; the menu of
another profile is never replaced.`,
		Example: `  monoagentcli task os install
  monoagentcli --profile Work task os install`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// As P1's operator commands do: an agent is refused first, on every platform; then the
			// arguments are counted (cobra's Args would be exit 1 with no --json document); then the
			// platform is checked.
			if _, err := d.callerFor(flagAs(cmd)).operator("install the macOS menu"); err != nil {
				return err
			}
			if len(args) != 0 {
				return d.InvalidInput("task os install takes no arguments (got %q): the profile is --profile, as in monoagentcli --profile Work task os install", cutArg(args[0]))
			}
			if err := d.taskOSOnly(); err != nil {
				return err
			}
			dir, services, err := taskOSDir(cmd)
			if err != nil {
				return err
			}
			exe, err := taskOSExecutable()
			if err != nil {
				return fmt.Errorf("finding this monoagentcli: %w", err)
			}
			cli := filepath.Clean(exe)
			if strings.Contains(filepath.ToSlash(cli), "/go-build") {
				return d.InvalidInput("this monoagentcli is a temporary build (%s) that is gone after the run: run task os install from the installed monoagentcli", cli)
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				spec, err := d.taskOSMenuSpec(cli, p)
				if err != nil {
					return err
				}
				b, err := osmenu.Render(spec)
				if err != nil {
					return d.InvalidInput("%v", err)
				}
				gone := func(id string) bool {
					_, err := store.Profile(ctx, id)
					return errors.Is(err, tasks.ErrInvalid)
				}
				res, err := osmenu.Install(dir, b, force, gone)
				if errors.Is(err, osmenu.ErrTaken) {
					return d.InvalidInput("%v", err)
				}
				if err != nil {
					return err
				}
				if services && res.Outcome != osmenu.Unchanged {
					refreshServices()
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{
						"profile": p, "path": res.Path, "menu_item": res.Menu, "cli": cli, "outcome": res.Outcome, "removed": res.Removed,
					})
				}
				printTaskOSInstalled(cmd.OutOrStdout(), p, res, cli)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Replace a bundle of the same name that this command did not write")
	return cmd
}

func printTaskOSInstalled(w io.Writer, p tasks.Profile, res osmenu.Result, cli string) {
	switch res.Outcome {
	case osmenu.Unchanged:
		fmt.Fprintf(w, "Already installed: %q files into profile %s (%s).\n  %s\n  runs %s\n", res.Menu, p.Name, p.ID, res.Path, cli)
		return
	case osmenu.Updated:
		fmt.Fprintf(w, "Updated %q: it files into profile %s (%s).\n  %s\n  runs %s\n", res.Menu, p.Name, p.ID, res.Path, cli)
		for _, old := range res.Removed {
			fmt.Fprintf(w, "  removed the older %s\n", old)
		}
	default:
		fmt.Fprintf(w, "Installed %q: it files into profile %s (%s).\n  %s\n  runs %s\n", res.Menu, p.Name, p.ID, res.Path, cli)
	}
	fmt.Fprintf(w, `Select text in any app, right-click it and choose Services, %q (or use the
app's menu, Services). The text goes to the Inbox of %s, where you read and approve it.
If the item is not listed, enable it once in System Settings, Keyboard, Keyboard
Shortcuts, Services, Text; you can give it a keyboard shortcut there too.
For another profile: monoagentcli --profile NAME task os install
`, res.Menu, p.Name)
}

// taskOSMenuJSON is one menu in `task os status --json`.
type taskOSMenuJSON struct {
	Path    string        `json:"path"`
	Profile tasks.Profile `json:"profile"` // the name is empty when the profile is gone or in another database
	CLI     string        `json:"cli"`
	DB      string        `json:"db"`
	State   string        `json:"state"` // current, stale, profile_gone or other_database
	Why     string        `json:"why"`   // empty when current
}

func (d Deps) newTaskOSStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List the installed Add to MonoAgent Tasks menus and whether each is current (macOS)",
		Long: `Lists the menus task os install wrote in the Services folder, with the profile
each files into and its state: current; stale (the profile was renamed, the
menu's monoagentcli is gone, or the menu was changed or written in an older
format: install it again); profile_gone (the profile was deleted: uninstall it
with --profile and the id shown); or other_database (it files into another
database: run status with that --db-path to judge it).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return d.InvalidInput("task os status takes no arguments (got %q): its one option is --dest", cutArg(args[0]))
			}
			if err := d.taskOSOnly(); err != nil {
				return err
			}
			dir, _, err := taskOSDir(cmd)
			if err != nil {
				return err
			}
			dbPath, err := d.taskOSDBPath()
			if err != nil {
				return err
			}
			menus, err := osmenu.List(dir)
			if err != nil {
				return err
			}
			return d.withTaskStore(cmd, func(ctx context.Context, store *tasks.Store, _ *sql.DB, _ string) error {
				rows := make([]taskOSMenuJSON, 0, len(menus))
				for _, m := range menus {
					row, err := d.taskOSState(ctx, store, dbPath, m)
					if err != nil {
						return err
					}
					rows = append(rows, row)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"dir": dir, "menus": rows})
				}
				printTaskOSStatus(cmd.OutOrStdout(), dir, rows)
				return nil
			})
		},
	}
}

// taskOSState compares an installed menu with what install would write for
// it now with the monoagentcli it names, so that the answer does not depend on
// which copy of the CLI asks. A menu of another database than db is not judged
// against this one.
func (d Deps) taskOSState(ctx context.Context, store *tasks.Store, db string, m osmenu.Menu) (taskOSMenuJSON, error) {
	row := taskOSMenuJSON{Path: m.Path, Profile: tasks.Profile{ID: m.ProfileID}, CLI: m.CLI, DB: m.DB,
		State: "other_database", Why: "it files into another database"}
	if m.DB != db {
		return row, nil
	}
	row.State, row.Why = "profile_gone", "the profile was deleted"
	p, err := store.Profile(ctx, m.ProfileID)
	if errors.Is(err, tasks.ErrInvalid) {
		return row, nil
	}
	if err != nil {
		return row, d.taskErr(err)
	}
	row.Profile, row.State = p, "stale"
	if !taskOSRunnable(m.CLI) {
		row.Why = "monoagentcli is not at " + m.CLI
		return row, nil
	}
	row.Why = "the profile was renamed, or the menu was changed or written in an older format"
	spec, err := d.taskOSMenuSpec(m.CLI, p)
	if err != nil {
		return row, nil
	}
	if b, err := osmenu.Render(spec); err == nil && osmenu.Matches(m.Path, b) {
		row.State, row.Why = "current", ""
	}
	return row, nil
}

func printTaskOSStatus(w io.Writer, dir string, rows []taskOSMenuJSON) {
	if len(rows) == 0 {
		fmt.Fprintf(w, "No Add to MonoAgent Tasks menu is installed in %s.\nAdd one with: monoagentcli --profile NAME task os install\n", dir)
		return
	}
	fmt.Fprintf(w, "Add to MonoAgent Tasks menus in %s:\n", dir)
	for _, r := range rows {
		switch r.State {
		case "current":
			fmt.Fprintf(w, "  current       %s (%s)  %s\n", r.Profile.Name, r.Profile.ID, filepath.Base(r.Path))
		case "stale":
			fmt.Fprintf(w, "  stale         %s (%s)  %s\n      %s; to refresh: monoagentcli --profile %s task os install\n",
				r.Profile.Name, r.Profile.ID, filepath.Base(r.Path), r.Why, r.Profile.ID)
		case "other_database":
			fmt.Fprintf(w, "  other database  %s  %s\n      files into %s; to judge it: monoagentcli --db-path %s task os status\n",
				r.Profile.ID, filepath.Base(r.Path), r.DB, r.DB)
		default:
			fmt.Fprintf(w, "  profile gone  %s  %s\n      to remove: monoagentcli --profile %s task os uninstall\n",
				r.Profile.ID, filepath.Base(r.Path), r.Profile.ID)
		}
	}
}

func (d Deps) newTaskOSUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove a profile's Add to MonoAgent Tasks menu (you only; macOS)",
		Long: `Removes the menu that files into the profile --profile names (an id or a
name), else into the active profile, of the database this command uses
(--db-path). A profile deleted since is named by its id, as task os status
prints it. Only a menu task os install wrote is removed; nothing to remove is
not an error.`,
		Example: `  monoagentcli --profile Work task os uninstall`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := d.callerFor(flagAs(cmd)).operator("remove the macOS menu"); err != nil {
				return err
			}
			if len(args) != 0 {
				return d.InvalidInput("task os uninstall takes no arguments (got %q): the profile is --profile, as in monoagentcli --profile Work task os uninstall", cutArg(args[0]))
			}
			if err := d.taskOSOnly(); err != nil {
				return err
			}
			dir, services, err := taskOSDir(cmd)
			if err != nil {
				return err
			}
			dbPath, err := d.taskOSDBPath()
			if err != nil {
				return err
			}
			return d.withTaskStore(cmd, func(_ context.Context, _ *tasks.Store, db *sql.DB, active string) error {
				id := active
				if flag := d.ProfileID(); flag != "" {
					id = flag // a deleted profile's id, as status prints it
					if resolved, ok := resolveProfileID(db, flag); ok {
						id = resolved
					}
				}
				removed, err := osmenu.Remove(dir, dbPath, id)
				if err != nil {
					return err
				}
				if services && len(removed) > 0 {
					refreshServices()
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile_id": id, "removed": removed})
				}
				if len(removed) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "No Add to MonoAgent Tasks menu files into profile %s in %s: nothing to remove.\n", id, dir)
				}
				for _, p := range removed {
					fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", p)
				}
				return nil
			})
		},
	}
}

// resolveProfileID is the id of the profile idOrName names, by id or else by name (the
// CLI's resolveProfileID).
func resolveProfileID(db *sql.DB, idOrName string) (string, bool) {
	var id string
	if err := db.QueryRow(`SELECT id FROM profiles WHERE id = ?`, idOrName).Scan(&id); err == nil {
		return id, true
	}
	if err := db.QueryRow(`SELECT id FROM profiles WHERE LOWER(name) = LOWER(?)`, idOrName).Scan(&id); err == nil {
		return id, true
	}
	return "", false
}
