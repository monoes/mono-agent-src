// Package agentbox runs a turn's process tree inside a bubblewrap sandbox on Linux.
// The path lists here are the single source for the profile text `agent-mode sandbox`
// prints and for the argv this package builds.
package agentbox

// Paths below are relative to $HOME.

// ReadOnlyHomePaths are files and directories the compat profile lets the agent read but
// not change: code placed in them runs outside the sandbox at the next login or session.
var ReadOnlyHomePaths = []string{
	".bashrc", ".bash_profile", ".profile", ".zshrc",
	".config/systemd/user", ".config/autostart", ".local/share/applications", ".config/fish",
}

// HiddenHomeDirs are directories the compat profile replaces by an empty tmpfs when they exist.
var HiddenHomeDirs = []string{".ssh", ".config/gh", ".aws"}

// HiddenHomeFiles are files the compat profile replaces by /dev/null when they exist.
var HiddenHomeFiles = []string{".git-credentials", ".config/git/credentials", ".netrc"}

// EnvAllowlist is the only part of the caller's environment the sandboxed command sees
// (variables that are not set are skipped).
var EnvAllowlist = []string{
	"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM", "SHELL", "TMPDIR",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
	"MONOAGENT_API_URL", "MONOAGENT_API_TOKEN", "MONOAGENT_PROFILE",
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "CODEX_HOME", "CLAUDE_CONFIG_DIR",
}

// StrictForbidden are home-relative paths that an allowed path and the working directory
// may not be, be inside, or contain: state, credentials and every place that starts code
// at a later login or session.
var StrictForbidden = []string{
	".monoagent", ".ssh", ".gnupg", ".local/share/keyrings", ".local/share/kwalletd",
	".config/gh", ".config/git", ".aws", ".netrc", ".git-credentials",
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
	".zshrc", ".zshenv", ".zprofile", ".zlogin", ".pam_environment",
	".config/systemd", ".config/autostart", ".config/environment.d", ".config/fish",
	".local/share/applications", ".local/bin",
}

// StrictRuntimeDirs are bound back read-write when they exist: the agent runtimes keep
// their login and settings there, and a runtime that cannot read its own login cannot
// start. They are credentials too, and a hooks or settings change made in them outlives
// the sandbox: strict mode confines what an agent reaches, it does not make these safe.
var StrictRuntimeDirs = []string{".claude", ".claude.json", ".codex", ".config/opencode", ".gemini", ".config/github-copilot"}

// RuntimeConfig is what a boxed turn gets of one agent runtime's home configuration:
// Scratch directories are empty and disposable (the runtime writes its settings, caches and
// sessions there and they vanish with the turn), Auth files are bound back read-only so the
// runtime stays logged in. Other runtimes' directories are not bound at all.
type RuntimeConfig struct{ Scratch, Auth []string }

// RuntimeConfigs maps a runtime id to its configuration, relative to $HOME.
var RuntimeConfigs = map[string]RuntimeConfig{
	"claude":   {Scratch: []string{".claude"}, Auth: []string{".claude.json", ".claude/.credentials.json"}},
	"codex":    {Scratch: []string{".codex"}, Auth: []string{".codex/auth.json"}},
	"gemini":   {Scratch: []string{".gemini"}, Auth: []string{".gemini/oauth_creds.json", ".gemini/google_accounts.json"}},
	"opencode": {Scratch: []string{".config/opencode", ".local/share/opencode"}, Auth: []string{".local/share/opencode/auth.json"}},
	"copilot":  {Scratch: []string{".config/github-copilot"}, Auth: []string{".config/github-copilot/hosts.json", ".config/github-copilot/apps.json"}},
}

// RuntimeBinaries names the command each runtime id starts, to find where it is installed.
var RuntimeBinaries = map[string]string{
	"claude": "claude", "codex": "codex", "gemini": "gemini", "opencode": "opencode", "copilot": "copilot",
}

// BoxEnvDeny are allowlisted variables a boxed turn does not inherit: the box shares the
// host network, so an ambient API bearer would reach what the filesystem boundary hides.
// A caller that issues a scoped token for the turn passes it in PassEnv.
var BoxEnvDeny = map[string]bool{"MONOAGENT_API_URL": true, "MONOAGENT_API_TOKEN": true, "MONOAGENT_PROFILE": true}
