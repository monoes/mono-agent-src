package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/capturedocs"
	"github.com/monoes/mono-agent/internal/docscan"
	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	_ "modernc.org/sqlite"
)

// App holds application state bound to the Wails runtime.
type App struct {
	ctx     context.Context
	db      *sql.DB
	dbPath  string
	logs    []LogEntry
	logsMu  sync.Mutex
	chatSup *chatSupervisor // conversation/turn/event supervisor; see app_chat.go

	runningMu      sync.Mutex
	runningCmds    map[string]*exec.Cmd // workflowID / "action:<id>" / "noderun:<id>" → running subprocess
	nodeRunCounter atomic.Int64         // source of RunNode run ids (NodeRunResult.run_id)
	orgEventIDs    map[string]string    // "orgevents:<org>" key → id of the stream running under it; see StreamOrgEvents
	orgEventStops  map[string]time.Time // stream id → when a stop for it arrived before it was registered

	selectedProfilePtr atomic.Pointer[string] // the profile the window shows (profile_selection.go); read/written across Wails goroutines
	ready              atomic.Bool            // set once the signed-in setup has finished; see IsReady

	orgWatchMu sync.Mutex
	orgWatcher *orgdesign.Watcher // polls the active profile's .monomind/orgs/ dir; see restartOrgWatcher
	// orgSignSupport caches whether monomind requires signed orgs (#288).
	orgSignSupport orgSignSupport

	docWatchMu   sync.Mutex
	pickedDocsMu sync.Mutex
	pickedDocs   map[string]bool      // paths a picker handed out; the next batch may upload only these
	docBatchBusy atomic.Bool          // an UploadProfileDocuments batch is running
	docWatcher   *docscan.Watcher     // polls the active profile's whole folder (minus .monomind/) for document changes; see restartDocumentWatcher
	capWatcher   *capturedocs.Watcher // polls the active profile's browser-capture inbox; see restartDocumentWatcher

	imgWatchMu sync.Mutex
	imgWatcher *imagescan.Watcher // polls the active profile's whole folder for images; see restartImageWatcher

	taskWatchMu      sync.Mutex
	taskWatchStop    func() // stops the active profile's task board watcher; see restartTaskWatcher
	taskWatchProfile string // the profile that watcher watches

	dbMigratePending atomic.Bool // `db migrate` was refused for sign-in; AccountStatus retries it (app_db_prepare.go)

	work signedInWork // what runs only while the session allows work; see app_account_gate.go
}

// NewApp creates the App instance.
func NewApp() *App {
	home, _ := os.UserHomeDir()
	return &App{
		dbPath:      filepath.Join(home, ".monoagent", "monoagent.db"),
		logs:        make([]LogEntry, 0, 200),
		runningCmds: make(map[string]*exec.Cmd),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// The process guard for any layer-2 call this process makes; see app_account_guard.go.
	a.startAccountGuard()
	// A first launch on a new machine has no ~/.monoagent yet; SQLite can't
	// create the database file inside a folder that doesn't exist (the CLI's
	// initDB makes it the same way).
	if err := os.MkdirAll(filepath.Dir(a.dbPath), 0o700); err != nil {
		runtime.LogErrorf(ctx, "data folder error: %v", err)
	}
	db, err := openReadOnlyDB(a.dbPath)
	if err != nil {
		runtime.LogErrorf(ctx, "DB open error: %v", err)
		return
	}
	a.db = db

	// Everything else waits for a session that allows work, and stops when
	// it no longer does (app_account_gate.go).
	a.syncSignedIn()
	go a.watchSession(ctx)

	// The sign-in screen offers the app update, so the check runs signed out.
	go a.backgroundUpdateCheck()
}

// IsReady reports whether the signed-in setup (startSignedIn) has finished
// (DB migrations, selected-profile resolution, node registry, etc.). Wails
// gives no guarantee the frontend won't become interactive before this
// finishes — a page that queries profile-scoped data (e.g. Orgs) landing
// before it queries no profile (an error) until the
// user navigates away and back. Polled by the frontend instead of guessing
// with a fixed delay.
func (a *App) IsReady() bool {
	if a.signedOut() != nil {
		return false
	}
	return a.ready.Load()
}

func (a *App) shutdown(_ context.Context) {
	a.stopAccountGuard()
	if a.chatSup != nil {
		a.chatSup.stopAll()
	}

	a.stopWatchers()

	a.stopRunningCmds()
	if a.db != nil {
		_ = a.db.Close()
	}
}

// stopRunningCmds ends every tracked subprocess on shutdown. A monoagentcli
// in its own process group (setChatProcessGroup: org event tails, org runs,
// agent validation) gets the graceful group kill and up to chatKillGrace to
// exit, after which that kill's timer SIGKILLs it: a SIGKILLed CLI can't
// cancel, so the monomind children it started would outlive the app
// (monoes/mono-agent#235). The rest (workflow and node runs) are killed as
// before, as is everything on Windows, where no group is tracked: there the
// CLI's Job Object takes its monomind children down with it.
func (a *App) stopRunningCmds() {
	var reaped []chan struct{}
	a.runningMu.Lock()
	for _, cmd := range a.runningCmds {
		if r := reapedChan(cmd); r != nil {
			killChatProcessGroup(cmd)
			reaped = append(reaped, r)
		} else if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	a.runningMu.Unlock()
	deadline := time.After(chatKillGrace + shutdownReapMargin)
	for _, r := range reaped {
		select {
		case <-r:
		case <-deadline:
			return
		}
	}
}

// shutdownReapMargin is how long past chatKillGrace shutdown waits for the
// SIGKILLed stragglers to be reaped.
const shutdownReapMargin = 2 * time.Second

// orgsDirForActiveProfile resolves the directory the org design watcher
// should poll for the selected profile; "" when none is selected or its
// folder cannot be resolved.
func (a *App) orgsDirForActiveProfile() string {
	root := a.orgProjectRoot()
	if root == "" {
		return ""
	}
	return orgdesign.OrgsDir(root)
}

// restartOrgWatcher stops any existing org design watcher and starts a new
// one scoped to the active profile's orgs directory — the mechanism by
// which AI-driven and externally-made org edits (both happen in a separate
// OS process from this app; see internal/ai/chat/monoagent_tools.go) reach
// the live "org:designUpdated" event, since no direct runtime.EventsEmit
// call is possible from outside this process.
func (a *App) restartOrgWatcher() {
	a.orgWatchMu.Lock()
	defer a.orgWatchMu.Unlock()

	if a.orgWatcher != nil {
		a.orgWatcher.Stop()
		a.orgWatcher = nil
	}

	dir := a.orgsDirForActiveProfile()
	if dir == "" {
		return
	}
	w := orgdesign.NewWatcher(dir, 0, func(c orgdesign.Change) {
		a.emitOrgDesignUpdated(c.Name, "external", c.Deleted, c.Doc, len(c.Errors) == 0, c.Errors)
	})
	w.Start()
	a.orgWatcher = w
}

// newUUID generates a random UUID v4 without external dependencies.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (a *App) emitLog(source, level, message string) {
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Source:  source,
		Level:   level,
		Message: message,
	}
	a.logsMu.Lock()
	a.logs = append(a.logs, entry)
	if len(a.logs) > 500 {
		a.logs = a.logs[len(a.logs)-500:]
	}
	a.logsMu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "log:entry", entry)
	}
}

