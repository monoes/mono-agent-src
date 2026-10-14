package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/nodemgr"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/shellpath"
	"github.com/monoes/mono-agent/internal/storage"
)

// EnvDeps is what the real-machine environment takes from the CLI around it.
type EnvDeps struct {
	// DBPath is --db-path as given.
	DBPath func() string
	// ProfileID is --profile as given (an id or a name; "" for the active profile).
	ProfileID func() string
	// Version is this binary's version.
	Version func() string
	// LatestVersion is the tag of the latest release.
	LatestVersion func(ctx context.Context) (string, error)
	// MigrateDB opens the database --db-path names with --profile, applying its
	// migrations, and closes it (the CLI's initDB).
	MigrateDB func() error
	// InstallRuntime installs one coding-agent runtime by its `agent scan` id; the fix is the
	// consent, so vendor install scripts are allowed.
	InstallRuntime func(ctx context.Context, id string, progress func(string)) error
	// Hook, when set (tests only), adjusts every environment doctor builds — e.g. replaces
	// the hooks that start processes or use the network.
	Hook func(*health.Env)
}

// NewHealthEnv builds the real-machine environment for checks and fixes; projects narrows the
// monomind projects (--project). The database is opened only if it already exists and is never
// migrated here — checks must not change anything; the migrate fix does that.
func NewHealthEnv(d EnvDeps, projects []string) (*health.Env, func()) {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	dbPath := clicore.ExpandPath(d.DBPath())
	env := &health.Env{
		Home:          home,
		DataDir:       filepath.Join(home, ".monoagent"),
		DBPath:        dbPath,
		Version:       d.Version(),
		Executable:    exe,
		ProfileID:     d.ProfileID(),
		LoginPath:     shellpath.LoginPath,
		FreeBytes:     health.FreeBytes,
		LatestVersion: d.LatestVersion,
		Migrate: func(_ context.Context, progress func(string)) error {
			// The migration runner reports through the standard logger;
			// route it into the fix's progress stream for the duration.
			restore := captureStdLog(progress)
			defer restore()
			return d.MigrateDB()
		},
	}
	nm := nodemgr.New()
	env.SystemNode = nm.SystemNode
	env.ManagedNode = func() (string, string, bool) {
		v, ok := nm.Current()
		if !ok {
			return "", "", false
		}
		return v, nm.NodePath(v), true
	}
	env.UpdateNode = func(ctx context.Context, progress func(string)) error {
		v, err := nm.Install(ctx, "lts", progress)
		if err != nil {
			return err
		}
		return nm.Prune(v)
	}
	env.RemoveNode = func(_ context.Context, progress func(string)) error {
		progress("removing " + nm.Root + " and " + nm.NpmRoot)
		return nm.Remove("")
	}
	env.InstallNode = func(ctx context.Context, progress func(string)) error {
		if _, err := nm.Install(ctx, "lts", progress); err != nil {
			return err
		}
		// Later fixes in this same run (installing monomind with it) need
		// it on PATH now, not at the next start.
		nodemgr.Activate(ctx)
		return nil
	}
	var hsOnce sync.Once
	var hsInfo *monomind.VersionInfo
	var hsErr error
	env.FindMonomind = monomind.Find
	env.MonomindCandidates = monomind.FindAll
	env.MonomindHandshake = func(ctx context.Context) (*monomind.VersionInfo, error) {
		hsOnce.Do(func() { _, hsInfo, hsErr = monomind.Ensure(ctx) })
		return hsInfo, hsErr
	}
	env.ScanRuntimes = monomind.Scan
	env.InstallMonomind = func(ctx context.Context, progress func(string)) error {
		return installMonomind(ctx, func(ctx context.Context, progress func(string)) (string, error) {
			plan, err := nm.InstallGlobal(ctx, progress, health.MonomindPackage)
			if err != nil {
				return "", err
			}
			return plan.BinDir, nil
		}, monomind.Find, monomind.Handshake, progress)
	}
	env.MonomindDoctor = func(ctx context.Context, o monomind.DoctorOptions) (*monomind.DoctorReport, error) {
		bin, err := monomind.Find()
		if err != nil {
			return nil, err
		}
		return monomind.Doctor(ctx, bin, o)
	}
	env.MonomindProjects = func() []string { return profileProjects(env, projects) }
	env.InitMonomindProfile = func(ctx context.Context, root string, progress func(string)) error {
		return monomind.InitProfile(ctx, monomind.InitOptions{Root: root, Progress: progress})
	}
	env.RepinMonomindProfile = func(ctx context.Context, root string, progress func(string)) error {
		return monomind.InitProfile(ctx, monomind.InitOptions{Root: root, Repin: true, Progress: progress})
	}
	env.InstallMonomindDeps = func(ctx context.Context, progress func(string)) error {
		bin, err := monomind.Find()
		if err != nil {
			return err
		}
		return monomind.DepsInstall(ctx, bin, progress)
	}
	env.InstallRuntime = func(ctx context.Context, id string, progress func(string)) error {
		// The fix itself is the consent (it is a confirm fix), so vendor
		// scripts are allowed here.
		return d.InstallRuntime(ctx, id, progress)
	}
	addServiceHooks(env, d)
	addMonoesAccountHook(env)
	env.ProfileRoot = func(id string) string { return profiledir.Root(env.DB, id) }
	env.EnsureProfile = func(id string) error { return profiledir.EnsureLayout(env.DB, id) }

	closeFn := func() {}
	if _, err := os.Stat(dbPath); err == nil {
		db, err := storage.NewDatabase(dbPath)
		if err != nil {
			env.DBErr = err
		} else {
			env.DB = db.DB
			env.PendingMigrations = db.PendingMigrations
			env.QuickCheck = db.QuickCheck
			addDBSchemaHook(env, db)
			env.VaultState = func(ctx context.Context, id string) (string, error) {
				st, err := secrets.CheckVault(ctx, db.DB, id)
				return string(st), err
			}
			closeFn = func() { db.Close() }
			if env.ProfileID == "" {
				var id string
				_ = db.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, profiledir.ActiveProfileSetting).Scan(&id)
				env.ProfileID = id
			} else if id, ok := resolveProfileID(db.DB, env.ProfileID); ok {
				// --profile takes a name or an id, as it does for every other
				// command; one that matches neither is left for the profile
				// check to report.
				env.ProfileID = id
			}
		}
	} else if !os.IsNotExist(err) {
		env.DBErr = err
	}
	if env.ProfileID == "" {
		env.ProfileID = "default"
	}
	if env.DB != nil {
		addAccountHooks(env, env.DB)
	}
	addAutomationHooks(env)
	// Jev: the DB may be unmigrated or absent; a vault miss just means the
	// key comes from TYPESAFE_API_KEY or nowhere.
	// KeySource only lists vault entry names: doctor never decrypts (no
	// keyring/passphrase prompt just to report where the key lives).
	env.JevKey = func(ctx context.Context) (string, error) {
		return jevconf.KeySource(ctx, env.DB, env.ProfileID)
	}
	env.JevModels = func(ctx context.Context) error {
		key, _, err := jevconf.ResolveKey(ctx, env.DB, env.ProfileID, "")
		if err != nil {
			return err
		}
		c, err := jev.NewClient(key, "")
		if err != nil {
			return err
		}
		_, err = c.Models(ctx)
		return err
	}
	if d.Hook != nil {
		d.Hook(env)
	}
	return env, closeFn
}

