// Package agentmode is `monoagentcli agent-mode`: how to run a coding agent so that
// it cannot read this user's credentials, and use mono-agent through the daemon's
// profile-scoped HTTP API instead of running the CLI as the user.
//
// The OS key stores do not separate one process of a user from another, so only an
// OS boundary keeps an agent away from ~/.monoagent and the key store (SECURITY.md,
// "A copy of ~/.monoagent and the key store"). This package prints the profile of that
// boundary; it reads and writes nothing of the user's.
package agentmode

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/docs"
	"github.com/monoes/mono-agent/internal/storage"
)

// Runtime names a sandbox the profile is written for.
type Runtime string

const (
	Bwrap      Runtime = "bwrap"
	Seatbelt   Runtime = "seatbelt"
	ClaudeCode Runtime = "claude-code"
	// Windows has no profile: see ErrWindows.
	Windows Runtime = "windows"
)

// Runtimes lists the runtimes that have a profile, in the order help shows them.
var Runtimes = []Runtime{Bwrap, Seatbelt, ClaudeCode}

// WindowsDoc is the command that prints the Windows recipe (a separate low-privilege account, the
// daemon API with a scoped token, WSL2), which is embedded in the binary.
const WindowsDoc = "monoagentcli agent-mode windows"

// ErrWindows is the answer for --runtime windows. Windows has no sandbox primitive that a
// command line can start a process in without installing something (AppContainer needs a
// packaged app or a Win32 API program, Windows Sandbox is a separate disposable VM, and
// neither can run an agent against the files of one folder), so no profile is printed
// rather than one that pretends.
var ErrWindows = errors.New("agent-mode has no Windows profile: Windows has no sandbox that a command can start a process in " +
	"without extra installs, and a profile that only looks like one would be worse than none. Use one of: " +
	"(1) WSL2 - run the agent and monoagentcli inside a WSL distribution and use --runtime bwrap there " +
	"(the agent then cannot see the Windows user's ~/.monoagent, but mono-agent's data must live in the distribution to be used); " +
	"(2) a separate Windows user account for the agent (it cannot read your profile folder) that reaches the API on 127.0.0.1 with an `agent-mode token`; " +
	"(3) Windows Sandbox or a VM, with the API forwarded in. See \"Agent mode\" in SECURITY.md and run \"" + WindowsDoc + "\" for the steps")

// The profiles contain no path or value of the caller, so nothing can be injected.
// Paths come from $HOME (bwrap), a -D parameter (seatbelt) or ~ (Claude Code) at the
// time the sandbox starts. The bwrap and seatbelt text is generated from the lists in
// profiles.go, so the printed profile has one source.

const claudeCodeProfile = `{
  "_comment": "monoagentcli agent-mode sandbox --runtime claude-code. Merge into .claude/settings.json (or settings.local.json). Check the keys against your Claude Code version.",
  "sandbox": {
    "enabled": true,
    "filesystem": {
      "denyRead": [
        "~/.monoagent",
        "~/Library/Keychains",
        "~/.local/share/keyrings",
        "~/.local/share/kwalletd"
      ]
    }
  },
  "permissions": {
    "deny": [
      "Read(~/.monoagent/**)",
      "Read(~/Library/Keychains/**)",
      "Read(~/.local/share/keyrings/**)"
    ]
  }
}
`

// Profile returns the sandbox profile for rt.
func Profile(rt Runtime) (string, error) {
	switch rt {
	case Bwrap:
		return bwrapProfile(), nil
	case Seatbelt:
		return seatbeltProfile(), nil
	case ClaudeCode:
		return claudeCodeProfile, nil
	}
	if rt == Windows {
		return "", ErrWindows
	}
	return "", fmt.Errorf("unknown runtime %q (want one of %v, or windows for the explanation why there is none)", rt, Runtimes)
}

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag.
	JSONOutput func() bool
	// ProfileID is the profile named by --profile.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound and InvalidInput build the CLI's exit-code errors (2 and 3).
	NotFound     func(format string, a ...interface{}) error
	InvalidInput func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// Refusal is operator.Refusal: why this process is not the user's own terminal ("" when
	// it is). A token is a credential for the profile, so only the user makes, revokes or
	// reads one out. Nil refuses.
	Refusal func() string
	// APIAddr is the host:port the daemon's HTTP API listens on (httpapi.ResolveAddr).
	APIAddr func() string
	// StateDir is ~/.monoagent; only `status` looks at it.
	StateDir func() string
}

