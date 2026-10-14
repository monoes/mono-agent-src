package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	hilcli "github.com/monoes/mono-agent/internal/cli/hil"
	"github.com/monoes/mono-agent/internal/hilsend"
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
		RequireOperator: requireOperator,
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

func hilSendStore(cfg *globalConfig, db *sql.DB) hilsend.Store {
	return hilsend.Store{DB: db, Profile: cfg.ProfileID}
}

// requireOperator refuses an agent: it may prepare a send, never decide one.
func requireOperator(what string) error {
	if _, err := callerFor("").operator(what); err != nil {
		return err
	}
	if _, agent := hilsend.Detect(); agent {
		return operatorOnlyError("%s is set, so an agent is running this command: only the operator can %s; run it in your own terminal", hilsend.EnvAgentContext, what)
	}
	return nil
}

// requirePublicationApproval gates `publication register` from an agent: a
// publication it claims must be one the operator approved. approvalID names the
// send item (as returned when it was queued); an approved send registers its
// own publication, so this is only for a publication made outside that path.
// Operators, and profiles that turned sendApproval off, are not asked.
func requirePublicationApproval(cfg *globalConfig, approvalID string) error {
	if _, agent := hilsend.Detect(); !agent {
		return nil
	}
	return withImageDB(cfg, func(db *sql.DB) error {
		if !hilsend.Enabled(db) {
			return nil
		}
		if approvalID != "" {
			if p, err := hilSendStore(cfg, db).Get(context.Background(), approvalID); err == nil && p.State == hilsend.StateSent {
				return nil
			}
		}
		return operatorOnlyError("registering a publication from an agent needs an approved send: prepare the send (it is queued for approval, and registers itself once approved), or pass --approval <hil id> of a send that was approved and sent")
	})
}
