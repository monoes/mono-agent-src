package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
)

// `monoagentcli browser …` drives the user's own browser through the
// extension bridge (internal/browserctl). Plan C2 in
// docs/mastermind/plans/2026-10-15-coder-browser.md.

// browserSubcommands lets other files add `browser` subcommands from their
// own init() without editing this file.
var browserSubcommands []func(*globalConfig) *cobra.Command

// browserBridge finds the running bridge; tests stand in a fake.
var browserBridge = browserctl.FindBridge

const browserLong = `Drive your own browser — the one with the MonoAgent Bridge extension — from the
command line: open and navigate tabs, click and type, take screenshots, read the
console and failed requests, and ask you to pick an element.

Pass --profile <id> with every command but ` + "`browser browsers`" + `, ` + "`browser allow`" + ` and
` + "`browser diff`" + `: it routes to the browser bound to that profile, and held submits
belong to it. Every command acts on one tab: --tab, else the tab this context used
last, else the active tab (for an agent, the active tab of its own tab group).
--browser picks one of several attached browsers (see ` + "`browser browsers`" + `).

Run by an agent (Claude Code, Codex, an org role, a coder chat), the commands do what
that kind of agent's level allows (` + "`browser permissions`" + `, Settings → Agent browser
permissions): by default coder chats, org roles and other agents drive only local and
dev pages (localhost, *.localhost, 127.0.0.0/8, ::1, *.local, *.test, private network
addresses) and the sites you allowed, and the in-app chat any site; a site you block
(and its subdomains, on any scheme and port) is refused at every level, and file: and
browser-internal pages never. Refusals exit 3 with the code browser_off, site_blocked,
origin_not_allowed, site_needs_approval (with the hil_id that asks you), site_rejected
(you said no to it in the last 10 minutes) or site_asks_full (20 sites already wait for
you). An agent's form submit on a page that is not local waits for your approval
(except at full-no-approvals): exit 3 with {"status":"awaiting_approval","hil_id":…};
approve it with
` + "`monoagentcli --profile <id> hil approve <hil_id>`" + ` or on the Human in Loop page, then
the agent runs ` + "`monoagentcli --profile <id> browser submit --hil <hil_id>`" + `. This is
a guard against mistakes and prompt injection from pages, not a sandbox: an agent
with full access to this machine can reach the bridge directly. A terminal you run
yourself is not restricted.`

func newBrowserCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browser",
		Short: "Drive your browser through the extension: tabs, clicks, screenshots, console, network",
		Long:  browserLong,
		Example: `  monoagentcli --profile <id> browser open http://localhost:3000
  monoagentcli --profile <id> browser click "button.save"
  monoagentcli --profile <id> --json browser screenshot --full
  monoagentcli --profile <id> --json browser console --level error`,
	}
	cmd.PersistentFlags().String("browser", "", "Attached browser to use: an instance id (4+ leading characters) or label")
	cmd.PersistentFlags().Int("tab", 0, "Tab id to act on (see browser tabs)")
	builtin := []func(*globalConfig) *cobra.Command{
		newBrowserBrowsersCmd, newBrowserTabsCmd, newBrowserOpenCmd, newBrowserNavigateCmd, newBrowserReloadCmd,
		newBrowserCloseCmd, newBrowserClickCmd, newBrowserTypeCmd, newBrowserPressCmd, newBrowserWaitCmd,
		newBrowserEvalCmd, newBrowserScreenshotCmd, newBrowserConsoleCmd, newBrowserNetworkCmd, newBrowserA11yCmd,
		newBrowserPickCmd, newBrowserAllowCmd, newBrowserPermissionsCmd, newBrowserSubmitCmd, newBrowserUploadCmd, newBrowserFillCmd,
	}
	for _, f := range append(builtin, browserSubcommands...) {
		cmd.AddCommand(f(cfg))
	}
	return cmd
}

// browserRun is one browser command's session and the database it reads
// the allowlist from and writes approvals to.
type browserRun struct {
	cfg *globalConfig
	cmd *cobra.Command
	s   *browserctl.Session
	db  *storage.Database
}