// resolveProfileID is the id of the profile idOrName names, by id or else by name (the CLI's
// resolveProfileID).
func resolveProfileID(db *sql.DB, idOrName string) (string, bool) {
	var id string
	if err := db.QueryRow(`SELECT id FROM profiles WHERE id = ?`, idOrName).Scan(&id); err == nil {
		return id, true
	}
	if err := db.QueryRow(`SELECT id FROM profiles WHERE LOWER(name) = LOWER(?)`, idOrName).Scan(&id); err == nil {
		return id, true
	}
	return "", false
}

// installMonomind runs install (npm install -g, returning the folder the
// executable went to), then checks that the monomind Find now picks is
// the new one and answers. An older monomind earlier on PATH (a root-owned
// /usr/bin/monomind) would otherwise still be used, and the check would
// offer this same fix again on every run: that is reported, naming the
// file to remove.
func installMonomind(ctx context.Context, install func(context.Context, func(string)) (string, error),
	find func() (string, error), handshake func(context.Context, string) (*monomind.VersionInfo, error),
	progress func(string)) error {
	var output []string
	binDir, err := install(ctx, func(line string) {
		output = append(output, line)
		progress(line)
	})
	if err != nil {
		return health.ExplainNpmClash(err, output, health.MonomindPackage)
	}
	monomind.ResetCapabilityCache()
	name := "monomind"
	if runtime.GOOS == "windows" {
		name = "monomind.cmd"
	}
	installed := filepath.Join(binDir, name)
	bin, err := find()
	if err != nil {
		return fmt.Errorf("installed into %s, but monomind is still not found: %w", binDir, err)
	}
	vi, herr := handshake(ctx, bin)
	if _, statErr := os.Stat(installed); statErr == nil && !samePath(bin, installed) {
		if herr != nil {
			return fmt.Errorf("installed monomind at %s, but %s comes first on PATH and is still used, and it does not work (%v) — "+
				"remove it (e.g. `sudo npm uninstall -g @monoes/monomindcli`, or delete it), or set %s=%s",
				installed, bin, herr, monomind.EnvOverride, installed)
		}
		progress(fmt.Sprintf("note: %s (monomind %s) comes first on PATH and is the one used; the copy just installed at %s is not",
			bin, vi.Version, installed))
		return nil
	}
	if herr != nil {
		return fmt.Errorf("installed, but monomind still does not answer: %w", herr)
	}
	progress(fmt.Sprintf("monomind %s ready at %s", vi.Version, bin))
	return nil
}

// samePath compares two files after resolving symlinks.
func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// profileProjects lists the monomind projects inside the active profile's
// folder, relative to it, narrowed to filter (relative paths or folder
// names) when given.
func profileProjects(env *health.Env, filter []string) []string {
	root := env.ProfileRoot(env.ProfileID)
	if root == "" {
		return nil
	}
	var out []string
	for _, abs := range monomind.ProjectsUnder(root) {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if len(filter) == 0 || matchesProject(rel, filter) {
			out = append(out, rel)
		}
	}
	return out
}

func matchesProject(rel string, filter []string) bool {
	for _, f := range filter {
		f = filepath.Clean(filepath.FromSlash(f))
		if f == rel || f == filepath.Base(rel) {
			return true
		}
	}
	return false
}
