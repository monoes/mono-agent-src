// wails-app/app_account_gate.go
//
// The app's half of "nothing works before sign-in". The frontend shows only the
// sign-in screen while the machine's monoes.me sign-in does not allow work;
// this file makes the Go side refuse too, so a binding called from the devtools
// or by a page that is still mounted does nothing:
//
//   - every bound method begins with a.signedOut() and returns the refusal when
//     it fails (app_account_gate_test.go walks them all; the ones the sign-in
//     screen needs are listed there with their reason);
//   - the work this process runs on its own (the profile watchers, the chat
//     supervisor, the database preparation) starts only once the session allows
//     work, and the watchers stop when it no longer does (watchSession).
//
// The verdict is the installed guard's (app_account_guard.go), the one
// account.Require gives every gate of the program.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// gateRefusals counts the refusals signedOut gave; the binding walk reads it.
var gateRefusals atomic.Int64

// signedOut returns nil when the machine's sign-in allows work, else the
// *account.LoginRequiredError every gate returns.
func (a *App) signedOut() error {
	if err := account.Require(context.Background()); err != nil {
		gateRefusals.Add(1)
		return err
	}
	return nil
}

// loginRequiredJSON is a refusal in the shape of the JSON-string bindings:
// {"error": …, "login_required": true, "code", "account"}, the document the
// CLI prints for it.
func loginRequiredJSON(err error) string {
	out := map[string]any{"error": err.Error(), "login_required": true}
	if e, ok := err.(*account.LoginRequiredError); ok {
		for k, v := range e.JSONErrorFields() {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// signInURL reports whether raw is a page of the monoes.me host, the only
// thing OpenURL opens signed out (the sign-in screen's fallback link).
func signInURL(raw string) bool {
	u, err := url.Parse(raw)
	h, herr := url.Parse(account.Host())
	return err == nil && herr == nil && u.Scheme == h.Scheme && strings.EqualFold(u.Host, h.Host)
}

// sessionWatchInterval is how often watchSession asks the guard; the guard
// itself re-reads session.json every account.PollInterval.
var sessionWatchInterval = account.PollInterval

// signedInWork is what runs only while the session allows work.
type signedInWork struct {
	mu          sync.Mutex
	running     bool
	boot        sync.Once
	start, stop func() // a test's stand-ins for startSignedIn and stopSignedIn
}

// syncSignedIn starts the signed-in work when the session allows work and it
// is not running, and stops it when the session does not and it is. It
// reports whether the state changed.
func (a *App) syncSignedIn() bool {
	allowed := account.CurrentStatus().Allowed()
	a.work.mu.Lock()
	defer a.work.mu.Unlock()
	switch {
	case allowed && !a.work.running:
		a.work.running = true
		if a.work.start != nil {
			a.work.start()
		} else {
			a.startSignedIn()
		}
	case !allowed && a.work.running:
		a.work.running = false
		if a.work.stop != nil {
			a.work.stop()
		} else {
			a.stopSignedIn()
		}
	default:
		return false
	}
	return true
}

// followSession is AccountStatus's half: when the CLI's answer and this
// process's verdict differ (a sign-in or sign-out the guard has not polled
// yet), the guard re-reads the session, and the signed-in work follows the
// verdict. The CLI's answer only prompts the re-read; the verdict stays the
// guard's.
func (a *App) followSession(status string) {
	accountGuard.mu.Lock()
	started := accountGuard.g != nil
	accountGuard.mu.Unlock()
	if !started {
		return // before startup or after shutdown there is no work to follow the session
	}
	if statusAllowsWork(status) != account.CurrentStatus().Allowed() {
		a.startAccountGuard()
	}
	go a.syncSignedIn()
}

// watchSession keeps the signed-in work in step with the session until ctx
// ends, and tells the page ("account:changed") when it changed, so the
// sign-in screen comes up, or goes away, without waiting for a focus.
func (a *App) watchSession(ctx context.Context) {
	t := time.NewTicker(sessionWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a.syncSignedIn() {
				eventsEmit(ctx, "account:changed")
			}
		}
	}
}

// startSignedIn is what startup used to do before the page could ask anything:
// the automation packages, the database preparation, the chat supervisor, the
// active profile and its watchers. Called with a.work.mu held.
func (a *App) startSignedIn() {
	a.work.boot.Do(func() {
		// Installed automation packages, before anything reads actions or forms.
		if _, err := nodes.BootAutomations(""); err != nil {
			runtime.LogWarningf(a.ctx, "automations: %v (using the built-in action set)", err)
		}
	})
	if a.db == nil {
		return
	}
	// The CLI owns the schema: `db migrate` applies pending migrations and
	// the per-profile folder and vault migrations (app_db_prepare.go); this
	// read-only handle only serves what the app still reads itself.
	a.prepareDatabase()
	// os.UserHomeDir (not $HOME) so the vault dir resolves on Windows too.
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".monoagent", "vault"), 0o700); err != nil {
		runtime.LogErrorf(a.ctx, "vault dir error: %v", err)
	}
	// The chat supervisor reaches chat history only through the CLI
	// (`chat history …`), which creates the chat tables itself.
	if a.chatSup == nil {
		a.initChatSupervisor()
	}
	// The profile this window shows: the saved selection, else the oldest
	// profile. With no profile at all nothing is selected, bindings that
	// need one return errNoProfile, and the sidebar offers a new profile.
	if err := a.selectProfileAtStartup(); err != nil && !errors.Is(err, errNoProfile) {
		a.emitLog("SYSTEM", "WARN", "selecting a profile: "+err.Error())
	}
	a.startWatchers()
	a.emitLog("SYSTEM", "INFO", "Mono Agent UI connected to "+a.dbPath)
	// Marks the signed-in setup as finished — see IsReady. Must be the last
	// line: everything above (profile resolution in particular) needs to
	// have already happened before any page can trust IsReady()==true.
	a.ready.Store(true)
}

// stopSignedIn stops the watchers when the session ends. Running CLI
// children are left to the CLI, which judges the session itself.
func (a *App) stopSignedIn() {
	a.ready.Store(false)
	a.stopWatchers()
}

// startWatchers (re)starts the active profile's watchers, never signed out.
func (a *App) startWatchers() {
	if !account.CurrentStatus().Allowed() {
		return
	}
	a.restartOrgWatcher()
	a.restartDocumentWatcher()
	a.restartImageWatcher()
	a.restartTaskWatcher()
}

// stopWatchers stops every watcher (sign-out and shutdown).
func (a *App) stopWatchers() {
	a.orgWatchMu.Lock()
	if a.orgWatcher != nil {
		a.orgWatcher.Stop()
		a.orgWatcher = nil
	}
	a.orgWatchMu.Unlock()

	a.docWatchMu.Lock()
	if a.docWatcher != nil {
		a.docWatcher.Stop()
		a.docWatcher = nil
	}
	if a.capWatcher != nil {
		a.capWatcher.Stop()
		a.capWatcher = nil
	}
	a.docWatchMu.Unlock()

	a.imgWatchMu.Lock()
	if a.imgWatcher != nil {
		a.imgWatcher.Stop()
		a.imgWatcher = nil
	}
	a.imgWatchMu.Unlock()

	a.stopTaskWatcher()
}
