package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Group names.
const GroupCore = "core"

// Check IDs of the core group.
const (
	CheckCLI     = "core.cli"
	CheckHome    = "core.home"
	CheckDB      = "core.db"
	CheckProfile = "core.profile"
	CheckVault   = "core.vault"
	CheckPath    = "core.path"
	CheckDisk    = "core.disk"
	CheckUpdate  = "core.update"
)

// Fix IDs of the core group.
const (
	FixHomeCreate    = "core.home.create"
	FixDBMigrate     = "core.db.migrate"
	FixProfileLayout = "core.profile.layout"
	FixUpdate        = "core.update.install"
)

// minFreeBytes is the free-space floor under ~/.monoagent.
const minFreeBytes = 1 << 30

func coreChecks() []Check {
	return []Check{
		{ID: CheckCLI, Group: GroupCore, Title: "monoagentcli", Run: checkCLI},
		{ID: CheckHome, Group: GroupCore, Title: "Data folder", Required: true, Run: checkHome},
		{ID: CheckDB, Group: GroupCore, Title: "Database", Required: true, DependsOn: []string{CheckHome}, Run: checkDB},
		permsCheck(),
		{ID: CheckSandbox, Group: GroupCore, Title: "Agent sandbox", DependsOn: []string{CheckHome}, Run: checkSandbox},
		{ID: CheckAgentIsolation, Group: GroupCore, Title: "Agent isolation", Run: checkAgentIsolation},
		{ID: CheckProfile, Group: GroupCore, Title: "Profile", Required: true, DependsOn: []string{CheckDB}, Run: checkProfile},
		{ID: CheckVault, Group: GroupCore, Title: "Secrets vault", Features: []string{"credentials", "connections"}, DependsOn: []string{CheckProfile}, Run: checkVault},
		{ID: CheckPath, Group: GroupCore, Title: "Login shell PATH", Features: []string{"tools installed via nvm/Homebrew/mise"}, Run: checkPath},
		{ID: CheckDisk, Group: GroupCore, Title: "Disk space", DependsOn: []string{CheckHome}, Run: checkDisk},
		{ID: CheckUpdate, Group: GroupCore, Title: "Updates", Network: true, Timeout: 20 * time.Second, Run: checkUpdate},
	}
}

func coreFixes() []Fix {
	return []Fix{
		{FixInfo: FixInfo{ID: FixHomeCreate, Label: "Create data folder", Safety: SafetyAuto}, Apply: fixHomeCreate},
		{FixInfo: FixInfo{ID: FixDBMigrate, Label: "Create / migrate database", Safety: SafetyAuto}, Apply: fixDBMigrate},
		permsFix(),
		{FixInfo: FixInfo{ID: FixProfileLayout, Label: "Create profile folder", Safety: SafetyAuto}, Apply: fixProfileLayout},
		{FixInfo: FixInfo{ID: FixUpdate, Label: "Update monoagentcli", Safety: SafetyConfirm, Command: "monoagentcli update"}, Apply: fixUpdate},
	}
}

func checkCLI(_ context.Context, env *Env) Result {
	return Result{Status: StatusInfo, Summary: fmt.Sprintf("%s (%s)", env.Version, env.Executable)}
}

func checkHome(_ context.Context, env *Env) Result {
	info, err := os.Stat(env.DataDir)
	if os.IsNotExist(err) {
		return Result{Status: StatusFail, Summary: env.DataDir + " does not exist", FixID: FixHomeCreate}
	}
	if err != nil {
		return Result{Status: StatusFail, Summary: "cannot read " + env.DataDir, Detail: err.Error()}
	}
	if !info.IsDir() {
		return Result{Status: StatusFail, Summary: env.DataDir + " is not a folder"}
	}
	f, err := os.CreateTemp(env.DataDir, ".doctor-*")
	if err != nil {
		return Result{Status: StatusFail, Summary: env.DataDir + " is not writable", Detail: err.Error()}
	}
	f.Close()
	os.Remove(f.Name())
	return Result{Status: StatusOK, Summary: env.DataDir}
}

