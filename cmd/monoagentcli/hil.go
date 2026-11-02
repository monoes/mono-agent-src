package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	hilcli "github.com/monoes/mono-agent/internal/cli/hil"
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
// In this order, like the person gate of the task verbs: a process that holds an agent token (an
// agent, whatever else its environment says); anything the environment and the operator marker
// ancestry name as not the operator; a process inside a coder session's turn (a model's shell
// that carries no marker). A courtesy layer, not proof: SECURITY.md says so.
func (c *globalConfig) requireOperator(what string) error {
	for _, key := range taskcli.PersonTokenEnv() {
		if os.Getenv(key) != "" {
			return operatorOnlyError("%s is set, so a process holding an agent token is running this command; only the operator can %s; run it in your own terminal", key, what)
		}
	}
	if _, err := callerFor("").operator(what); err != nil {
		return err
	}
	if why := notOperator(); why != "" {
		return operatorOnlyError("%s: only the operator can %s; run it in your own terminal", why, what)
	}
	in, err := c.callerInATurn()
	if err != nil {
		return err // fails closed: a process that cannot be placed is not let through
	}
	if in {
		return operatorOnlyError("this command runs inside a coder session's turn, so a model is running it; only the operator can %s; run it in your own terminal", what)
	}
	return nil
}

// callerInATurn says whether this process descends from a recorded coder turn. It never creates
// the database: no file (a fresh machine, doctor before init) or no coder_sessions table means no
// turn was ever recorded. A database that cannot be read or queried is an error (the caller refuses).
func (c *globalConfig) callerInATurn() (bool, error) {
	if inClientMode() {
		return false, nil // client mode never opens the local database (client_mode.go)
	}
	path := expandPath(c.DBPath)
	// The database the process names is only half of the question: --db-path or another HOME must not hide a
	// turn, so every running coder host's database (the turn registry) is checked too. An override only adds checks.
	if in, err := coderhost.CallerInARegisteredTurn(path); err != nil || in {
		return in, err
	}
	return callerInATurnNamed(path)
}

// callerInATurnNamed is the check of the database the process names, as it was before the registry.
func callerInATurnNamed(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'coder_sessions'`).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	return coderhost.NewStore(db.DB).CallerInATurn()
}

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
