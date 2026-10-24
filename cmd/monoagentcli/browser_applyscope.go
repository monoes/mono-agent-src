package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// browserTaskSession is the browser-capable confined task this process is the
// session of, nil when it is none. It fails CLOSED: a session that says it is
// a task's (the task id the chat puts in the environment of every confined turn,
// or the apply kind token) whose task cannot be looked up, is not open, or is a
// browser-capable confined kind without the vouched apply token, is refused,
// never treated as an ordinary caller whose tab and files nothing scopes.
func browserTaskSession(cfg *globalConfig, db *sql.DB) (*aitask.Task, error) {
	envID := strings.TrimSpace(os.Getenv(aitask.EnvTaskID))
	apply := browserctl.AgentKind() == browserctl.KindApply
	vouched := ""
	if apply {
		if vouched = browserctl.ApplyTaskID(); vouched == "" {
			return nil, errInvalidInput("this apply session carries no task, so it may not read or write files")
		}
	}
	if envID == "" && vouched == "" {
		return nil, nil
	}
	if envID != "" && vouched != "" && envID != vouched {
		return nil, errInvalidInput("this session's task id (%s) is not the one its token names (%s)", envID, vouched)
	}
	id := vouched
	if id == "" {
		id = envID
	}
	t, err := (&aitask.Store{DB: db, Profile: cfg.ProfileID}).Get(id)
	if err != nil {
		return nil, errInvalidInput("this session's task %s could not be looked up in this profile, so it may not use the browser", id)
	}
	browserKind := t.Kind == applyfill.Kind || aitask.IsExtensionKind(t.Kind)
	if !browserKind {
		if vouched != "" {
			return nil, errInvalidInput("this apply session's task %s is not an application-apply or extension task of this profile", id)
		}
		return nil, nil // another kind's session (a full-access task, a confined reader): not scoped here
	}
	if vouched != id {
		return nil, errInvalidInput("task %s's browser session is not vouched for by its apply token, so it may not use the browser", id)
	}
	if t.Status != aitask.StatusRunning && t.Status != aitask.StatusNeedsUser {
		return nil, errInvalidInput("this session's task %s is not open", id)
	}
	return &t, nil
}

// applyScope is where this process may read and write files when it is an
// apply-kind agent (an application-apply or extension task's turn), nil for any
// other caller: the task's context snapshot (for an extension task only its
// document/ folder, the files meant to be attached) to read, its work folder to
// write. Its folders are next to the database. A browser-capable task session
// whose task cannot be established is refused.
func applyScope(cfg *globalConfig) (*browserctl.ApplyScope, error) {
	db, err := initDB(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	t, err := browserTaskSession(cfg, db.DB)
	if err != nil || t == nil {
		return nil, err
	}
	snapshot := aitask.SnapshotDir(filepath.Dir(clicore.ExpandPath(cfg.DBPath)), t.ID)
	read := snapshot
	if aitask.IsExtensionKind(t.Kind) {
		// The facts, the page description and the queue record are in the snapshot
		// too, and none of them is a file to attach.
		read = filepath.Join(snapshot, "document")
	}
	return &browserctl.ApplyScope{ContextDir: read, WorkDir: filepath.Join(filepath.Dir(snapshot), "work")}, nil
}

// scopeError is the CLI's refusal of a path outside the apply scope.
func scopeError(err error) error {
	return browserCLIError{error: errInvalidInput("%v", err), code: "path_not_allowed"}
}

// extensionTarget is the one tab an extension task's turn may act on: the
// tab, browser and origin recorded when the person queued it, read from the
// task's verified context snapshot. nil for every caller that is not an
// extension task's session; one whose task or record cannot be established is
// refused (browserTaskSession fails closed).
func extensionTarget(cfg *globalConfig, db *sql.DB) (*aitask.ExtensionTarget, error) {
	t, err := browserTaskSession(cfg, db)
	if err != nil || t == nil || !aitask.IsExtensionKind(t.Kind) {
		return nil, err
	}
	snapshot := aitask.SnapshotDir(filepath.Dir(clicore.ExpandPath(cfg.DBPath)), t.ID)
	raw, err := os.ReadFile(filepath.Join(snapshot, filepath.FromSlash(aitask.ExtensionTargetFile)))
	var tg aitask.ExtensionTarget
	if err != nil || json.Unmarshal(raw, &tg) != nil || tg.Tab <= 0 || tg.Origin == "" {
		return nil, errInvalidInput("task %s has no record of the tab it works on", t.ID)
	}
	return &tg, nil
}

// extensionApproved checks a held fill or upload an extension session is about
// to carry out: it must be for the queued tab and origin, whatever the item says.
func extensionApproved(cfg *globalConfig, db *sql.DB, it browserctl.SubmitItem) (*aitask.ExtensionTarget, error) {
	tgt, err := extensionTarget(cfg, db)
	if err != nil || tgt == nil {
		return tgt, err
	}
	if it.TabID != tgt.Tab || it.Origin != tgt.Origin {
		return nil, browserCLIError{error: errInvalidInput("this session works on tab %d (%s) only, and that approval is for another page", tgt.Tab, tgt.Origin), code: "tab_pinned"}
	}
	return tgt, nil
}