func fixHomeCreate(_ context.Context, env *Env, progress func(string)) error {
	progress("creating " + env.DataDir)
	return os.MkdirAll(env.DataDir, 0o700)
}

func checkDB(ctx context.Context, env *Env) Result {
	if env.DBErr != nil {
		return Result{Status: StatusFail, Summary: "cannot open " + env.DBPath, Detail: env.DBErr.Error()}
	}
	if env.DB == nil {
		return Result{Status: StatusFail, Summary: "no database yet at " + env.DBPath, FixID: FixDBMigrate}
	}
	if env.QuickCheck != nil {
		problem, err := env.QuickCheck(ctx)
		if err != nil {
			return Result{Status: StatusFail, Summary: "integrity check failed to run", Detail: err.Error()}
		}
		if problem != "" {
			return Result{Status: StatusFail, Summary: "database is damaged — restore from a backup", Detail: problem}
		}
	}
	if env.PendingMigrations == nil {
		return Result{Status: StatusOK, Summary: env.DBPath}
	}
	pending, err := env.PendingMigrations(ctx)
	if err != nil {
		return Result{Status: StatusFail, Summary: "cannot read schema version", Detail: err.Error()}
	}
	if len(pending) > 0 {
		return Result{Status: StatusWarn, Summary: fmt.Sprintf("%d schema migration(s) pending", len(pending)),
			Detail: strings.Join(pending, "\n"), FixID: FixDBMigrate}
	}
	return Result{Status: StatusOK, Summary: env.DBPath}
}

func fixDBMigrate(ctx context.Context, env *Env, progress func(string)) error {
	if env.Migrate == nil {
		return fmt.Errorf("migration is not available here")
	}
	progress("applying migrations to " + env.DBPath)
	return env.Migrate(ctx, progress)
}

// noProfile is the result of a per-profile check run without --profile: the
// machine checks still run, these wait for a profile to be named.
func noProfile() Result {
	return Result{Status: StatusSkip, Summary: "no profile given: pass --profile <id|name> to check one"}
}

// profileRoot is the folder of the profile being checked, "" when none was named.
func (env *Env) profileRoot() string {
	if env.ProfileID == "" || env.ProfileRoot == nil {
		return ""
	}
	return env.ProfileRoot(env.ProfileID)
}

