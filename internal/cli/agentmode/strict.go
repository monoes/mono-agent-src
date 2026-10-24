package agentmode

import (
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/agentbox"
	"strings"
)

// The forbidden and runtime-dir lists are internal/agentbox's.
var (
	strictForbidden   = agentbox.StrictForbidden
	strictRuntimeDirs = agentbox.StrictRuntimeDirs
)

// StrictOptions are the extra binds a strict profile is generated with.
type StrictOptions struct {
	AllowRW, AllowRO []string
}

// shellQuote single-quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// strictPathExpr turns an --allow path ("~/x" or absolute) into a shell word.
func strictPathExpr(p string) (string, error) {
	if p == "" || strings.ContainsAny(p, "\x00\n\r") {
		return "", errors.New("empty path or a path with a control character")
	}
	switch {
	case strings.HasPrefix(p, "~/"):
		return `"$H"` + shellQuote("/"+strings.TrimPrefix(p, "~/")), nil
	case strings.HasPrefix(p, "/"):
		return shellQuote(p), nil
	}
	return "", fmt.Errorf("%q: give an absolute path or one starting with ~/", p)
}

// StrictBwrapProfile is the strict Linux profile: the home directory is empty (a tmpfs)
// except for the working directory, the runtimes' own config and the paths named, so
// whatever the agent writes elsewhere in it, and in /tmp and /run, disappears with the
// sandbox, and the agent sockets and session bus in /run and /tmp are not there.
func StrictBwrapProfile(o StrictOptions) (string, error) {
	var b strings.Builder
	b.WriteString(`#!/bin/sh
# monoagentcli agent-mode sandbox --runtime bwrap --strict
# Usage: sh this-script COMMAND [ARGS...]   run it from the project directory
# The filesystem is read-only except: the current directory, the agent runtimes' own
# config (` + strings.Join(strictRuntimeDirs, ", ") + `, when they exist) and the paths
# given with --allow-rw. $HOME, /tmp, /var/tmp and /run are empty tmpfs mounts otherwise: writes
# there vanish with the sandbox, and ssh/gpg agent sockets, the session bus and keyrings are
# not present. Own pid and ipc namespaces and session, no capabilities, and a cleared
# environment with an allowlist. Network is NOT unshared (loopback API).
# Writable binds are the residual: a git hook or editor task inside the current directory,
# and a hooks/settings change inside a runtime's config, outlive the sandbox.
set -eu
H=$(cd "$HOME" && pwd -P)
forbidden="` + strings.Join(strictForbidden, " ") + `"
deny() { echo "agent-mode: refusing $1: $2" >&2; exit 1; }
real() {
  if [ -d "$1" ]; then (cd "$1" && pwd -P); else
    d=$(dirname -- "$1"); echo "$(cd "$d" && pwd -P)/$(basename -- "$1")"
  fi
}
check() {
  r=$(real "$1")
  case "$r" in "$H"|/) deny "$1" "it is your home directory or the root";; esac
  case "$H" in "$r"/*) deny "$1" "it contains your home directory";; esac
  for f in $forbidden; do
    case "$r" in "$H/$f"|"$H/$f"/*) deny "$1" "it is under ~/$f";; esac
    case "$H/$f" in "$r"/*) deny "$1" "it contains ~/$f";; esac
  done
}
cwd=$(pwd -P)
check "$cwd"
n=$#
set -- "$@" --ro-bind / / --dev-bind /dev /dev --proc /proc --tmpfs /tmp --tmpfs /var/tmp --tmpfs /run \
  --unshare-pid --unshare-ipc --new-session --cap-drop ALL --clearenv
if [ -d /run/systemd/resolve ]; then set -- "$@" --ro-bind /run/systemd/resolve /run/systemd/resolve; fi
for v in ` + shellWords(envAllowlist) + `; do
  if eval "[ -n \"\${$v+x}\" ]"; then
    eval "val=\${$v}"
    set -- "$@" --setenv "$v" "$val"
  fi
done
set -- "$@" --setenv HOME "$H" --setenv MONOAGENT_SANDBOX bwrap-strict --tmpfs "$H" --bind "$cwd" "$cwd"
for p in ` + shellWords(strictRuntimeDirs) + `; do
  if [ -e "$H/$p" ]; then set -- "$@" --bind "$H/$p" "$H/$p"; fi
done
`)
	add := func(paths []string, flag, what string) error {
		for _, p := range paths {
			e, err := strictPathExpr(p)
			if err != nil {
				return fmt.Errorf("--%s: %w", what, err)
			}
			b.WriteString("a=" + e + "\ncheck \"$a\"\n")
			b.WriteString(`if [ -e "$a" ]; then set -- "$@" ` + flag + ` "$a" "$a"; else echo "agent-mode: $a does not exist, not bound" >&2; fi` + "\n")
		}
		return nil
	}
	if err := add(o.AllowRW, "--bind", "allow-rw"); err != nil {
		return "", err
	}
	if err := add(o.AllowRO, "--ro-bind", "allow-ro"); err != nil {
		return "", err
	}
	b.WriteString(`set -- "$@" --die-with-parent
i=0
while [ "$i" -lt "$n" ]; do
  set -- "$@" "$1"
  shift
  i=$((i + 1))
done
exec bwrap "$@"
`)
	return b.String(), nil
}
