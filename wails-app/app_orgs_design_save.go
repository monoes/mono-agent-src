package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
)

// ── internals ─────────────────────────────────────────────────────────────

// orgDesignRoot resolves the active profile's project root the same way
// app_orgs.go's orgProjectRoot does, so org design reads/writes and the
// existing `monoagentcli org` observe/action surface agree on where an
// org's files live. Returns "" (meaning: use the CLI's own default project
// root, `~/.monoagent`) when there's no usable profile — orgdesign.OrgsDir
// handles the join either way via the same call site pattern as
// orgsDirForActiveProfile.
func (a *App) orgDesignRoot() string {
	return a.orgProjectRoot()
}

// saveAndRespond validates+saves d, runs the CLI validate check, and on
// success emits the live-update event and returns {"ok":true,"rev":sha,
// "org":<fresh doc>}. On any failure it returns the aiError envelope.
func (a *App) saveAndRespond(root string, d *orgdesign.Doc, origin string) string {
	return a.saveAndRespondNew(root, d, origin, false)
}

// saveAndRespondNew is saveAndRespond; signNew says d is a new org the
// user just created (CreateOrgDesign), which may be signed as theirs.
func (a *App) saveAndRespondNew(root string, d *orgdesign.Doc, origin string, signNew bool) string {
	sha, err := a.saveOrgDoc(root, d, signNew)
	if err != nil {
		return aiError(err)
	}
	a.emitOrgDesignUpdated(d.Name, origin, false, d, true, nil)
	b, _ := json.Marshal(map[string]interface{}{"ok": true, "rev": sha, "org": d, "sections_enabled": d.SectionsEnabled()})
	return string(b)
}

// saveOrgDoc runs orgdesign's own Go-side Validate (inside Save), writes
// atomically, registers the write with the watcher so it doesn't
// re-announce our own change, then runs `monoagentcli org validate` as the
// authoritative second check against monomind's real Zod schema. On CLI
// validation failure, restores the pre-write bytes so an org config
// monomind itself can't load is never left on disk, and returns the CLI's
// error text.
//
// The CLI check deliberately runs BEFORE reconcileOrgDoc: reconcile revokes
// the grant rows a document no longer backs, and rollback can only restore
// the FILE. Reconciling first would mean a rejected save leaves the pre-image
// on disk — still listing the role and its `automations` display copies —
// while the rows behind them are gone for good, so the role silently loses
// its tools at runtime with no error anywhere.
func (a *App) saveOrgDoc(root string, d *orgdesign.Doc, signNew bool) (sha string, err error) {
	var preImage *orgdesign.Doc
	if existing, loadErr := orgdesign.Load(root, d.Name); loadErr == nil {
		preImage = existing
		if d.LoadedSHA() == "" {
			// A Doc decoded from JSON has no layout: follow the file's, so
			// the diff stays reviewable. The re-sign rule is unaffected.
			d.InheritLayout(existing)
		}
	}
	// Decided before the first write: whether this save may be re-signed.
	sig := a.orgSignBefore(root, d, signNew)

	if _, err := a.writeOrgDoc(root, d); err != nil {
		return "", err
	}
	if cliErr := a.cliValidate(root, d.Name); cliErr != nil {
		a.rollbackOrgDoc(root, d.Name, preImage)
		return "", fmt.Errorf("monomind rejected this change: %s", cliErr.Error())
	}

	// Only now touch rows — and write the document again, since reconcile
	// rewrites the display copies it just matched against them.
	if err := a.reconcileOrgDoc(root, d, preImage == nil); err != nil {
		a.rollbackOrgDoc(root, d.Name, preImage)
		return "", err
	}
	if sha, err = a.writeOrgDoc(root, d); err != nil {
		return "", err
	}
	a.keepOrgSignature(root, sig, d.Name, sha)
	return sha, nil
}

// keepOrgSignature re-signs an org after the designer's own write when
// monomind 2.21+ requires signed definitions (#288): only if sig (taken
// before the write) allowed it, and only when the write changed a signed
// field. The CLI signs (`org sign --yes --expect-hash`), for the new JSON
// with the instructions files sig pinned, and withdraws the signature if
// monomind signed anything else. An org that still verifies (a cosmetic
// write) is left alone by the CLI, which asks monomind's own check; this
// process never reads the operator key. Anything else leaves the org for
// the Review & sign banner.
func (a *App) keepOrgSignature(root string, sig orgsign.Pre, name, sha string) {
	if !sig.Eligible() {
		return
	}
	raw, cur, err := orgsign.ReadFile(root, name)
	if err != nil || cur != sha {
		return
	}
	want, err := sig.PinnedHash(raw)
	if err != nil {
		log.Printf("org %s was not re-signed after this change: %v", name, err)
		return
	}
	out := a.rawCLI(orgCLITimeout, orgSignArgs(root, name, "--yes", "--expect-hash", want)...)
	var res struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal([]byte(out), &res) == nil && res.Error != "" && res.Code != "org_signing_unsupported" {
		log.Printf("org %s was not re-signed after this change: %s", name, res.Error)
	}
}

