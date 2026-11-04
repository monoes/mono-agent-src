package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	hilcli "github.com/monoes/mono-agent/internal/cli/hil"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	taskcli "github.com/monoes/mono-agent/internal/cli/task"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/storage"
)

// `hil` lives in internal/cli/hil; this is its adapter: the CLI's globals and helpers handed
// over as hilcli.Deps. Who may decide a send (requireOperator), how an approved send runs
// (hilSendRunner) and the publication gate (requirePublicationApproval) stay here: coder and
// publication use them too.

func newHILCmd(cfg *globalConfig) *cobra.Command {
	return hilcli.NewCommand(hilcli.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		ProfileID:       func() string { return cfg.ProfileID },
		OpenDB:          func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:        errNotFound,
		InvalidInput:    errInvalidInput,
		WriteJSON:       writeJSONTo,
		NewTable:        func(w io.Writer, headers []string) *tablewriter.Table { return newPlainTable(w, headers, nil) },
		RequireOperator: cfg.requireOperator,
		SendRunner:      func() hilsend.Runner { return hilSendRunner(cfg) },
		WorkspaceRoot: func(db *sql.DB) (string, error) {
			s, err := loadCoderSettings(db)
			return s.WorkspaceRoot, err
		},
	})
}

// hilSendRunner builds the Runner that performs an approved send. A variable
// so tests run a fake instead of starting a process.
var hilSendRunner = func(cfg *globalConfig) hilsend.Runner {
	return func(ctx context.Context, r hilsend.Run) (json.RawMessage, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		items := make([]map[string]interface{}, 0, len(r.Input))
		for _, in := range r.Input {
			items = append(items, map[string]interface{}{"json": in})
		}
		stdin, err := json.Marshal(map[string]interface{}{"config": r.Config, "input": items})
		if err != nil {
			return nil, err
		}
		args := []string{}
		if cfg.DBPath != "" {
			args = append(args, "--db-path", cfg.DBPath)
		}
		if cfg.ProfileID != "" {
			args = append(args, "--profile", cfg.ProfileID)
		}
		args = append(args, "node", "run", r.NodeType, "--stdin", "--output", "json")
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Env = append(os.Environ(), hilsend.EnvApprovalID+"="+r.ApprovalID, hilsend.EnvApprovalProof+"="+r.Proof)
		cmd.Stdin = bytes.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 800 {
				msg = msg[len(msg)-800:]
			}
			if msg == "" {
				msg = err.Error()
			}
			return nil, errors.New(msg)
		}
		out := bytes.TrimSpace(stdout.Bytes())
		if !json.Valid(out) {
			return json.RawMessage(strconv.Quote(string(out))), nil
		}
		return json.RawMessage(out), nil
	}
}

// requireOperator refuses whatever is not the operator's own terminal: it
// may prepare a send, never decide one, nor approve what agents asked for.
// It is gateVerdict (the one implementation, shared with opgate) worded for a command that
// takes the question as an error.
func (c *globalConfig) requireOperator(what string) error {
	v := c.gateVerdict(false, what)
	if v.err != nil {
		return v.err
	}
	if v.why != "" {
		return operatorOnlyError("%s%sonly the operator can %s; run it in your own terminal", v.why, v.sep, what)
	}
	return nil
}

// gateVerdict is the answer of the operator gate. why is "" for the operator. err, when set, is the
// error to hand back as it is (the task caller's refusal, a turn check that failed); why is then its
// reason in a sentence. sep joins why to "only the operator can ...".
type gateVerdict struct {
	why, sep string
	err      error
}

// gateVerdict asks, in this order, like the person gate of the task verbs: a process that holds an
// agent token (an agent, whatever else its environment says); anything the environment and the
// operator marker ancestry name as not the operator; a process inside a coder session's turn (a
// model's shell that carries no marker). A courtesy layer, not proof: SECURITY.md says so.
//
// recovery is for the commands that repair an install: an unreadable or absent database, or a turn
// registry entry that cannot be read, is then "no turn recorded" and does not refuse; a turn
// recorded in a readable database still does. Otherwise a process that cannot be placed is refused.
func (c *globalConfig) gateVerdict(recovery bool, what string) gateVerdict {
	for _, key := range taskcli.PersonTokenEnv() {
		if os.Getenv(key) != "" {
			return gateVerdict{why: key + " is set, so a process holding an agent token is running this command", sep: "; "}
		}
	}
	if _, err := callerFor("").operator(what); err != nil {
		why := err.Error()
		if i := strings.Index(why, ": only the operator can "); i >= 0 {
			why = why[:i]
		}
		return gateVerdict{why: why, err: err}
	}
	if why := notOperator(); why != "" {
		return gateVerdict{why: why, sep: ": "}
	}
	in, err := c.callerInATurnFor(recovery)
	if err != nil {
		// fails closed: a process that cannot be placed is not let through
		return gateVerdict{why: "cannot tell whether a coder session's turn runs this command (" + err.Error() + ")", err: err}
	}
	if in {
		return gateVerdict{why: "this command runs inside a coder session's turn, so a model is running it", sep: "; "}
	}
	return gateVerdict{}
}

// gateRefusal is the body cmd/monoagentcli registers with opgate: why this process is not the user's
// own terminal, "" for the user.
func (c *globalConfig) gateRefusal(recovery bool) string {
	return c.gateVerdict(recovery, "do this").why
}

