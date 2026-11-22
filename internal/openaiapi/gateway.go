package openaiapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/monomind"
)

// ErrScratchBusy is New's answer when another process already serves the
// OpenAI-compatible API from the same working folders. Two gateways over one
// home would hand the same slot folder to two turns of a profile, and each
// would empty the other's files.
var ErrScratchBusy = errors.New("another monoagentcli process is already serving the OpenAI-compatible API from this home (its working folders are in use)")

// ExecFunc runs one agent turn: monomind.Exec in production.
type ExecFunc func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)

// Deps are everything the gateway reaches outside itself, so tests can run it
// without a process, a network or a real monomind.
type Deps struct {
	Keys *apikeys.Store
	Exec ExecFunc
	// Bin finds the monomind binary. The gateway caches the answer.
	Bin     func(ctx context.Context) (string, error)
	Catalog CatalogFuncs
	// Knowledge searches a profile's own knowledge. nil disables context.
	Knowledge func(ctx context.Context, profileID, query string) ([]monomind.KnowledgeResult, error)
	// Auto lets Jev pick the model of a request for "auto". The zero value means
	// the model is never available.
	Auto    AutoFuncs
	Logf    func(format string, args ...any)
	Version string
}

// DefaultDeps wires the gateway to monomind and the stored roster.
func DefaultDeps(db *sql.DB, version string) Deps {
	return Deps{
		Keys: apikeys.NewStore(db),
		Exec: monomind.Exec,
		Bin: func(ctx context.Context) (string, error) {
			bin, _, err := monomind.Ensure(ctx)
			return bin, err
		},
		Catalog: CatalogFuncs{
			Scan:   monomind.Scan,
			Models: monomind.ListModelsStrict,
			Caps:   monomind.Capabilities,
			Roster: func(ctx context.Context) ([]agentroster.Result, error) { return agentroster.List(ctx, db) },
		},
		Knowledge: func(ctx context.Context, profileID, query string) ([]monomind.KnowledgeResult, error) {
			return monomind.SearchKnowledge(ctx, db, profileID, query)
		},
		Auto:    DefaultAuto(db),
		Logf:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, "api: "+format+"\n", args...) },
		Version: version,
	}
}

// Gateway serves the /v1 endpoints. Mount it on a mux once per listener,
// each time with that listener's Policy.
type Gateway struct {
	deps    Deps
	cfg     Config
	catalog *Catalog
	limiter *limiter
	bin     *binCache

	mu      sync.Mutex
	running int             // turns in flight
	idle    []chan struct{} // closed when running reaches zero

	// now is the clock of the auto model's breakers: time.Now, unless a test moves it.
	now       func() time.Time
	breakerMu sync.Mutex
	breakers  map[string]*autoBreaker // by profile

	// conts keeps where each leg of a conversation with tools that ended at a
	// call left its runtime session (tools_store.go).
	conts *contStore

	// jobs are the image requests answered with a job id (images_jobs.go).
	jobs *imageJobs

	// shutdownCtx ends when the server is stopping: every turn in flight
	// watches it, so closing a listener cannot leave an agent CLI running.
	shutdownCtx context.Context
	shutdown    context.CancelFunc

	// unlock releases the lock on the scratch root, once the turns are gone.
	unlock     func()
	unlockOnce sync.Once
}

