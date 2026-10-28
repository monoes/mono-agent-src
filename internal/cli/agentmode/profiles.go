package agentmode

import (
	"strings"

	"github.com/monoes/mono-agent/internal/agentbox"
)

// The lists live in internal/agentbox, which builds the argv of a running turn from
// the same source; the aliases keep the profile text below readable.
var (
	readOnlyHomePaths = agentbox.ReadOnlyHomePaths
	hiddenHomeDirs    = agentbox.HiddenHomeDirs
	hiddenHomeFiles   = agentbox.HiddenHomeFiles
	envAllowlist      = agentbox.EnvAllowlist
)

// seatbeltWriteDeny are home paths the macOS profile makes unwritable (persistence).
var seatbeltWriteDeny = []struct {
	path   string
	subdir bool
}{
	{"Library/LaunchAgents", true},
	{".bashrc", false}, {".bash_profile", false}, {".profile", false},
	{".zshrc", false}, {".zprofile", false}, {".zshenv", false},
}

func shellWords(list []string) string {
	return strings.Join(list, " ")
}

func bwrapProfile() string {
	return `#!/bin/sh
# monoagentcli agent-mode sandbox --runtime bwrap
# Usage: sh this-script COMMAND [ARGS...]   e.g.  sh this-script claude
# COMPAT profile: best effort, not a boundary. It keeps the whole filesystem writable and
# hides or protects the paths listed below; places that start code at a later login cannot
# all be listed, so use --strict for stronger (still best-effort) hardening. Runs COMMAND with the filesystem
# writable as usual except:
#   - mono-agent's state, the Secret Service keyrings, kwalletd, gnupg and the D-Bus
#     session bus are hidden;
#   - ssh keys, gh, aws and git credentials are hidden;
#   - shell startup files and autostart/systemd-user/desktop-entry/fish locations are
#     read-only, so nothing planted there runs outside the sandbox later;
#   - host processes are not visible (own pid and ipc namespace), no controlling
#     terminal is shared, all capabilities are dropped;
#   - the environment is cleared and only the variables listed below are passed on
#     (MONOAGENT_API_URL and MONOAGENT_API_TOKEN, see "monoagentcli agent-mode env",
#     are among them).
# Network is NOT unshared: the agent reaches the daemon's API on loopback (see "agent
# mode" in SECURITY.md). If your session bus is an abstract socket (unix:abstract=...),
# file masking cannot hide it: add --unshare-net and reach the API through a socket you
# forward in.
set -eu
run="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
n=$#
set -- "$@" --bind / / --dev-bind /dev /dev --proc /proc --unshare-pid --unshare-ipc --new-session --cap-drop ALL --clearenv
for v in ` + shellWords(envAllowlist) + `; do
  if eval "[ -n \"\${$v+x}\" ]"; then
    eval "val=\${$v}"
    set -- "$@" --setenv "$v" "$val"
  fi
done
set -- "$@" --setenv MONOAGENT_SANDBOX bwrap
set -- "$@" \
  --tmpfs "$HOME/.monoagent" \
  --tmpfs "$HOME/.local/share/keyrings" \
  --tmpfs "$HOME/.local/share/kwalletd" \
  --tmpfs "$HOME/.gnupg" \
  --ro-bind /dev/null "$run/bus" \
  --tmpfs "$run/keyring"
for p in ` + shellWords(hiddenHomeDirs) + `; do
  if [ -d "$HOME/$p" ]; then set -- "$@" --tmpfs "$HOME/$p"; fi
done
for p in ` + shellWords(hiddenHomeFiles) + `; do
  if [ -f "$HOME/$p" ]; then set -- "$@" --ro-bind /dev/null "$HOME/$p"; fi
done
for p in ` + shellWords(readOnlyHomePaths) + `; do
  if [ -e "$HOME/$p" ]; then set -- "$@" --ro-bind "$HOME/$p" "$HOME/$p"; fi
done
set -- "$@" --unsetenv DBUS_SESSION_BUS_ADDRESS --unsetenv GNOME_KEYRING_CONTROL --die-with-parent
i=0
while [ "$i" -lt "$n" ]; do
  set -- "$@" "$1"
  shift
  i=$((i + 1))
done
exec bwrap "$@"
`
}

func seatbeltProfile() string {
	var b strings.Builder
	b.WriteString(`;; monoagentcli agent-mode sandbox --runtime seatbelt
;; Usage: sandbox-exec -D HOME="$HOME" -f this-file COMMAND [ARGS...]
;; Best effort, not a boundary. Denies read and write of mono-agent's state, the keychain
;; files, ~/.ssh, ~/.aws, ~/.config/gh, ~/.git-credentials and ~/.netrc, the Mach services
;; that hand out keychain items (which is what /usr/bin/security talks to), and write of
;; ~/Library/LaunchAgents and the shell startup files.
;; Everything else, loopback networking included, stays as it is. Not run on macOS by
;; the authors: check the rules and the service names on your macOS version with:
;; log stream --predicate 'sender == "Sandbox"' while the agent runs.
(version 1)
(allow default)
(deny file-read* file-write*
  (subpath (string-append (param "HOME") "/.monoagent"))
  (subpath (string-append (param "HOME") "/Library/Keychains"))
  (subpath (string-append (param "HOME") "/.ssh"))
  (subpath (string-append (param "HOME") "/.aws"))
  (subpath (string-append (param "HOME") "/.config/gh"))
  (literal (string-append (param "HOME") "/.git-credentials"))
  (literal (string-append (param "HOME") "/.netrc")))
(deny file-write*
`)
	for i, w := range seatbeltWriteDeny {
		kind := "literal"
		if w.subdir {
			kind = "subpath"
		}
		b.WriteString("  (" + kind + ` (string-append (param "HOME") "/` + w.path + `"))`)
		if i == len(seatbeltWriteDeny)-1 {
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	b.WriteString(`(deny mach-lookup
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.securityd")
  (global-name "com.apple.secd"))
`)
	return b.String()
}
