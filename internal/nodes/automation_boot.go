package nodes

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/bot"
	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/logging"
)

var (
	bootMu     sync.Mutex
	bootedHome string
	bootedReg  *automation.Registry

	// defaultBootTried is set once BootAutomations("") has run, whatever
	// the outcome, so a failure is reported by its first caller only.
	defaultBootTried bool
)

// BootAutomations opens the automation registry under home (normally
// ~/.monoagent; "" means automation.Default), folds legacy
// ~/.monoagent/actions directories into local-<p> packages and switches the
// action loader to the registry. Nothing is seeded: official packages are
// installed from monoes.me. It is idempotent per home. On error the loader
// is left as it was (~/.monoagent/actions only), so callers log the error
// and carry on.
func BootAutomations(home string) (*automation.Registry, error) {
	action.SetGlobalHostDeny(socialHostDeny)
	bootMu.Lock()
	defer bootMu.Unlock()
	if home == "" {
		defaultBootTried = true
	}
	if bootedReg != nil && bootedHome == home {
		return bootedReg, nil
	}

	var reg *automation.Registry
	var err error
	if home == "" {
		reg, err = automation.Default()
	} else {
		reg, err = automation.Open(home)
	}
	if err != nil {
		return nil, fmt.Errorf("open automation registry: %w", err)
	}
	if reg == nil {
		return nil, fmt.Errorf("open automation registry: no registry")
	}
	if _, err := reg.Boot(); err != nil {
		return nil, fmt.Errorf("fold legacy actions: %w", err)
	}
	src := reg.DefSource()
	if src == nil {
		return nil, fmt.Errorf("automation registry has no definition source")
	}

	action.SetDefSource(src)
	browser.SetStartURLResolver(packageStartURL)
	bootedHome, bootedReg = home, reg
	return reg, nil
}

// ensureAutomationsBooted boots the default registry once per process. Every
// node registry is built through RegisterBrowserNodes, so this covers the
// CLI, the daemon, MCP, the HTTP API and the desktop app without each
// startup site having to remember it. Test binaries never touch the real
// home: they call BootAutomations with a temp dir explicitly.
func ensureAutomationsBooted() {
	if testing.Testing() {
		return
	}
	bootMu.Lock()
	tried := defaultBootTried
	bootMu.Unlock()
	if tried {
		return // the CLI root (or an earlier call) already booted, or warned
	}
	if _, err := BootAutomations(""); err != nil {
		l := logging.Zerolog()
		l.Warn().Err(err).Msg("automations: registry unavailable, using ~/.monoagent/actions only")
	}
}

// socialHostDeny is the global host rule (contract §8): a social site whose
// platform isn't compiled into this binary is off limits to every action,
// with or without a package.
func socialHostDeny(host string) (bool, string) {
	platform, ok := automation.SocialPlatformForHost(host)
	if !ok || bot.PlatformCompiledIn(platform) {
		return false, ""
	}
	return true, fmt.Sprintf("%s is a %s site and this build has no social support (built with -tags nosocial)", host, platform)
}

// bootedManifest returns the installed manifest for an automation, when the
// registry was booted and has it.
func bootedManifest(id string) (*automation.Manifest, bool) {
	bootMu.Lock()
	reg := bootedReg
	bootMu.Unlock()
	if reg == nil {
		return nil, false
	}
	p, err := reg.Get(id)
	if err != nil || p == nil {
		return nil, false
	}
	return &p.Manifest, true
}

// nativePlatform is the compiled Go bot an automation's actions call into:
// manifest requires.native, else the automation id itself (legacy platforms,
// where the two are the same).
func nativePlatform(id string) string {
	if m, ok := bootedManifest(id); ok && m.Requires.Native != "" {
		return m.Requires.Native
	}
	return id
}

// packageStartURL resolves manifest site.startUrl through the installed
// definition source ("" when unknown).
func packageStartURL(platform string) string {
	src := action.CurrentDefSource()
	if src == nil {
		return ""
	}
	pkg := src.Package(strings.ToLower(platform))
	if pkg == nil {
		return ""
	}
	return pkg.StartURL()
}

var (
	healthMu  sync.Mutex
	healthObs = map[*sql.DB]action.SelectorObserver{}
)

// healthObserver returns the process-wide selector-health observer for db,
// creating it on first use. The observer batches writes on its own
// goroutine, so it must be shared, not built per run.
func healthObserver(db *sql.DB) action.SelectorObserver {
	healthMu.Lock()
	defer healthMu.Unlock()
	if obs, ok := healthObs[db]; ok {
		return obs
	}
	obs := automation.HealthObserver(db)
	if obs == nil {
		return nil
	}
	healthObs[db] = obs
	return obs
}

// flushHealth writes the observer's pending observations for db now, so a
// short CLI run doesn't exit before the batch timer fires.
func flushHealth(db *sql.DB) {
	healthMu.Lock()
	obs := healthObs[db]
	healthMu.Unlock()
	if f, ok := obs.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

// CloseHealthObservers flushes and stops every selector-health observer.
// Call it on process shutdown, before the database is closed.
func CloseHealthObservers() {
	healthMu.Lock()
	all := healthObs
	healthObs = map[*sql.DB]action.SelectorObserver{}
	healthMu.Unlock()
	for _, obs := range all {
		if c, ok := obs.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}
}

// attachPackage hands the executor the package its action belongs to, the
// selector-health observer and the manifest's download permission. Without a
// package (legacy loader) the executor keeps its legacy behaviour.
func attachPackage(ex *action.ActionExecutor, platform string, db *sql.DB) {
	src := action.CurrentDefSource()
	if src == nil {
		return
	}
	pkg := src.Package(strings.ToLower(platform))
	if pkg == nil {
		return
	}
	ex.SetPackage(pkg)
	if db != nil {
		if obs := healthObserver(db); obs != nil {
			ex.SetSelectorObserver(obs)
		}
	}
	m, found := bootedManifest(pkg.ID())
	ex.SetDownloadsAllowed(found && m.Permissions.Downloads)
}
