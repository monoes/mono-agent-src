package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/monoes/mono-agent/internal/aitask"
	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
)

// A confined AI task's agent may run only the commands on its allow-list
// (internal/cli/chat confinedVerbs), but the list is a prefix match: whatever
// follows the prefix is free text, including global flags. In a task session
// (an agent process that carries a task id) the root hook therefore refuses
// the flags that would leave the task: a --profile other than the task's, or
// given twice (the last one wins in cobra), another database, config, output
// or log file, every profile at once (--all-profiles) and a file for
// `workflow validate` (an oracle on any file).

// taskSessionForbiddenFlags are refused outright in a task session.
var taskSessionForbiddenFlags = []string{"db-path", "config-dir", "output-dir", "log-file"}

// countingValue counts how many times a flag was set.
type countingValue struct {
	pflag.Value
	n *int
}

func (c countingValue) Set(s string) error {
	*c.n++
	return c.Value.Set(s)
}

// countProfileFlag makes root's --profile count its settings.
func countProfileFlag(root *cobra.Command, n *int) {
	if f := root.PersistentFlags().Lookup("profile"); f != nil {
		f.Value = countingValue{f.Value, n}
	}
}

// taskProfileLookup is taskProfile; a var so a test can see when it is called.
var taskProfileLookup = taskProfile

// taskProfile is the id and name of the profile task id belongs to.
func taskProfile(dbPath, taskID string) (id, name, kind string, err error) {
	path := expandPath(dbPath)
	if _, err := os.Stat(path); err != nil {
		return "", "", "", err
	}
	db, err := sql.Open("sqlite", readOnlyURI(path))
	if err != nil {
		return "", "", "", err
	}
	defer db.Close()
	err = db.QueryRow(`SELECT p.id, p.name, t.kind FROM ai_tasks t JOIN profiles p ON p.id = t.profile_id WHERE t.id = ?`, taskID).Scan(&id, &name, &kind)
	return id, name, kind, err
}

// guardTaskSession refuses, in a task session, what would leave the task's
// profile or database. It is a no-op for the operator's own terminal and for
// the supervisor that runs the turn (which carries the task id too but is the
// operator). Fails closed: a task that cannot be looked up is refused. The
// task id in the environment is the single signal that a session is a task's:
// the chat sets it on every confined turn, open or over (chat_aitask.go).
func guardTaskSession(cmd *cobra.Command, dbPath string, profileSets int) error {
	taskID := os.Getenv(aitask.EnvTaskID)
	if taskID == "" || notOperator() == "" {
		return nil
	}
	refuse := func(format string, a ...any) error {
		return errInvalidInput("an AI task's session may not do this: "+format, a...)
	}
	// The flags that move the command to another database or file come first:
	// a refused --db-path is never used for the lookup below. They are refused in
	// every task session, a generic one included.
	for _, name := range taskSessionForbiddenFlags {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			return refuse("--%s is not allowed", name)
		}
	}
	// Only a confined kind's session is held to the rest; a generic task is a
	// full-access coder and can run anything anyway.
	id, name, kind, lookupErr := taskProfileLookup(dbPath, taskID)
	if lookupErr == nil && !chatcli.ConfinedKind(kind) {
		return nil
	}
	var bad error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if bad == nil && (f.Name == "all-profiles" || (f.Name == "file" && strings.HasSuffix(cmd.CommandPath(), "workflow validate"))) {
			bad = refuse("--%s is not allowed", f.Name)
		}
	})
	if bad != nil {
		return bad
	}
	if profileSets > 1 {
		return refuse("--profile is given %d times", profileSets)
	}
	// The default database: the task's own (--db-path is refused above).
	if lookupErr != nil {
		return refuse("its task %s could not be looked up (%v)", taskID, lookupErr)
	}
	given := ""
	if f := cmd.Flags().Lookup("profile"); f != nil {
		given = strings.TrimSpace(f.Value.String())
	}
	if given != "" && given != id && given != name {
		return refuse("--profile %s is not the task's profile", fmt.Sprintf("%q", given))
	}
	return nil
}
