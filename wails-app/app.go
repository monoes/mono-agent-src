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
	"github.com/monoes/mono-agent/internal/nodes"
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

	activeProfileIDPtr atomic.Pointer[string] // currently selected profile; access via get/setActiveProfileID (read/written across Wails goroutines)
	ready              atomic.Bool            // set once startup()'s synchronous setup has finished; see IsReady

	orgWatchMu sync.Mutex
	orgWatcher *orgdesign.Watcher // polls the active profile's .monomind/orgs/ dir; see restartOrgWatcher
	// orgSignSupport caches whether monomind requires signed orgs (#288).
	orgSignSupport orgSignSupport

	docWatchMu sync.Mutex
	docWatcher *docscan.Watcher     // polls the active profile's whole folder (minus .monomind/) for document changes; see restartDocumentWatcher
	capWatcher *capturedocs.Watcher // polls the active profile's browser-capture inbox; see restartDocumentWatcher

	imgWatchMu sync.Mutex
	imgWatcher *imagescan.Watcher // polls the active profile's whole folder for images; see restartImageWatcher

	taskWatchMu      sync.Mutex
	taskWatchStop    func() // stops the active profile's task board watcher; see restartTaskWatcher
	taskWatchProfile string // the profile that watcher watches

	dbMigratePending atomic.Bool // `db migrate` was refused for sign-in; AccountStatus retries it (app_db_prepare.go)
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

// getActiveProfileID returns the currently selected profile id. Wails dispatches
// bound methods on independent goroutines, so this is read concurrently with
// SwitchProfile writes — the atomic makes that access race-free. Defaults to
// "default" before startup sets it.
func (a *App) getActiveProfileID() string {
	if p := a.activeProfileIDPtr.Load(); p != nil {
		return *p
	}
	return "default"
}

// setActiveProfileID atomically updates the selected profile id.
func (a *App) setActiveProfileID(id string) {
	a.activeProfileIDPtr.Store(&id)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// The process guard for any layer-2 call this process makes; see app_account_guard.go.
	a.startAccountGuard()
	// Installed automation packages, before anything reads actions or forms.
	if _, err := nodes.BootAutomations(""); err != nil {
		runtime.LogWarningf(ctx, "automations: %v (using the built-in action set)", err)
	}
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
	// The CLI owns the schema: `db migrate` applies pending migrations and
	// the per-profile folder and vault migrations (app_db_prepare.go); this
	// read-only handle only serves what the app still reads itself.
	a.prepareDatabase()

	// os.UserHomeDir (not $HOME) so vault/workflow dirs resolve on Windows too.
	home, _ := os.UserHomeDir()

	// Ensure vault directory exists.
	vaultDir := filepath.Join(home, ".monoagent", "vault")
	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		runtime.LogErrorf(ctx, "vault dir error: %v", err)
	}

	// The chat supervisor reaches chat history only through the CLI
	// (`chat history …`), which creates the chat tables itself.
	a.initChatSupervisor()

	// Load the active profile from settings; default to 'default' if not set.
	var activeProfileID string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&activeProfileID)
	if activeProfileID == "" {
		activeProfileID = "default"
	}
	a.setActiveProfileID(activeProfileID)

	a.restartOrgWatcher()
	a.restartDocumentWatcher()
	a.restartImageWatcher()
	a.restartTaskWatcher()

	a.emitLog("SYSTEM", "INFO", "Mono Agent UI connected to "+a.dbPath)

	// Marks synchronous startup as finished — see IsReady. Must be the last
	// line: everything above (profile resolution in particular) needs to
	// have already happened before any page can trust IsReady()==true.
	a.ready.Store(true)

	go a.backgroundUpdateCheck()
}

// IsReady reports whether startup() has finished its synchronous setup
// (DB migrations, active-profile resolution, node registry, etc.). Wails
// gives no guarantee the frontend won't become interactive before this
// finishes — a page that queries profile-scoped data (e.g. Orgs) landing
// before it does silently reads the wrong ("default") profile until the
// user navigates away and back. Polled by the frontend instead of guessing
// with a fixed delay.
func (a *App) IsReady() bool {
	return a.ready.Load()
}

func (a *App) shutdown(_ context.Context) {
	a.stopAccountGuard()
	if a.chatSup != nil {
		a.chatSup.stopAll()
	}

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
// should poll for the currently active profile. Mirrors orgProjectRoot's
// (app_orgs.go) own fallback exactly: when the active profile has no
// resolvable root, `monoagentcli org` (called without --project) resolves
// the active profile's folder itself, which for an unset profile is the
// "default" profile's folder — the watcher must watch that same directory
// or it silently watches nothing for a profile whose layout failed to
// initialize.
func (a *App) orgsDirForActiveProfile() string {
	root := a.orgProjectRoot()
	if root == "" {
		root = profiledir.Root(a.db, "default")
	}
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
func (a *App) OpenURL(url string) {
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
	cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "--json", "export", "--output-dir", dir)
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
	a.logsMu.Lock()
	defer a.logsMu.Unlock()
	out := make([]LogEntry, len(a.logs))
	copy(out, a.logs)
	return out
}

func (a *App) ClearLogs() {
	a.logsMu.Lock()
	a.logs = make([]LogEntry, 0, 200)
	a.logsMu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetDBPath() string {
	return a.dbPath
}

func (a *App) IsDBConnected() bool {
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