const longHelp = `Agent mode keeps a coding agent away from ~/.monoagent, the OS key store and the D-Bus session bus, and gives it the daemon's HTTP API with a scoped token instead of the CLI run as you. See "Agent mode" in SECURITY.md.

Start an agent on Linux under the generated bwrap profile, with a read-only token (run all of this
OUTSIDE the sandbox; the eval hands the token to the agent through its environment):

  monoagentcli --profile work agent-mode token create --name reviewer --scope read --expires 24h
  eval "$(monoagentcli --profile work agent-mode env --token reviewer)"
  monoagentcli agent-mode sandbox --runtime bwrap > agent-sandbox.sh
  sh agent-sandbox.sh claude

That is the --compat profile (the default for now): best effort that reduces what a cooperative agent does by
accident, not a boundary. For a real separation use "agent-mode sandbox --runtime bwrap --strict" and run the
script from the project directory (see "monoagentcli agent-mode sandbox --help").

The agent then calls $MONOAGENT_API_URL/workflows with "Authorization: Bearer $MONOAGENT_API_TOKEN".
Scopes: read = GET only; write = also validate and other non-send changes; run = also run and
(de)activate workflows. Vault secrets, sends, approvals and the account are never reachable by a token.

Still deferred, and not done here: user-presence unlock for the vault, the macOS keychain ACL
(needs a signed binary), and hiding an abstract-socket D-Bus session bus under --unshare-net.
Windows has no profile (agent-mode sandbox --runtime windows says why and what to use; the recipe is in
"monoagentcli agent-mode windows" prints the recipe, and "monoagentcli doctor --agent-account NAME" checks it).`

// NewCommand is `agent-mode`.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent-mode",
		Short: "Run a coding agent without your credentials: a sandbox profile, scoped tokens and the HTTP API",
		Long:  longHelp,
	}
	cmd.AddCommand(newWindowsCmd(), newSandboxCmd(), d.newTokenCmd(), d.newEnvCmd(), d.newStatusCmd())
	return cmd
}

func newSandboxCmd() *cobra.Command {
	var runtime string
	var strict, compat bool
	var allowRW, allowRO []string
	c := &cobra.Command{
		Use:   "sandbox",
		Short: "Print a sandbox profile that hides mono-agent's state and the key store from an agent",
		Long: "Prints, to stdout, a profile for bwrap (Linux), seatbelt (macOS sandbox-exec) or Claude Code's " +
			"sandbox settings. Nothing is read from or written to your mono-agent data. --runtime windows " +
			"prints no profile: it fails with the reason and what to use instead.\n\n" +
			"bwrap has two profiles. --strict is the supported separation: the home directory, /tmp and /run are empty " +
			"and only the current directory, the agent runtimes' own config and the paths named with --allow-rw/--allow-ro " +
			"are bound back, so nothing the agent writes elsewhere outlives it and no agent socket is reachable. " +
			"--compat (the default for now) keeps the whole filesystem writable and hides a list of paths: best effort that " +
			"reduces what a cooperative agent does by accident, not a boundary, because places that start code later " +
			"(startup files, autostart, ~/.local/bin, git hooks) cannot all be listed.",
		Example: "  monoagentcli agent-mode sandbox --runtime bwrap --strict > agent-sandbox.sh\n" +
			"  cd ~/projects/x && sh ~/agent-sandbox.sh claude",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strict && compat {
				return errors.New("--strict and --compat exclude each other")
			}
			if !strict && (len(allowRW) > 0 || len(allowRO) > 0) {
				return errors.New("--allow-rw and --allow-ro need --strict")
			}
			var out string
			var err error
			if strict {
				if Runtime(runtime) != Bwrap {
					return errors.New("--strict is for --runtime bwrap")
				}
				out, err = StrictBwrapProfile(StrictOptions{AllowRW: allowRW, AllowRO: allowRO})
			} else {
				out, err = Profile(Runtime(runtime))
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		},
	}
	c.Flags().StringVar(&runtime, "runtime", string(Bwrap), "sandbox to write the profile for: bwrap, seatbelt, claude-code (windows is refused with guidance)")
	c.Flags().BoolVar(&strict, "strict", false, "bwrap: deny-by-default home, /tmp and /run; only the current directory, runtime config and --allow-* paths are bound back")
	c.Flags().BoolVar(&compat, "compat", false, "bwrap: the best-effort profile that hides a list of paths (the default for now)")
	c.Flags().StringArrayVar(&allowRW, "allow-rw", nil, "with --strict: bind this path read-write (~/x or absolute; repeatable; ~/.ssh, ~/.monoagent, startup files and similar are refused)")
	c.Flags().StringArrayVar(&allowRO, "allow-ro", nil, "with --strict: bind this path read-only (same rules as --allow-rw; repeatable)")
	return c
}

func newWindowsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "windows",
		Short: "Print the guide to running an agent on Windows (a separate low-privilege account)",
		Long:  "Prints the guide embedded in this binary: why a separate account is the boundary on Windows, the steps, WSL2 as an alternative, and what it does not protect against. It reads nothing and works offline.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), docs.AgentModeWindows)
			return err
		},
	}
}
