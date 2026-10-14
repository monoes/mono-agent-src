package main

import (
	"database/sql"
	"io"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	capturecli "github.com/monoes/mono-agent/internal/cli/capture"
	"github.com/monoes/mono-agent/internal/storage"
)

// `capture` lives in internal/cli/capture; this is its adapter: the CLI's globals and helpers
// handed over as capturecli.Deps, with the extension bridge `capture page` captures through and
// the org environment `capture task` files onto.

// captureErrDeps is the part of captureDeps that needs no globalConfig: the CLI's errors.
var captureErrDeps = capturecli.Deps{NotFound: errNotFound, InvalidInput: errInvalidInput, AuthConnection: errAuthConnection}

func captureDeps(cfg *globalConfig) capturecli.Deps {
	d := captureErrDeps
	d.JSONOutput = func() bool { return cfg.JSONOutput }
	d.ProfileID = func() string { return cfg.ProfileID }
	d.DBPath = func() string { return cfg.DBPath }
	d.OpenDB = func() (*storage.Database, error) { return initDB(cfg) }
	d.NewTable = func(w io.Writer, headers []string) *tablewriter.Table {
		return newPlainTable(w, headers, nil)
	}
	d.Page = capturecli.PageDeps{
		Bridge: func() browserpkg.ExtensionBridge {
			return setupProfileBridge(browserProfile(cfg), newExtensionBridgeLogger(), 3*time.Second)
		},
		EnsureConnected: func(b browserpkg.ExtensionBridge, timeout time.Duration) error {
			return ensureExtensionConnected(b, timeout)
		},
		Advice:             bridgeAdvice,
		LifetimeHint:       func(b browserpkg.ExtensionBridge) string { return bridgeLifetimeHint(b) },
		CheckSummaryChoice: checkSummaryChoice,
	}
	d.OrgRoot = func(project string) (string, func()) {
		env := &orgEnv{cfg: cfg, projectFlag: project}
		return env.Root(), env.Close
	}
	return d
}

// newCaptureCmd returns the `capture` command group: save a page from the
// user's real, logged-in Chrome into the monomind inbox
// (~/.monomind/inbox/<timestamp>-<slug>/), and list what is already there.
//
// The capture itself happens in the browser — the extension holds the
// `debugger` permission, so an MHTML snapshot and a print-to-PDF of a page
// the user is signed into need no new permission and no second, cookie-less
// browser. This side sends the command, reassembles what comes back (large
// artifacts arrive in chunks), and writes the envelope. See
// docs/BROWSER_TRACK_PLAN.md and internal/capture.
func newCaptureCmd(cfg *globalConfig) *cobra.Command {
	return capturecli.NewCommand(captureDeps(cfg))
}

// defaultDBPath mirrors the --db-path flag default in root.go (see capturecli.DefaultDBPath).
const defaultDBPath = capturecli.DefaultDBPath

// The capture helpers the extension bridge, record, crawl and people commands share.
var (
	openProfileDB          = capturecli.OpenProfileDB
	extensionProfileSource = capturecli.ExtensionProfileSource
	newCaptureClassifier   = capturecli.NewClassifier
	captureHumanBytes      = capturecli.HumanBytes
	truncateCaptureCell    = capturecli.TruncateCell
	slicesContains         = capturecli.SlicesContains
)

// resolveCaptureProfile turns what the user typed after --profile into a profile and its inbox.
func resolveCaptureProfile(db *sql.DB, idOrName string) (capturecli.Profile, error) {
	return captureErrDeps.ResolveProfile(db, idOrName)
}