// New builds a Gateway and empties the slot folders an earlier crash may have
// left files in. It holds an exclusive lock on the scratch root until Shutdown
// (or the process's end), so a second gateway over the same home gets
// ErrScratchBusy instead of emptying folders a running turn is working in.
func New(d Deps, c Config) (*Gateway, error) {
	cfg, err := c.withDefaults()
	if err != nil {
		return nil, err
	}
	switch {
	case d.Keys == nil:
		return nil, errors.New("openaiapi: Deps.Keys is required")
	case d.Exec == nil:
		return nil, errors.New("openaiapi: Deps.Exec is required")
	case d.Bin == nil:
		return nil, errors.New("openaiapi: Deps.Bin is required")
	case d.Catalog.Scan == nil || d.Catalog.Models == nil:
		return nil, errors.New("openaiapi: Deps.Catalog.Scan and Models are required")
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	if d.Catalog.Logf == nil {
		d.Catalog.Logf = d.Logf
	}
	unlock, err := daemonhb.LockFile(filepath.Join(cfg.ScratchRoot, ".lock"))
	if errors.Is(err, daemonhb.ErrHeld) {
		return nil, ErrScratchBusy
	}
	if err != nil {
		return nil, fmt.Errorf("openaiapi: locking the working folders: %w", err)
	}
	g := &Gateway{
		deps:    d,
		cfg:     cfg,
		catalog: NewCatalog(d.Catalog, cfg.CatalogTTL),
		limiter: newLimiter(cfg.MaxConcurrent),
		bin:     &binCache{f: d.Bin},
		unlock:  unlock,

		now:      time.Now,
		breakers: map[string]*autoBreaker{},
	}
	g.shutdownCtx, g.shutdown = context.WithCancel(context.Background())
	g.conts = newContStore(func() time.Time { return g.now() })
	g.jobs = newImageJobs(func() time.Time { return g.now() })
	g.jobs.stuckAfter = cfg.TurnTimeout + turnGrace + time.Minute
	g.cleanSlots() // only now: nothing of another process can be running in them
	_ = os.RemoveAll(filepath.Join(cfg.ScratchRoot, tmpDirName))
	if cfg.ImagesOff() {
		d.Logf("image generation is %s", imagesOffBy)
	}
	if cfg.ToolsOff() {
		d.Logf("tool calling is %s", toolsOffBy)
	}
	g.catalog.onFirstLoad = g.logImageRuntimes
	return g, nil
}

const (
	// slotPrefix names a turn's working folder: slot-0 up to the concurrency
	// limit, inside the folder of the profile the turn runs for.
	slotPrefix     = "slot-"
	scratchPurpose = "api"
	// profilePrefix starts the name of a profile's folder under the scratch root.
	profilePrefix = "p-"
	// tmpDirName holds the private folders of the turns' prompt files.
	tmpDirName = ".tmp"
	// turnTmpName is the temp folder a turn gets inside its own folder.
	turnTmpName = ".tmp"
	// quarantineDirName holds the slot folders that could not be emptied, set
	// aside for the operator to delete.
	quarantineDirName = ".quarantine"
)

// profileFolder names the folder that holds a profile's slot folders: a hash
// of its id, so an id, an arbitrary string, never reaches a path as such. A
// profile's turns keep their working folders, and so the per-folder session
// state of the agent CLIs, apart from every other profile's.
func profileFolder(profileID string) string {
	sum := sha256.Sum256([]byte(profileID))
	return profilePrefix + hex.EncodeToString(sum[:8])
}

// isProfileFolder reports whether name has the shape profileFolder gives.
func isProfileFolder(name string) bool {
	raw, ok := strings.CutPrefix(name, profilePrefix)
	if !ok || len(raw) != 16 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// cleanSlots empties every slot folder of every profile at start, since a
// crash may have left files in one, and removes the folders of slots above
// the current limit. Nothing runs yet, so no turn can be using them. Only
// slot-N folders inside a profile folder are touched.
func (g *Gateway) cleanSlots() {
	profiles, err := os.ReadDir(g.cfg.ScratchRoot)
	if err != nil {
		return
	}
	for _, p := range profiles {
		if !p.IsDir() || !isProfileFolder(p.Name()) {
			continue
		}
		profileDir := filepath.Join(g.cfg.ScratchRoot, p.Name())
		slots, err := os.ReadDir(profileDir)
		if err != nil {
			continue
		}
		for _, e := range slots {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), slotPrefix) {
				continue
			}
			n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), slotPrefix))
			if err != nil {
				continue
			}
			dir := filepath.Join(profileDir, e.Name())
			if n >= g.cfg.MaxConcurrent {
				if emptyDir(dir) {
					_ = os.Remove(dir)
				} else {
					_ = g.setAside(dir)
				}
				continue
			}
			if !emptyDir(dir) {
				_ = g.quarantine(dir)
			}
		}
	}
}