// openBrowser opens the session a browser command acts on: the --browser
// and --tab flags, the active profile's browser, and (unless noTab) the tab,
// which an agent may use only on an allowed page. Close it when done.
func openBrowser(cmd *cobra.Command, cfg *globalConfig, noTab bool) (*browserRun, error) {
	sender, err := browserBridge()
	if err != nil {
		return nil, browserErr(cmd, cfg, err)
	}
	db, err := initDB(cfg)
	if err != nil {
		return nil, fmt.Errorf("initializing database: %w", err)
	}
	perms, err := browserctl.LoadPermissions(db.DB)
	if err != nil {
		db.Close()
		return nil, err
	}
	b, _ := cmd.Flags().GetString("browser")
	tab, _ := cmd.Flags().GetInt("tab")
	// An extension task works on the tab it was queued for and no other,
	// whatever --tab and --browser say, and submits wait for a person on
	// local pages too.
	tgt, err := extensionTarget(cfg, db.DB)
	if err != nil {
		db.Close()
		return nil, err
	}
	if tgt != nil {
		if (tab != 0 && tab != tgt.Tab) || (b != "" && b != tgt.Browser) {
			db.Close()
			return nil, browserCLIError{error: errInvalidInput("this session works on tab %d only: leave out --tab and --browser", tgt.Tab), code: "tab_pinned"}
		}
		tab, b = tgt.Tab, tgt.Browser
	}
	s, err := browserctl.Open(browserctl.Options{
		Browser: b, Profile: cfg.ProfileID, Tab: tab, NoTab: noTab, Permissions: &perms, DB: db.DB, Sender: sender,
		HoldLocalSubmits: tgt != nil, PinOrigin: pinOriginOf(tgt),
	})
	if err != nil {
		db.Close()
		return nil, browserErr(cmd, cfg, err)
	}
	if tgt != nil && !noTab {
		if now := browserctl.Origin(s.URL()); s.TabID() != tgt.Tab || now != tgt.Origin {
			db.Close()
			return nil, browserCLIError{error: errInvalidInput("tab %d is on %s now, not %s as when the task was queued: it stopped", tgt.Tab, now, tgt.Origin), code: "tab_moved"}
		}
	}
	return &browserRun{cfg: cfg, cmd: cmd, s: s, db: db}, nil
}

// pinOriginOf is the origin an extension session is pinned to, "" for none.
func pinOriginOf(tgt *aitask.ExtensionTarget) string {
	if tgt == nil {
		return ""
	}
	return tgt.Origin
}

func (r *browserRun) Close() { r.db.Close() }

// act runs one action on the tab inside the submit gate: the guard is armed
// first (and a submit it blocked since the last command reported), and a
// submit the action set off is reported after it.
func (r *browserRun) act(fn func() error) error {
	ctx := r.cmd.Context()
	if err := r.s.BeforeAct(ctx, r.db.DB); err != nil {
		return r.err(err)
	}
	if err := fn(); err != nil {
		return r.err(err)
	}
	if r.s.Gated() {
		browserctl.Settle()
	}
	return r.err(r.s.AfterAct(ctx, r.db.DB))
}

func (r *browserRun) err(err error) error { return browserErr(r.cmd, r.cfg, err) }

// out prints v as JSON with --json, else the human line(s) human writes.
func (r *browserRun) out(v any, human func()) error { return browserOut(r.cmd, r.cfg, v, human) }

func browserOut(cmd *cobra.Command, cfg *globalConfig, v any, human func()) error {
	if cfg.JSONOutput {
		return writeJSONTo(cmd.OutOrStdout(), v)
	}
	human()
	return nil
}

// browserCLIError carries a --json error code beyond the exit-code class.
type browserCLIError struct {
	error
	code  string
	extra map[string]any
}

func (e browserCLIError) Unwrap() error { return e.error }

func (e browserCLIError) JSONErrorFields() map[string]any {
	m := map[string]any{"code": e.code}
	for k, v := range e.extra {
		m[k] = v
	}
	return m
}

