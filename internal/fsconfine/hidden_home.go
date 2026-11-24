package fsconfine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrUnapprovedPath is wrapped by the refusals GuardHiddenHome adds to Path
// and WritePath.
var ErrUnapprovedPath = errors.New("a workflow the user has not approved can't use this path")

// Guard is what GuardHiddenHome keeps a run to.
type Guard struct {
	// Project is the profile's project folder: a trusted zone (the user's
	// choice) where only git hooks and configs and the files that run on
	// next use (runsOnNextUse) stay closed. A folder
	// that is or holds the home, or that lies in a hidden folder of the home
	// other than ~/.monoagent/profiles, doesn't count as one.
	Project string
	// ReadOK are folders in a hidden folder of the home its steps may still
	// read (the synced message attachments); WriteOK they may read and write
	// (the CLI's export folder).
	ReadOK, WriteOK []string
	// Approve says how the user approves the workflow, for the refusals.
	Approve string
}

type homeGuardKey struct{}

// GuardHiddenHome marks ctx as a run of a workflow the user never approved
// (internal/workflow), or one such a workflow called. Its file steps then:
//   - neither read nor write a hidden file or folder of the user's home (a
//     name starting with ".", plus Library on macOS and AppData on
//     Windows): keys and tokens live there (~/.ssh, ~/.monoagent), and files
//     that run code later (a shell's startup file, an autostart or systemd
//     entry);
//   - read no process's environment, command line or memory
//     (/proc/*/environ, cmdline, mem), where the daemon's keys can be;
//   - write nothing into a folder on PATH (this process's, plus ~/bin,
//     ~/go/bin, ~/.local/bin, /usr/local/bin and /opt/homebrew/bin), where a
//     file runs as a command, nor a PowerShell profile folder on Windows;
//   - write no git hook or git config, anywhere (a git directory found by
//     name or by its HEAD, objects and refs): git runs them at the next
//     commit; nor, anywhere, a file a tool runs the next time the user works
//     in its folder (runsOnNextUse: .claude/settings*, .claude/hooks,
//     .claude/helpers, CLAUDE.md, .mcp.json, .husky, .githooks, .envrc).
//
// The project folder is open but for those (Guard).
func GuardHiddenHome(ctx context.Context, g Guard) context.Context {
	return context.WithValue(ctx, homeGuardKey{}, g)
}

// WritePath is Path for a file a node writes or creates: in a run
// GuardHiddenHome marked it also applies the rules for writes.
func WritePath(ctx context.Context, p string) (string, error) {
	r, err := confine(ctx, p)
	if err != nil {
		return "", err
	}
	if err := homeRule(ctx, p, r, true); err != nil {
		return "", err
	}
	return r, nil
}