// OpenURL opens a URL in the system default browser.
//
// Signed out it opens only a page of the monoes.me host: the sign-in
// screen's fallback link to the sign-in page.
func (a *App) OpenURL(url string) {
	if a.signedOut() != nil && !signInURL(url) {
		return
	}
	runtime.BrowserOpenURL(a.ctx, url)
}

// ─────────────────────────────────────────────────────────────────────────────
// Data export
// ─────────────────────────────────────────────────────────────────────────────

// ExportResult summarizes a completed export.
type ExportResult struct {
	OutputDir   string `json:"output_dir"`
	PeopleCount int    `json:"people_count"`
	Cancelled   bool   `json:"cancelled,omitempty"`
}

// ExportData asks the user for a destination folder and exports all people
// to JSON files there by invoking the CLI (`monoagentcli export`).
func (a *App) ExportData() (*ExportResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return nil, err
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose export folder"})
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return &ExportResult{Cancelled: true}, nil
	}
	full, err := a.scopedArgs("export", "--output-dir", dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(a.ctx, cliBin, full...)
	suppressConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && cliStderr(ee.Stderr) != "" {
			return nil, fmt.Errorf("export failed: %s", cliStderr(ee.Stderr))
		}
		return nil, fmt.Errorf("export failed: %w", err)
	}
	var res ExportResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("unexpected export output: %w", err)
	}
	a.emitLog("EXPORT", "INFO", fmt.Sprintf("Exported %d people to %s", res.PeopleCount, res.OutputDir))
	return &res, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Logs
// ─────────────────────────────────────────────────────────────────────────────

type LogEntry struct {
	Time    string `json:"time"`
	Source  string `json:"source"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (a *App) GetLogs() []LogEntry {
	if a.signedOut() != nil {
		return nil
	}
	a.logsMu.Lock()
	defer a.logsMu.Unlock()
	out := make([]LogEntry, len(a.logs))
	copy(out, a.logs)
	return out
}

func (a *App) ClearLogs() {
	if a.signedOut() != nil {
		return
	}
	a.logsMu.Lock()
	a.logs = make([]LogEntry, 0, 200)
	a.logsMu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetDBPath() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.dbPath
}

func (a *App) IsDBConnected() bool {
	if a.signedOut() != nil {
		return false
	}
	if a.db == nil {
		return false
	}
	return a.db.Ping() == nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Profiles (the bindings live in app_profiles.go)
// ─────────────────────────────────────────────────────────────────────────────

// bootstrapProfileMonograph seeds an empty monograph database at a fresh
// profile's .monomind/ project root, so it exists (even if empty) before the
// first chat turn or knowledge-graph write ever asks for it. Best-effort and
// fire-and-forget — a failure here never blocks profile creation, since
// monograph_build/kgIngest will lazily create what they need on first real
// use regardless.
func (a *App) bootstrapProfileMonograph(profileID string) {
	db := a.db
	go func() {
		bin, err := monomind.Find()
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := monomind.CommandContext(ctx, bin, "monograph", "build", "--path", profiledir.MonomindDir(db, profileID))
		suppressConsole(cmd)
		if err := cmd.Run(); err != nil {
			a.emitLog("SYSTEM", "WARN", fmt.Sprintf("profile %s: monograph bootstrap: %v", profileID, err))
		}
	}()
}