func checkProfile(ctx context.Context, env *Env) Result {
	id := env.ProfileID
	if id == "" {
		return noProfile()
	}
	if env.DB != nil {
		var n int
		if err := env.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles`).Scan(&n); err == nil && n > 0 {
			var name string
			if err := env.DB.QueryRowContext(ctx, `SELECT COALESCE(name, id) FROM profiles WHERE id = ?`, id).Scan(&name); err != nil {
				return Result{Status: StatusFail, Summary: fmt.Sprintf("profile %q does not exist", id),
					Detail: "list profiles with: monoagentcli profile list"}
			}
			id = fmt.Sprintf("%s (%s)", name, id)
		}
	}
	if env.ProfileRoot == nil {
		return Result{Status: StatusOK, Summary: id}
	}
	root := env.profileRoot()
	if root == "" {
		return Result{Status: StatusFail, Summary: fmt.Sprintf("profile id %q is not usable as a folder name", env.ProfileID)}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return Result{Status: StatusWarn, Summary: id + " — folder missing: " + root, FixID: FixProfileLayout}
	}
	return Result{Status: StatusOK, Summary: id + " — " + root}
}

func fixProfileLayout(_ context.Context, env *Env, progress func(string)) error {
	if env.EnsureProfile == nil {
		return fmt.Errorf("profile setup is not available here")
	}
	if env.ProfileID == "" {
		return fmt.Errorf("pass --profile <id|name> to say which profile")
	}
	progress("creating profile folder for " + env.ProfileID)
	return env.EnsureProfile(env.ProfileID)
}

func checkVault(ctx context.Context, env *Env) Result {
	if env.VaultState == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	if env.ProfileID == "" {
		return noProfile()
	}
	state, err := env.VaultState(ctx, env.ProfileID)
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	switch state {
	case "ok":
		return Result{Status: StatusOK, Summary: "key in OS keychain unlocks the vault"}
	case "uninitialized":
		return Result{Status: StatusInfo, Summary: "no secrets stored yet — the key is created on first save"}
	case "file-keyring":
		return Result{Status: StatusWarn, Summary: "OS keychain unavailable; using the file keyring (MONOAGENT_ALLOW_FILE_KEYRING)"}
	case "keyring-unavailable":
		return Result{Status: StatusFail, Summary: "OS keychain is unavailable — secrets can't be stored or read", Detail: detail}
	case "key-missing":
		return Result{Status: StatusFail, Summary: "vault key is missing from the OS keychain — stored secrets are unreadable",
			Detail: detail + "\nrestore from a `monoagentcli secret export` backup"}
	case "key-mismatch":
		return Result{Status: StatusFail, Summary: "OS keychain holds a different key than the vault was sealed with",
			Detail: detail + "\nrestore from a `monoagentcli secret export` backup"}
	case "not-bound":
		return Result{Status: StatusFail, Summary: "the vault key was made for another install (a copied database?) — stored secrets are not opened here",
			Detail: detail + "\nif this database was moved on purpose, run `monoagentcli secret rebind`"}
	default:
		return Result{Status: StatusFail, Summary: "cannot check the vault", Detail: detail}
	}
}

func checkPath(ctx context.Context, env *Env) Result {
	if runtime.GOOS == "windows" {
		return Result{Status: StatusSkip, Summary: "not needed on Windows"}
	}
	if env.LoginPath == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	p, err := env.LoginPath(ctx)
	if err != nil || strings.TrimSpace(p) == "" {
		detail := "the app falls back to the PATH it was started with"
		if err != nil {
			detail = err.Error() + "\n" + detail
		}
		return Result{Status: StatusWarn, Summary: "could not read your login shell's PATH", Detail: detail}
	}
	n := len(filepath.SplitList(p))
	return Result{Status: StatusOK, Summary: fmt.Sprintf("%d entries from %s", n, loginShell())}
}

func loginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return filepath.Base(s)
	}
	return "sh"
}

func checkDisk(_ context.Context, env *Env) Result {
	if env.FreeBytes == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	free, err := env.FreeBytes(env.DataDir)
	if err != nil {
		return Result{Status: StatusWarn, Summary: "could not read free space", Detail: err.Error()}
	}
	s := humanBytes(free) + " free"
	if free < minFreeBytes {
		return Result{Status: StatusWarn, Summary: s + " — below " + humanBytes(minFreeBytes)}
	}
	return Result{Status: StatusOK, Summary: s}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func checkUpdate(ctx context.Context, env *Env) Result {
	if env.LatestVersion == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	cur := strings.TrimPrefix(env.Version, "v")
	if cur == "" || cur == "dev" || strings.Contains(cur, "-g") {
		return Result{Status: StatusSkip, Summary: "development build"}
	}
	latest, err := env.LatestVersion(ctx)
	if err != nil {
		return Result{Status: StatusWarn, Summary: "could not check for updates", Detail: err.Error()}
	}
	latest = strings.TrimPrefix(latest, "v")
	if latest == cur {
		return Result{Status: StatusOK, Summary: "up to date (" + cur + ")"}
	}
	return Result{Status: StatusWarn, Summary: fmt.Sprintf("%s available (installed %s)", latest, cur), FixID: FixUpdate}
}

func fixUpdate(ctx context.Context, env *Env, progress func(string)) error {
	return RunStreaming(ctx, progress, env.Executable, "update")
}