// registerOperatorGate makes cfg's turn-aware gate the answer of every command that asks opgate. It runs
// when the real command tree is built, before any command, so a process that never builds it (library use,
// the tests of other packages) keeps operator.Refusal.
// recoverySkippedEntry says on stderr that a recovery command skipped an unreadable turn-registry entry (the
// error names the database and the entry file), so a turn hidden behind it is not silent. A seam for tests.
var recoverySkippedEntry = func(err error) { fmt.Fprintln(os.Stderr, "warning: "+err.Error()) }

func registerOperatorGate(cfg *globalConfig) {
	opgate.Register(cfg.gateRefusal)
	opgate.RegisterRecovery(cfg.gateRecovery)
}

// gateRecovery is gateRefusal(true) that also hands back the busy-host error, when that is why the command
// is refused, so the command can say "wait and retry" instead of "only the operator can".
func (c *globalConfig) gateRecovery() (string, error) {
	v := c.gateVerdict(true, "do this")
	if v.err != nil && errors.Is(v.err, coderhost.ErrDatabaseBusy) {
		return v.why, v.err
	}
	return v.why, nil
}

// callerInATurn says whether this process descends from a recorded coder turn. It never creates
// the database: no file (a fresh machine, doctor before init) or no coder_sessions table means no
// turn was ever recorded. A database that cannot be read or queried is an error (the caller refuses).
func (c *globalConfig) callerInATurn() (bool, error) { return c.callerInATurnFor(false) }

// callerInATurnFor is callerInATurn for a recovery command when recovery is set: every error becomes
// "not in a turn" (see gateVerdict), and the registry's failure does not hide the named database.
func (c *globalConfig) callerInATurnFor(recovery bool) (bool, error) {
	if inClientMode() {
		return false, nil // client mode never opens the local database (client_mode.go)
	}
	path := expandPath(c.DBPath)
	// The database the process names is only half of the question: --db-path or another HOME must not hide a
	// turn, so every running coder host's database (the turn registry) is checked too. An override only adds checks.
	var in bool
	var busy []error
	if recovery {
		// an unreadable entry is skipped, never allowed to hide a turn recorded in a readable database
		var skipped []error
		in, skipped, busy = coderhost.CallerInARegisteredTurnLenient(path)
		for _, e := range skipped {
			recoverySkippedEntry(e)
		}
	} else {
		var err error
		in, err = coderhost.CallerInARegisteredTurn(path)
		if err != nil && !in {
			return false, err
		}
	}
	if in {
		return true, nil
	}
	if recovery {
		return recoveryNamedVerdict(path, busy)
	}
	return callerInATurnNamed(path)
}

// recoveryNamedVerdict is the last step of the recovery flavour: the database the process names, then the busy
// live hosts the registry scan found. A lock on the database of a live coder host could be hiding a turn:
// refuse with "wait and retry", naming every busy host. A dead host's lock, or a database no live host owns,
// never locks the operator out of a repair, and is not waited for.
func recoveryNamedVerdict(path string, busy []error) (bool, error) {
	in, err := coderhost.CallerInATurnAtRecovery(path)
	if err == nil {
		if in {
			return true, nil
		}
	} else if file, ok := coderhost.LiveHostEntryOf(path); ok && errors.Is(err, coderhost.ErrDatabaseBusy) {
		busy = append([]error{&coderhost.BusyHostError{DB: path, File: file}}, busy...)
	}
	if joined := coderhost.JoinBusy(busy...); joined != nil {
		return false, joined
	}
	return false, nil
}

// callerInATurnNamed is the check of the database the process names, as it was before the registry. It is
// coderhost.CallerInATurnAt, the registry's own check: the file is opened read-only (never created or
// migrated), only a regular file is opened (a FIFO cannot hang it) and a locked database is waited for
// at most busy_timeout. Its errors refuse a non-recovery verb; a recovery verb treats them as "not in a turn".
func callerInATurnNamed(path string) (bool, error) { return coderhost.CallerInATurnAt(path) }

// notOperator says why this process is not the operator's own terminal, or
// "" (internal/operator): an agent, a process of any kind of agent, a chat
// tool call's run, or a process a workflow node started.
func notOperator() string { return operator.Refusal() }

// requirePublicationApproval gates `publication register` from whatever is not
// the operator's own terminal (notOperator: an agent, the chat's runs, what a
// workflow node starts): a publication it claims must be the one a send the
// operator approved made, and that approval registers it alone
// (hilsend.ClaimPublicationApproval). approvalID names the send item (as returned
// when it was queued); an approved send registers its own publication, so this is
// only for a publication made outside that path. Operators, and profiles that
// turned sendApproval off, are not asked. release gives the claim back when the
// registration fails.
func requirePublicationApproval(cfg *globalConfig, approvalID string, e publication.Entry) (release func(), err error) {
	release = func() {}
	if notOperator() == "" {
		return release, nil
	}
	var claim *hilsend.PublicationClaim
	err = withImageDB(cfg, func(db *sql.DB) error {
		var cerr error
		claim, cerr = hilsend.ClaimPublicationApproval(context.Background(), db, cfg.ProfileID, approvalID, e)
		if errors.Is(cerr, hilsend.ErrPublicationNeedsApproval) {
			return operatorOnlyError("registering a publication from an agent needs an approved send: prepare the send (it is queued for approval, and registers itself once approved), or pass --approval <hil id> of a send that was approved and sent. Asked because %s", notOperator())
		}
		if cerr != nil {
			return operatorOnlyError("%v (asked because %s)", cerr, notOperator())
		}
		return nil
	})
	if err != nil || claim == nil {
		return release, err
	}
	return func() {
		_ = withImageDB(cfg, func(db *sql.DB) error { claim.Release(context.Background(), db); return nil })
	}, nil
}
