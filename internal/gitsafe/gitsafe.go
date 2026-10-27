// Package gitsafe is how this program runs git in a folder an agent can write to.
//
// A repository's own configuration, hooks and the process environment can each make git run
// a program: a file system monitor, a hook, a pager or editor, an external diff, a signature
// verifier (git asks for one while reading history when the configuration says so), an ssh
// command. A work folder is written by the agent, so none of them may run with the
// operator's privileges. Args pins the settings that name such a program to inert values for
// one invocation, and Env drops the environment variables that do the same. Neither touches
// the user's identity or safe.directory, which are read from the operator's own files.
//
// Not covered, because git offers no switch: programs named by a content filter or a merge
// driver (the repository's configuration together with its attributes). Callers that run
// git on content (add, checkout, merge) still honour those.
package gitsafe

import (
	"os"
	"strings"
)

// Args are the leading arguments (after -C dir, before the subcommand) of a hardened call.
func Args() []string {
	return []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.pager=cat",
		"-c", "core.editor=true",
		"-c", "core.askPass=true",
		"-c", "core.sshCommand=false",
		"-c", "core.gitProxy=false",
		"-c", "protocol.allow=never",
		"-c", "credential.helper=",
		"-c", "log.showSignature=false",
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "gpg.format=openpgp",
		"-c", "gpg.program=true",
		"-c", "gpg.openpgp.program=true",
		"-c", "gpg.x509.program=true",
		"-c", "gpg.ssh.program=true",
	}
}

// dropped are the variables that redirect git, inject configuration or name a program.
var dropped = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_CEILING_DIRECTORIES": true,
	"GIT_NAMESPACE": true, "GIT_REPLACE_REF_BASE": true, "GIT_NO_REPLACE_OBJECTS": true, "GIT_PREFIX": true,
	"GIT_CONFIG_COUNT": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true,
	"GIT_ASKPASS": true, "SSH_ASKPASS": true, "GIT_PROXY_COMMAND": true, "GIT_EXEC_PATH": true,
	"GIT_TRACE": true, "GIT_TRACE2": true, "GIT_TRACE2_EVENT": true, "GIT_TRACE2_PERF": true,
	"GIT_NO_LAZY_FETCH": true, "GIT_ALLOW_PROTOCOL": true,
}

// Env returns environ without the variables that redirect git, inject configuration
// (GIT_CONFIG_COUNT and its KEY/VALUE pairs) or name a program (GIT_SSH*, pager, editor,
// external diff). It then pins two more: GIT_NO_LAZY_FETCH=1, so a partial clone's missing object
// is an error rather than a fetch through the repository's own remote, and GIT_ALLOW_PROTOCOL=file,
// an explicit allowlist that the environment holds over every protocol.<name>.allow of the
// configuration (a more specific protocol.ext.allow=always still beats protocol.allow=never).
func Env(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		k = strings.ToUpper(k)
		if dropped[k] || strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_") ||
			k == "GIT_SSH" || k == "GIT_SSH_COMMAND" || k == "GIT_SSH_VARIANT" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=file")
}

// Environ is Env of this process's environment.
func Environ() []string { return Env(os.Environ()) }