// writeOrgDoc saves d and registers the write with the watcher so it doesn't
// re-announce our own change as an external edit.
func (a *App) writeOrgDoc(root string, d *orgdesign.Doc) (string, error) {
	sha, err := orgdesign.Save(root, d)
	if err != nil {
		return "", err
	}
	if a.orgWatcher != nil {
		a.orgWatcher.MarkSelfWrite(d.Name, sha)
	}
	return sha, nil
}

// rollbackOrgDoc restores the pre-write bytes — never leave a config on disk
// that monomind's own schema rejects, even transiently. A save that created
// the file (no pre-image) removes it instead.
func (a *App) rollbackOrgDoc(root, name string, preImage *orgdesign.Doc) {
	if preImage == nil {
		_ = orgdesign.Delete(root, name)
		return
	}
	if sha, err := orgdesign.Save(root, preImage); err == nil && a.orgWatcher != nil {
		a.orgWatcher.MarkSelfWrite(name, sha)
	}
}

// reconcileOrgDoc brings a full-document save from the canvas into line with
// the grant, endpoint, and autonomy rows before it reaches disk, exactly as
// every CLI save does: a stale or hand-edited document can never carry
// grants, tool providers, or an autonomy level no row backs (C-3, C-54).
// A new org gets its starting autonomy row (mid, Q8). The rows are the
// CLI's (`org reconcile-doc`); it hands back the reconciled document, which
// replaces d for the caller to write.
func (a *App) reconcileOrgDoc(root string, d *orgdesign.Doc, isNew bool) error {
	if root == "" {
		return nil // no profile folder: the org lives in the CLI's default root, outside any profile's rows
	}
	body, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode org document: %w", err)
	}
	args := []string{"org", "reconcile-doc", d.Name}
	if isNew {
		args = append(args, "--new", "--by", "gui")
	}
	var res struct {
		Org *orgdesign.Doc `json:"org"`
	}
	if err := a.runMonoCLI(string(body), &res, args...); err != nil {
		return fmt.Errorf("reconcile org rows: %w", err)
	}
	if res.Org == nil {
		return fmt.Errorf("reconcile org rows: the CLI returned no document")
	}
	// The document the CLI sends back was decoded from JSON, so it knows
	// nothing of the file it came from: carry the loaded bytes over, or the
	// write after this reorders the file's keys and the re-sign check loses
	// its sha.
	res.Org.InheritLoaded(d)
	*d = *res.Org
	return nil
}

// cliValidate runs `monoagentcli org validate <name>` and returns a non-nil
// error (the CLI's own message) when it reports invalid.
func (a *App) cliValidate(root, name string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		// No CLI available to double-check with — don't block the save on
		// an environment problem unrelated to the org's own validity; our
		// own Go-side Validate (already run inside orgdesign.Save) is still
		// authoritative for structure even without this second check.
		return nil
	}
	args := []string{"org", "validate", name}
	if root != "" {
		args = append(args, "--project", root)
	}
	cmd := exec.Command(cliBin, args...)
	suppressConsole(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	return orgValidateResult(stdout.Bytes(), stderr.Bytes(), runErr)
}

// orgValidateResult reads `org validate`'s report. The CLI exits 1 for an
// invalid org but still prints its one JSON report on stdout, so the report
// wins over the exit status: its "error" is the problem list. Only when
// there is no report does a failed run fall back to stderr.
func orgValidateResult(stdout, stderr []byte, runErr error) error {
	var payload struct {
		Valid *bool  `json:"valid"`
		Error string `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &payload) == nil && payload.Valid != nil {
		if *payload.Valid {
			return nil
		}
		if payload.Error != "" {
			return fmt.Errorf("%s", payload.Error)
		}
		return fmt.Errorf("invalid org config")
	}
	if runErr != nil {
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return runErr
	}
	return nil
}

// validateDoc runs both the Go-side and CLI-side checks for a read-only
// status report (GetOrgDesign/ValidateOrgDesign), merging their error text.
func (a *App) validateDoc(d *orgdesign.Doc) (bool, []string) {
	var errs []string
	if err := orgdesign.Validate(d); err != nil {
		if ve, ok := err.(*orgdesign.ValidationError); ok {
			errs = append(errs, ve.Errors...)
		} else {
			errs = append(errs, err.Error())
		}
	}
	return len(errs) == 0, errs
}

// emitOrgDesignUpdated fires the single event name the frontend subscribes
// to for the org designer, regardless of what triggered the change (an
// in-app save, the watcher observing an external/AI-driven edit, or a
// deletion).
func (a *App) emitOrgDesignUpdated(orgName, origin string, deleted bool, d *orgdesign.Doc, valid bool, errs []string) {
	payload := map[string]interface{}{
		"v":         1,
		"orgName":   orgName,
		"profileID": a.getActiveProfileID(),
		"origin":    origin, // "ui" | "external"
		"deleted":   deleted,
		"valid":     valid,
		"errors":    errs,
	}
	if d != nil {
		payload["org"] = d
		payload["sections_enabled"] = d.SectionsEnabled()
	} else {
		payload["org"] = nil
	}
	runtime.EventsEmit(a.ctx, "org:designUpdated", payload)
}