// setAside moves a folder that could not be emptied under the scratch root's
// quarantine folder, for the operator to delete when they like, and says so in
// the log. It is not retried on every request, and it cannot be walked (or is
// being changed under the gateway's hands): moving it is one rename.
func (g *Gateway) setAside(dir string) error {
	qdir := filepath.Join(g.cfg.ScratchRoot, quarantineDirName)
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(qdir, fmt.Sprintf("%s-%s-%d", filepath.Base(filepath.Dir(dir)), filepath.Base(dir), time.Now().UnixNano()))
	if err := os.Rename(dir, dest); err != nil {
		return err
	}
	g.deps.Logf("the working folder %s could not be emptied and was moved to %s: delete it when you no longer need it", dir, dest)
	return nil
}

// quarantine is setAside followed by an empty folder at the same path, since the
// path is what an agent CLI keys its session state on.
func (g *Gateway) quarantine(dir string) error {
	if err := g.setAside(dir); err != nil {
		return err
	}
	return os.Mkdir(dir, 0o700)
}

// turnStarted counts a turn in, and refuses once the gateway is shutting down:
// Shutdown has returned, or is about to, so a process started now would have
// nobody left to stop it. Shutdown cancels the context before it waits, and
// both sides hold the mutex, so a turn is either counted (and waited for) or
// refused.
func (g *Gateway) turnStarted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shutdownCtx.Err() != nil {
		return false
	}
	g.running++
	return true
}

// stopping reports whether the server is shutting down. A turn that ends
// cancelled while its caller is still there was cut short by that.
func (g *Gateway) stopping() bool { return g.shutdownCtx.Err() != nil }

func (g *Gateway) turnEnded() {
	g.mu.Lock()
	g.running--
	if g.running == 0 {
		for _, c := range g.idle {
			close(c)
		}
		g.idle = nil
	}
	g.mu.Unlock()
}

// Shutdown ends every turn in flight (each is cancelled and its process group
// killed) and waits up to timeout for them to be gone, reporting whether they
// are. A server calls it when it stops: returning without it would leave an
// agent CLI running with nobody left to stop it. The gateway serves nothing
// useful afterwards.
func (g *Gateway) Shutdown(timeout time.Duration) bool {
	g.shutdown()
	drained := g.Drain(timeout)
	if drained {
		// Another gateway may use the folders now. With turns still alive the
		// lock stays until the process ends.
		g.unlockOnce.Do(g.unlock)
	}
	return drained
}

// Drain waits up to timeout for the turns in flight to end, without ending
// them, and reports whether they did.
func (g *Gateway) Drain(timeout time.Duration) bool {
	g.mu.Lock()
	if g.running == 0 {
		g.mu.Unlock()
		return true
	}
	c := make(chan struct{})
	g.idle = append(g.idle, c)
	g.mu.Unlock()
	select {
	case <-c:
		return true
	case <-time.After(timeout):
		return false
	}
}

// binCache remembers where monomind is for a while: finding it walks the
// PATH ladder and re-probes the handshake.
type binCache struct {
	f  func(ctx context.Context) (string, error)
	mu sync.Mutex
	at time.Time
	v  string
}

const binTTL = 5 * time.Minute

func (b *binCache) get(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.v != "" && time.Since(b.at) < binTTL {
		return b.v, nil
	}
	v, err := b.f(ctx)
	if err != nil {
		return "", err
	}
	b.v, b.at = v, time.Now()
	return v, nil
}