// browserErr maps browserctl's and the bridge's errors to the CLI's exit
// codes. A held submit is printed as {"status":"awaiting_approval",…} and
// exits 3.
func browserErr(cmd *cobra.Command, cfg *globalConfig, err error) error {
	if err == nil {
		return nil
	}
	var aw *browserctl.AwaitingApproval
	var na *browserctl.NotAllowedError
	var bnf *browserctl.BrowserNotFoundError
	var nb *extension.NoBrowserError
	var se *browserctl.SubmitStateError
	var changed *browserctl.SubmitChangedError
	var blankTab *browserctl.BlankTabError
	var pinned *browserctl.PinnedOriginError
	var off *browserctl.BrowserOffError
	var blocked *browserctl.SiteBlockedError
	var pending *browserctl.SiteApprovalPendingError
	var ask *browserctl.SiteNeedsApprovalError
	var rejected *browserctl.SiteRejectedError
	var full *browserctl.SiteAsksFullError
	switch {
	case errors.As(err, &off):
		extra := map[string]any{"kind": off.Kind}
		if off.Declared != "" {
			extra["declared"] = off.Declared
		}
		return browserCLIError{error: errInvalidInput("%s", off.Error()), code: "browser_off", extra: extra}
	case errors.As(err, &blocked):
		return browserCLIError{error: errInvalidInput("%s", blocked.Error()), code: "site_blocked", extra: map[string]any{"origin": blocked.Origin}}
	case errors.As(err, &pending):
		return browserCLIError{error: errInvalidInput("%s", pending.Error()), code: "site_needs_approval",
			extra: map[string]any{"hil_id": pending.HILID, "origin": pending.Origin}}
	case errors.As(err, &ask):
		return browserCLIError{error: errInvalidInput("%s", ask.Error()), code: "site_needs_approval", extra: map[string]any{"origin": ask.Origin}}
	case errors.As(err, &rejected):
		return browserCLIError{error: errInvalidInput("%s", rejected.Error()), code: "site_rejected", extra: map[string]any{"origin": rejected.Origin}}
	case errors.As(err, &full):
		return browserCLIError{error: errInvalidInput("%s", full.Error()), code: "site_asks_full"}
	case errors.As(err, &aw):
		if cfg.JSONOutput {
			_ = writeJSONTo(cmd.OutOrStdout(), map[string]any{
				"status": "awaiting_approval", "hil_id": aw.HILID, "origin": aw.Origin, "url": aw.URL, "via": aw.Via,
				"hint": fmt.Sprintf("ask the user to approve it (monoagentcli %s hil approve %s, or the Human in Loop page), then run monoagentcli %s browser %s --hil %s",
					browserctl.ProfileFlag(aw.Profile), aw.HILID, browserctl.ProfileFlag(aw.Profile), awaitingVerb(aw), aw.HILID),
			})
		}
		return reportedError{Err: errInvalidInput("%s", aw.Error())}
	case errors.As(err, &na):
		return browserCLIError{error: errInvalidInput("%s", na.Error()), code: "origin_not_allowed", extra: map[string]any{"origin": na.Origin}}
	case errors.As(err, &se):
		return browserCLIError{error: errInvalidInput("%s", se.Error()), code: "submit_" + se.Status}
	case errors.As(err, &changed):
		return browserCLIError{error: errInvalidInput("%s", changed.Error()), code: "submit_changed"}
	case errors.Is(err, browserctl.ErrSubmitUnbound):
		return browserCLIError{error: errInvalidInput("%v", err), code: "submit_unbound"}
	case errors.As(err, &pinned):
		return browserCLIError{error: errInvalidInput("%s", pinned.Error()), code: "tab_moved"}
	case errors.As(err, &blankTab):
		return browserCLIError{error: errInvalidInput("%s", blankTab.Error()), code: "tab_blank"}
	case errors.Is(err, browserctl.ErrNoBridge), errors.Is(err, extension.ErrNoExtension), errors.Is(err, extension.ErrBrowserGone),
		errors.Is(err, extension.ErrRelayUnauthorized), errors.As(err, &nb):
		return errAuthConnection("%v", err)
	case errors.Is(err, browserctl.ErrNoTab), errors.As(err, &bnf), errors.Is(err, browserctl.ErrSubmitNotFound):
		return errNotFound("%v", err)
	case errors.Is(err, browserctl.ErrNotSubmitItem):
		return errInvalidInput("%v", err)
	case errors.Is(err, browserctl.ErrApprovedElsewhere):
		return browserCLIError{error: errInvalidInput("%v", err), code: "submit_not_operator_approved"}
	}
	msg := err.Error()
	if strings.Contains(msg, browserctl.OriginChangedCode) {
		return browserCLIError{error: errInvalidInput("the tab left the page this command was checked for, so the browser did not run it: %s",
			strings.TrimPrefix(msg, "extension error: ")), code: browserctl.OriginChangedCode}
	}
	if strings.Contains(msg, "Element not found") || strings.Contains(msg, "Timed out waiting for element") {
		return errNotFound("%s", strings.TrimPrefix(msg, "extension error: "))
	}
	if strings.Contains(msg, "Unknown command type") {
		return fmt.Errorf("%s: the MonoAgent extension in that browser is older than this command — update it, or reload it from the browser's extensions page", strings.TrimPrefix(msg, "extension error: "))
	}
	if strings.Contains(msg, "No tab with id") {
		return errNotFound("%s — see `monoagentcli %s browser tabs`", strings.TrimPrefix(msg, "extension error: "), browserctl.ProfileFlag(cfg.ProfileID))
	}
	return err
}

// awaitingVerb is the command that carries out a held action once approved.
func awaitingVerb(aw *browserctl.AwaitingApproval) string {
	switch aw.Via {
	case browserctl.ViaUpload:
		return "upload"
	case browserctl.ViaFill:
		return "fill"
	}
	return "submit"
}

// browserCut shortens s to at most n runes for a table cell.
func browserCut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
