package dynorg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Repository-local git configuration can name a program that git runs
// during staging, checkout and merge. The conductor runs git as the
// operator in folders an agent can write (its worktree, and through the
// shared repository configuration the main repository), and git has no
// switch that turns such programs off. So before an operation that could
// reach one, refuseProgramConfig reads the repository-local configuration
// and refuses to go on when it defines one; the operator removes the entry.
//
// Only the definitions are refused. An attribute assignment in a
// .gitattributes file is harmless without a definition that gives it a
// program to run, so attributes are not scanned. The operator's own global
// and system configuration is not inspected either: the operator controls
// those files, and tools such as git-lfs legitimately live there.
//
// Nothing is cached: the configuration can change between two operations,
// so every operation asks again (one git call).

// A signing program is in the set too: signing is pinned off for the
// conductor's commits and merges, but the entry is still a program the
// repository names, and a pin is not relied on alone.
//
// programEntry reports whether a configuration entry (key with the section
// lower-cased, subsection as written and variable lower-cased, as git
// prints it) names a program that the conductor's git operations could
// run. set is false for a key written without a value, which git reads as
// true. The file system monitor and the hooks path are pinned off on every
// call, so an explicit off value is harmless and passes; anything else
// is refused.
func programEntry(key, value string, set bool) bool {
	first := strings.Index(key, ".")
	last := strings.LastIndex(key, ".")
	if first < 0 {
		return false
	}
	section, variable := strings.ToLower(key[:first]), strings.ToLower(key[last+1:])
	hasSub := last > first
	switch section {
	case "filter":
		return hasSub && (variable == "clean" || variable == "smudge" || variable == "process")
	case "merge":
		return hasSub && variable == "driver"
	case "diff":
		return (hasSub && (variable == "textconv" || variable == "command")) || (!hasSub && variable == "external")
	case "gpg":
		return variable == "program" || (hasSub && variable == "defaultkeycommand")
	case "extensions":
		return !hasSub && variable == "partialclone" && value != ""
	case "remote":
		if !hasSub {
			return false
		}
		switch variable {
		case "uploadpack", "receivepack", "vcs", "partialclonefilter":
			return !set || value != ""
		case "promisor":
			return !set || !isFalse(value, true)
		case "url", "pushurl":
			// A helper scheme in an address selects a program.
			return strings.Contains(value, "::")
		}
	case "credential":
		return variable == "helper" && (!set || value != "")
	case "url":
		// A rewritten URL that selects a remote helper runs its program.
		return hasSub && (variable == "insteadof" || variable == "pushinsteadof") &&
			(strings.Contains(key, "::") || strings.Contains(value, "::"))
	case "protocol":
		// Any policy other than an explicit never (or an empty value) is
		// refused, whatever the protocol.
		v := strings.TrimSpace(value)
		return variable == "allow" && (!set || !(v == "" || strings.EqualFold(v, "never")))
	case "trailer":
		return hasSub && variable == "command" && (!set || value != "")
	case "submodule":
		return hasSub && variable == "update" && strings.HasPrefix(strings.TrimSpace(value), "!")
	case "branch":
		// Merge options stored for a branch are added to git merge.
		return hasSub && variable == "mergeoptions" && (!set || value != "")
	case "core":
		if hasSub {
			return false
		}
		switch variable {
		case "sshcommand", "gitproxy", "askpass", "alternaterefscommand":
			return !set || value != ""
		case "fsmonitor":
			return !set || !isFalse(value, true)
		case "hookspath":
			return !set || value != ""
		}
	}
	return false
}

// printableKey is a configuration key made safe to show: the key is agent
// text, so only the section and the variable name are kept (a subsection
// or URL part can carry text that must not reach notices or logs),
// anything that is not printable is dropped, the length is bounded and the
// result is quoted.
func printableKey(k string) string {
	if first, last := strings.Index(k, "."), strings.LastIndex(k, "."); first >= 0 {
		if last > first {
			k = k[:first] + ".<subsection>." + k[last+1:]
		}
	}
	k = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, k)
	return strconv.Quote(boundText(k, 120))
}

// isFalse reports whether a git boolean value is explicitly false;
// emptyToo counts the empty string as false.
func isFalse(v string, emptyToo bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "no", "off", "0":
		return true
	case "":
		return emptyToo
	}
	return false
}

// noValue marks a key written without a value in localConfigKeys' result.
const noValue = "\x00"

// localConfigKeys lists the keys (and their values) of one scope of the
// repository-local configuration, includes resolved.
func localConfigKeys(ctx context.Context, dir, scope string) ([][2]string, error) {
	if scope == "--worktree" {
		// git fails on a per-worktree file that does not exist; there is
		// nothing to read then.
		p, err := gitRun(ctx, dir, "rev-parse", "--git-path", "config.worktree")
		if err != nil {
			return nil, err
		}
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return nil, nil
		}
	}
	out, err := gitRun(ctx, dir, "config", scope, "--includes", "--null", "--list")
	if err != nil {
		return nil, err
	}
	var entries [][2]string
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		k, v, ok := strings.Cut(rec, "\n")
		if !ok {
			v = noValue
		}
		entries = append(entries, [2]string{k, v})
	}
	return entries, nil
}

// configRefusal is a refusal of the program-config guard, so a caller can
// tell it from a git hiccup and say so.
type configRefusal struct{ msg string }

func (e *configRefusal) Error() string { return e.msg }

// isConfigRefusal reports whether err is a refusal of refuseProgramConfig.
func isConfigRefusal(err error) bool {
	var r *configRefusal
	return errors.As(err, &r)
}

// refuseProgramConfig returns an error when the repository-local git
// configuration of dir (its common configuration, and the per-worktree one
// when extensions.worktreeConfig is on) defines a program git would run.
// It fails closed: a configuration that cannot be read is refused too.
func refuseProgramConfig(ctx context.Context, dir string) error {
	entries, err := localConfigKeys(ctx, dir, "--local")
	if err != nil {
		return &configRefusal{fmt.Sprintf("the repository's local git configuration could not be checked (%v); nothing was run", err)}
	}
	for _, e := range entries {
		if strings.EqualFold(e[0], "extensions.worktreeconfig") {
			// git reads any non-zero integer as true, so the file is read
			// unless the value is plainly false; reading too often is harmless.
			if e[1] == noValue || !isFalse(e[1], false) {
				more, err := localConfigKeys(ctx, dir, "--worktree")
				if err != nil {
					return &configRefusal{fmt.Sprintf("the worktree's git configuration could not be checked (%v); nothing was run", err)}
				}
				entries = append(entries, more...)
			}
		}
	}
	for _, e := range entries {
		if programEntry(e[0], e[1], e[1] != noValue) {
			return &configRefusal{fmt.Sprintf("the repository's local git configuration names a program that git would run (%s); "+
				"nothing was run - ask the operator to remove that entry", printableKey(e[0]))}
		}
	}
	return nil
}
