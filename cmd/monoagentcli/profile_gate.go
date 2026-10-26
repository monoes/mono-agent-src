package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/cli/profilegate"
	"github.com/monoes/mono-agent/internal/profileorder"
)

// The profile gate (internal/cli/profilegate): a command that acts on a
// profile must be told which one with --profile, or it is refused (exit 3). The
// gate enforces: there is no active profile and no fallback. ModeWarn stays for
// the gate package's own tests.
var profileGateMode = profilegate.ModeEnforce

// installProfileGate makes every command run the gate before it runs. The gate
// reads whether --profile was given from the parsed flags, so it must come
// before anything (initDB) resolves cfg.ProfileID. A command with a
// PersistentPreRun of its own (doctor, setup) replaces the root's, so each of
// those is wrapped to run the gate first.
func installProfileGate(root *cobra.Command, cfg *globalConfig) {
	profileSets := 0
	countProfileFlag(root, &profileSets)
	// gate returns the refusal in ModeEnforce (exit code 3, printed by the
	// CLI's normal error path before the command's own work) and prints the
	// warning in ModeWarn.
	gate := func(cmd *cobra.Command) error {
		if err := guardTaskSession(cmd, cfg.DBPath, profileSets); err != nil {
			return err
		}
		list := func() ([]profilegate.ProfileRef, error) { return listProfilesReadOnly(cfg.DBPath) }
		// An empty value (--profile '' or --profile=) names no profile.
		given := cmd.Flags().Changed("profile")
		if f := cmd.Flags().Lookup("profile"); given && f != nil && strings.TrimSpace(f.Value.String()) == "" {
			given = false
		}
		warning, err := profilegate.Check(cmd, given, profileGateMode, list)
		if err != nil {
			return errInvalidInput("%s", err.Error())
		}
		if warning != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), warning)
		}
		return nil
	}
	var wrap func(c *cobra.Command)
	wrap = func(c *cobra.Command) {
		if run := c.PersistentPreRun; run != nil {
			c.PersistentPreRun = nil
			c.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
				if err := gate(cmd); err != nil {
					return err
				}
				run(cmd, args)
				return nil
			}
		}
		for _, sub := range c.Commands() {
			wrap(sub)
		}
	}
	wrap(root)
}

// readOnlyURI is the SQLite URI that opens the file at path read-only. The path
// is escaped (a ?, # or % in a folder name is part of the name), and a Windows
// drive path gets the leading slash a file URI needs.
func readOnlyURI(path string) string {
	p := filepath.ToSlash(path)
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	return u.String()
}

// listProfilesReadOnly names the profiles of the database at dbPath for the
// gate's message. Best effort: it never creates or migrates a database, and
// any failure means no list.
func listProfilesReadOnly(dbPath string) ([]profilegate.ProfileRef, error) {
	if inClientMode() { // not even to read it (client_mode.go)
		return nil, errClientModeDB
	}
	path := expandPath(dbPath)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", readOnlyURI(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, name, created_at FROM profiles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		ref     profilegate.ProfileRef
		created string
	}
	var all []row
	for rows.Next() {
		var r row
		var created sql.NullString
		if err := rows.Scan(&r.ref.ID, &r.ref.Name, &created); err != nil {
			return nil, err
		}
		r.created = created.String
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	profileorder.Sort(all, func(r row) string { return r.created }, func(r row) string { return r.ref.ID })
	var refs []profilegate.ProfileRef
	for _, r := range all {
		refs = append(refs, r.ref)
	}
	return refs, nil
}