// homeRule applies GuardHiddenHome's rules to p, which confine resolved to
// r. Symlinks and a relative path (to the working directory) are resolved
// first, so a link can't lead past them.
func homeRule(ctx context.Context, p, r string, write bool) error {
	g, guarded := ctx.Value(homeGuardKey{}).(Guard)
	if !guarded {
		return nil
	}
	approve := g.Approve
	if approve == "" {
		approve = "save or activate the workflow in the app or your terminal to let it"
	}
	refuse := func(why string, a ...interface{}) error {
		return fmt.Errorf("%w: %s; %s", ErrUnapprovedPath, fmt.Sprintf(why, a...), approve)
	}
	abs, err := filepath.Abs(r)
	if err != nil {
		return refuse("%s: %v", p, err)
	}
	resolved, err := resolveExisting(filepath.Clean(abs))
	if err != nil {
		return refuse("%s: %v", p, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return refuse("%s: no home folder to check against (%v)", p, err)
	}
	if procSecret(resolved) || procSecret(filepath.Clean(abs)) {
		return refuse("%s is a process's environment, command line or memory, where keys can be", p)
	}
	if systemSecret(resolved) {
		return refuse("%s is a system credential file (root's home, the password or sudo files)", p)
	}
	// $HOME can be overridden (a scratch HOME, a launcher, injected env); the account's own home
	// is guarded all the same.
	homes := []string{home}
	if u, err := user.Current(); err == nil && u.HomeDir != "" && u.HomeDir != home {
		homes = append(homes, u.HomeDir)
	}
	seen := map[string]bool{}
	var primary string
	for i, h := range homes {
		if r, err := filepath.EvalSymlinks(h); err == nil {
			h = r
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		if i == 0 {
			primary = h
		} else if in(primary, resolved) {
			continue // the overridden home already judged this path
		}
		if err := homeRuleAt(g, p, resolved, write, h, refuse); err != nil {
			return err
		}
	}
	return nil
}

// homeRuleAt is homeRule's checks against one home folder.
func homeRuleAt(g Guard, p, resolved string, write bool, home string, refuse func(string, ...interface{}) error) error {
	if write {
		if gitRunsIt(resolved) || inGitDir(resolved) {
			return refuse("%s is a git hook or git config, which git runs at the next commit", p)
		}
		if strings.EqualFold(filepath.Base(resolved), ".git") {
			return refuse("%s is a .git file, which can point git at another folder's hooks", p)
		}
		if what := runsOnNextUse(resolved); what != "" {
			return refuse("%s is %s, which runs the next time you work in that folder", p, what)
		}
		if onPath(home, filepath.Dir(resolved)) {
			return refuse("%s is in %s, a folder on PATH, where a file runs as a command", p, filepath.Dir(resolved))
		}
		if powerShellProfile(home, resolved) {
			return refuse("%s is in a PowerShell profile folder, which PowerShell runs at start", p)
		}
	}
	project := trustedProject(home, g.Project)
	if project != "" && in(project, resolved) {
		return nil
	}
	for _, d := range g.WriteOK {
		if dirHolds(d, resolved) {
			return nil
		}
	}
	if !write {
		for _, d := range g.ReadOK {
			if dirHolds(d, resolved) {
				return nil
			}
		}
	}
	if !in(home, resolved) {
		return nil
	}
	rel, _ := filepath.Rel(home, resolved)
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if strings.HasPrefix(part, ".") && part != "." || i == 0 && hiddenTop(part) {
			verb := "reads"
			if write {
				verb = "writes"
			}
			why := fmt.Sprintf("it %s no hidden file or folder in your home, and %s is in %s", verb, p, filepath.Join(home, filepath.Join(parts[:i+1]...)))
			if i == len(parts)-1 {
				why = fmt.Sprintf("it %s no hidden file or folder in your home, and %s is one", verb, p)
			}
			if note := untrustedProject(home, g.Project, project, resolved); note != "" {
				why += "; " + note
			}
			return refuse("%s", why)
		}
	}
	return nil
}

// trustedProject is project, resolved, when it counts as the project folder:
// not the home or a folder above it, and not in a hidden folder of the home
// other than ~/.monoagent/profiles (the default project folders). "" when it
// doesn't.
func trustedProject(home, project string) string {
	if project == "" {
		return ""
	}
	root, err := resolveExisting(filepath.Clean(project))
	if err != nil || in(root, home) {
		return ""
	}
	if !in(home, root) || in(filepath.Join(home, ".monoagent", "profiles"), root) {
		return root
	}
	rel, _ := filepath.Rel(home, root)
	for i, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") || i == 0 && hiddenTop(part) {
			return ""
		}
	}
	return root
}

// untrustedProject explains, for a path in the profile's folder project, why
// that folder opened nothing (trustedProject said no): "" when the path is
// not in it or the folder counts.
func untrustedProject(home, project, trusted, p string) string {
	if project == "" || trusted != "" {
		return ""
	}
	root, err := resolveExisting(filepath.Clean(project))
	if err != nil || !in(root, p) {
		return ""
	}
	if in(root, home) {
		return fmt.Sprintf("the profile's folder %s is your home or holds it, so it doesn't count as a project folder", project)
	}
	return fmt.Sprintf("the profile's folder %s lies in a hidden folder of your home, so it doesn't count as a project folder", project)
}

