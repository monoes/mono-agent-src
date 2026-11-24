package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
)

// ─────────────────────────────────────────────────────────────────────────────
// Signed org definitions (#288, monomind#502). monomind 2.21 runs an org
// only when the operator signed its definition. The org view and designer
// show a banner for an unsigned or changed org; "Review & sign" shows
// monomind's own review and signs only after the user confirms, and only
// the definition that was reviewed: the review's `hash` (the projection
// hash, instructions files included), which the CLI hands over only when
// no file moved during monomind's read. Signing is always `monoagentcli
// org sign`. The designer's own writes are decided in orgSignBefore: the
// CLI's verdict, then orgsign.Before's pinning (read-only checks).
// ─────────────────────────────────────────────────────────────────────────────

// orgSignArgs builds `org [--project <root>] sign <org> [extra...]`.
func orgSignArgs(root, orgName string, extra ...string) []string {
	args := []string{"org"}
	if root != "" {
		args = append(args, "--project", root)
	}
	return append(append(args, "sign", orgName), extra...)
}

// issueOrgSignNonce is the operator's one-time nonce, stubbed in tests.
var issueOrgSignNonce = operator.IssueNonce

// orgSignYesArgs are the extra arguments of a signing `org sign --yes`: the pinned hash and, because the CLI
// is run without a terminal here, the one-time nonce this process issues for exactly that definition. The app is
// the operator's own act (the review dialog's confirm button, or the re-sign after the designer saved), so it
// can issue the nonce itself. If none can be issued the CLI is still called without one and says why it refuses.
func orgSignYesArgs(orgName, hash string) []string {
	args := []string{"--yes", "--expect-hash", hash}
	if nonce, err := issueOrgSignNonce(operator.OrgSignNoncePurpose, orgName+":"+hash, true); err == nil {
		args = append(args, "--nonce", nonce)
	}
	return args
}

// OrgSignatureStatus is `org sign <org> --status`: {supported, state,
// detail, sha256, message}. supported is false below monomind 2.21.
// sha256 is the file's; nothing is signed by it.
func (a *App) OrgSignatureStatus(orgName string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult(orgSignArgs(a.orgProjectRoot(), orgName, "--status")...)
}

// OrgSignatureReview is `org sign <org>` without --yes: monomind's review
// text and the hash of exactly the reviewed definition ("" when a file
// moved during the review, and then nothing may be signed from it). It
// signs nothing.
func (a *App) OrgSignatureReview(orgName string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult(orgSignArgs(a.orgProjectRoot(), orgName)...)
}

// OrgSign signs the reviewed definition. The frontend calls it only from
// the review dialog's confirm button; hash is the review's projection hash
// (instructions files included), so a definition that changed since is
// refused rather than signed.
func (a *App) OrgSign(orgName, hash string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult(orgSignArgs(a.orgProjectRoot(), orgName, orgSignYesArgs(orgName, hash)...)...)
}

// orgSignSupport caches whether the installed monomind requires signed
// org definitions, as `org sign --status` last said, so a monomind below
// 2.21 costs one CLI call every few minutes rather than one per save.
type orgSignSupport struct {
	mu        sync.Mutex
	known, ok bool
	at        time.Time
}

const orgSignSupportTTL = 5 * time.Minute

func (c *orgSignSupport) get() (ok, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ok, c.known && time.Since(c.at) < orgSignSupportTTL
}

func (c *orgSignSupport) set(ok bool) {
	c.mu.Lock()
	c.known, c.ok, c.at = true, ok, time.Now()
	c.mu.Unlock()
}

// cliVerdict hands orgsign.Before the CLI's answer as its Checker (the CLI
// uses monomind's own --check where monomind has it). It never signs.
type cliVerdict struct{ st orgsign.Status }

func (cliVerdict) Sign(context.Context, string, string, string) error {
	return errors.New("the designer signs through the CLI")
}

func (v cliVerdict) Check(context.Context, string, string) (orgsign.Status, bool) { return v.st, true }

// orgSignBefore decides, before a designer write, whether it may be
// re-signed: by the CLI's verdict on the org as it is now (`org sign
// --status`), then orgsign.Before's own checks (loaded from exactly the
// signed bytes, instructions files pinned). Nothing is eligible below
// monomind 2.21.
func (a *App) orgSignBefore(root string, d *orgdesign.Doc, signNew bool) orgsign.Pre {
	if ok, known := a.orgSignSupport.get(); known && !ok {
		return orgsign.Pre{}
	}
	var res struct {
		Supported *bool  `json:"supported"`
		State     string `json:"state"`
		Detail    string `json:"detail"`
		Error     string `json:"error"`
		Code      string `json:"code"`
	}
	out := a.rawCLI(orgCLITimeout, orgSignArgs(root, d.Name, "--status")...)
	if json.Unmarshal([]byte(out), &res) != nil {
		return orgsign.Pre{}
	}
	if res.Supported != nil {
		a.orgSignSupport.set(*res.Supported)
		if !*res.Supported {
			return orgsign.Pre{}
		}
	}
	if res.Code == "not_found" {
		// A new org: signed only when it is the user's own (signNew), and
		// only where monomind is known to take signatures.
		if ok, known := a.orgSignSupport.get(); !known || !ok {
			return orgsign.Pre{}
		}
		return orgsign.Before(context.Background(), nil, root, d.Name, d.LoadedSHA(), signNew)
	}
	if res.Error != "" || res.State == "" {
		return orgsign.Pre{}
	}
	return orgsign.Before(context.Background(), cliVerdict{orgsign.Status{State: res.State, Detail: res.Detail}},
		root, d.Name, d.LoadedSHA(), signNew)
}
