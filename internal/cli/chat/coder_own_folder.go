package chat

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/gitsafe"
	"github.com/monoes/mono-agent/internal/monomind"
)

// untrackedTop lists the top-level untracked, non-ignored entries of the git
// work tree at dir (directories collapsed); ok is false outside a work tree.
func untrackedTop(ctx context.Context, dir string) (names map[string]bool, ok bool) {
	out, err := gitsafe.Command(ctx, "-C", dir, "ls-files", "--others", "--exclude-standard", "--directory", "--no-empty-directory").Output()
	if err != nil {
		return nil, false
	}
	names = map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSuffix(strings.TrimSpace(l), "/"); l != "" && !strings.Contains(l, "/") {
			names[l] = true
		}
	}
	return names, true
}

// initOwnFolder sets up a folder the user picked as a monomind project and
// keeps what that adds out of their way: every top-level entry the setup newly
// created is listed in .git/info/exclude (local to this clone, never the
// user's tracked .gitignore), and w is told which entries were added.
func initOwnFolder(ctx context.Context, w io.Writer, dir, target string) (*monomind.WorkspaceInit, error) {
	before, isRepo := untrackedTop(ctx, dir)
	existing := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			existing[e.Name()] = true
		}
	}
	res, err := initWorkspace(ctx, "", dir, target)
	if err != nil {
		return res, err
	}
	var added []string
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !existing[e.Name()] {
				added = append(added, e.Name())
			}
		}
	}
	sort.Strings(added)
	if len(added) == 0 {
		return res, nil
	}
	if isRepo {
		after, _ := untrackedTop(ctx, dir)
		prefix := repoPrefix(ctx, dir)
		var lines []string
		for _, n := range added {
			if after[n] && !before[n] {
				// anchored at the work tree's top, so a folder below it needs its path in front
				lines = append(lines, "/"+prefix+n)
			}
		}
		if err := appendInfoExclude(ctx, dir, lines); err != nil {
			fmt.Fprintf(w, "note: could not add the new files to .git/info/exclude: %v\n", err)
		}
	}
	fmt.Fprintf(w, "monoagent added %s to %s so coder turns can run there", strings.Join(added, ", "), dir)
	if isRepo {
		fmt.Fprint(w, " (kept out of git status through .git/info/exclude)")
	}
	fmt.Fprintln(w)
	return res, nil
}

// repoPrefix is dir's path below the work tree's top, with a trailing slash ("" at the top).
func repoPrefix(ctx context.Context, dir string) string {
	out, err := gitsafe.Command(ctx, "-C", dir, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func appendInfoExclude(ctx context.Context, dir string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	out, err := gitsafe.Command(ctx, "-C", dir, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return err
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// a file that does not end in a newline would swallow our comment into its last pattern
	lead := ""
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		last := make([]byte, 1)
		if rf, err := os.Open(p); err == nil {
			_, _ = rf.ReadAt(last, st.Size()-1)
			rf.Close()
			if last[0] != '\n' {
				lead = "\n"
			}
		}
	}
	_, err = f.WriteString(lead + "# added by monoagent for coder chats\n" + strings.Join(lines, "\n") + "\n")
	return err
}
