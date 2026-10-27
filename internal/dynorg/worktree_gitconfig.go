package dynorg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A signing program is not in the set: a checkpoint already runs with
// signing off, and the merge is pinned the same way, so none is reached.
//
// programKey reports whether a configuration key (section lower-cased,
// subsection as written, variable lower-cased, as git prints it) names a
// program that the conductor's git operations could run.
func programKey(key string) bool {
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
	case "core":
		return !hasSub && (variable == "fsmonitor" || variable == "hookspath")
	}
	return false
}

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
		k, v, _ := strings.Cut(rec, "\n")
		entries = append(entries, [2]string{k, v})
	}
	return entries, nil
}

// refuseProgramConfig returns an error when the repository-local git
// configuration of dir (its common configuration, and the per-worktree one
// when extensions.worktreeConfig is on) defines a program git would run.
// It fails closed: a configuration that cannot be read is refused too.
func refuseProgramConfig(ctx context.Context, dir string) error {
	entries, err := localConfigKeys(ctx, dir, "--local")
	if err != nil {
		return fmt.Errorf("the repository's local git configuration could not be checked (%v); nothing was run", err)
	}
	for _, e := range entries {
		if strings.EqualFold(e[0], "extensions.worktreeconfig") {
			switch strings.ToLower(strings.TrimSpace(e[1])) {
			case "", "true", "yes", "on", "1":
				more, err := localConfigKeys(ctx, dir, "--worktree")
				if err != nil {
					return fmt.Errorf("the worktree's git configuration could not be checked (%v); nothing was run", err)
				}
				entries = append(entries, more...)
			}
		}
	}
	for _, e := range entries {
		if programKey(e[0]) {
			return fmt.Errorf("the repository's local git configuration names a program that git would run (%s); "+
				"nothing was run - ask the operator to remove that entry", boundText(e[0], 120))
		}
	}
	return nil
}