// dirHolds reports whether d, resolved, holds p.
func dirHolds(d, p string) bool {
	if d == "" {
		return false
	}
	if r, err := resolveExisting(filepath.Clean(d)); err == nil {
		d = r
	}
	return in(d, p)
}

// caseFolds reports a file system that compares names without case: Windows,
// and macOS's default.
var caseFolds = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// fold is name as the file system compares it.
func fold(name string) string {
	if caseFolds {
		return strings.ToLower(name)
	}
	return name
}

// in is within as the file system compares names.
func in(root, p string) bool { return within(fold(root), fold(p)) }

// hiddenTop reports a top-level home folder that holds startup entries
// without a leading dot on this OS.
func hiddenTop(name string) bool {
	switch runtime.GOOS {
	case "windows":
		return strings.EqualFold(name, "AppData")
	case "darwin":
		return strings.EqualFold(name, "Library")
	}
	return false
}

// gitRunsIt reports a path git runs or takes its commands from: anything in
// a hooks folder of a git directory (".git", a bare "*.git", and the
// modules and worktrees inside one), or a config file there (config,
// config.worktree). Names compare without case: git on a case-folding
// file system finds .GIT/HOOKS too.
func gitRunsIt(p string) bool {
	parts := strings.Split(strings.ToLower(p), string(filepath.Separator))
	for i, part := range parts {
		if part != ".git" && !strings.HasSuffix(part, ".git") {
			continue
		}
		for j := i + 1; j+1 < len(parts); j++ {
			if parts[j] == "hooks" {
				return true
			}
		}
		if len(parts) > i+1 && strings.HasPrefix(parts[len(parts)-1], "config") {
			return true
		}
	}
	return false
}

// runsOnNextUse names a file a tool runs, or takes commands from, the next
// time the user works in its folder, "" for any other: anything in Claude
// Code's .claude (settings and hooks, the scripts hooks run, commands,
// skills, agents), AI agents' instructions and configuration (CLAUDE.md,
// CLAUDE.local.md, AGENTS.md, GEMINI.md, .cursorrules, .cursor/rules,
// .cursor/mcp.json, .windsurfrules, .aider.conf.yml, .codex, .gemini,
// .continue, .github/copilot-instructions.md), project MCP servers
// (.mcp.json), editor tasks (.vscode/tasks.json, .zed), GitHub Actions
// workflows, git hook managers (.husky, .githooks) and direnv's .envrc.
//
// Names match without case on every system: the file reaches a CI service,
// an editor or a checkout on a system that folds case (CLOUDBUILD.YAML).
func runsOnNextUse(p string) string {
	parts := strings.Split(strings.ToLower(p), string(filepath.Separator))
	last := parts[len(parts)-1]
	for name, what := range nextUseFiles {
		if last == strings.ToLower(name) {
			return what + " (" + name + ")"
		}
	}
	for i := 0; i+1 < len(parts); i++ {
		dir, below := parts[i], parts[i+1]
		for name, what := range nextUseDirs {
			if dir == strings.ToLower(name) {
				return what + " (" + name + ")"
			}
		}
		switch dir {
		case ".github":
			switch below {
			case "copilot-instructions.md", "agents", "instructions", "prompts":
				return "Copilot's instructions, agents or prompts (.github)"
			case "workflows", "actions":
				return "a GitHub Actions workflow or action, which runs at the next push"
			}
		case ".gitlab":
			if below == "ci" {
				return "a GitLab CI pipeline's part (.gitlab/ci), which runs at the next push"
			}
		case ".vscode":
			if i+2 == len(parts) && (last == "tasks.json" || last == "launch.json" || last == "settings.json") {
				return "VS Code's tasks, launch configurations or settings (.vscode), which can run when the folder opens"
			}
		case ".bundle":
			if i+2 == len(parts) && last == "config" {
				return "Bundler's configuration (.bundle/config)"
			}
		case ".cargo":
			if i+2 == len(parts) && (last == "config" || last == "config.toml") {
				return "Cargo's configuration (.cargo), which can name the programs it runs"
			}
		case ".zed":
			if i+2 == len(parts) && (last == "settings.json" || last == "tasks.json" || last == "keymap.json") {
				return "Zed's settings, tasks or keymap (.zed)"
			}
		}
	}
	return ""
}

// onPath reports whether dir is a folder on PATH: this process's, plus the
// user folders a login shell often adds (~/bin, ~/go/bin, ~/.local/bin) and
// the ones a package manager keeps writable (/usr/local/bin,
// /opt/homebrew/bin).
func onPath(home, dir string) bool {
	entries := append(filepath.SplitList(os.Getenv("PATH")),
		filepath.Join(home, "bin"), filepath.Join(home, "go", "bin"), filepath.Join(home, ".local", "bin"))
	if runtime.GOOS != "windows" {
		entries = append(entries, "/usr/local/bin", "/opt/homebrew/bin")
	}
	for _, e := range entries {
		if e == "" || !filepath.IsAbs(e) {
			continue
		}
		if r, err := filepath.EvalSymlinks(e); err == nil {
			e = r
		}
		if fold(filepath.Clean(e)) == fold(dir) {
			return true
		}
	}
	return false
}

// powerShellProfile reports a file in a PowerShell profile folder of the
// home on Windows (Documents\WindowsPowerShell, Documents\PowerShell), which
// PowerShell runs at start; elsewhere its profile is in ~/.config.
func powerShellProfile(home, p string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	for _, d := range []string{"WindowsPowerShell", "PowerShell"} {
		if in(filepath.Join(home, "Documents", d), p) {
			return true
		}
	}
	return false
}

// procSecret reports a process's environment, command line or memory on
// Linux (/proc/<pid>/environ, cmdline, mem, or a thread's), where keys can
// be.
func procSecret(p string) bool {
	if runtime.GOOS != "linux" || !strings.HasPrefix(p, "/proc/") {
		return false
	}
	switch filepath.Base(p) {
	case "environ", "cmdline", "mem", "maps", "smaps":
		return true
	}
	// /proc/<pid>/fd/<n> opens whatever the process has open.
	return strings.Contains(p, "/fd/") || strings.HasSuffix(p, "/fd")
}

// inGitDir reports a hook or config of a git directory whatever its name: a
// folder with HEAD, objects and refs (a --separate-git-dir, or the one a
// worktree's .git file points to), which gitRunsIt can't tell by name.
func inGitDir(p string) bool {
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		if isGitDir(d) {
			rel, err := filepath.Rel(d, p)
			return err == nil && gitRunsIt(filepath.Join(".git", rel))
		}
		if parent := filepath.Dir(d); parent == d {
			return false
		}
	}
}

// isGitDir reports a git directory: objects and refs folders and a HEAD
// that reads as git writes it ("ref: refs/…" or an object name), so a folder
// that merely has those three names is none. Git needs no config file.
func isGitDir(d string) bool {
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Stat(filepath.Join(d, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	f, err := os.Open(filepath.Join(d, "HEAD"))
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	return gitHead(strings.TrimSpace(string(buf[:n])))
}

// gitHead reports whether head reads as a git HEAD: a symbolic ref, or a
// SHA-1 or SHA-256 object name.
func gitHead(head string) bool {
	if strings.HasPrefix(head, "ref: refs/") {
		return true
	}
	if len(head) != 40 && len(head) != 64 {
		return false
	}
	for _, c := range head {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
