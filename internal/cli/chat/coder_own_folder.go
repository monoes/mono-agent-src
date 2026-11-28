package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
// keeps what that adds out of their way: every new top-level entry, and every
// file the setup reports it created inside a folder that was already there, is
// listed in .git/info/exclude (local to this clone). Lines the setup appends to
// an existing .gitignore are moved there too, so the user's file stays as it
// was. w is told what was added.
func initOwnFolder(ctx context.Context, w io.Writer, dir, target string) (*monomind.WorkspaceInit, error) {
	before, isRepo := untrackedTop(ctx, dir)
	filesBefore := untrackedFiles(ctx, dir)
	existing := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			existing[e.Name()] = true
		}
	}
	ignorePath := filepath.Join(dir, ".gitignore")
	ignore, ignoreErr := os.ReadFile(ignorePath)
	res, err := initWorkspace(ctx, "", dir, target)
	appended := ""
	if ignoreErr == nil {
		// also after a failed setup: it may have written before it failed
		appended = keepGitignore(ctx, w, dir, ignorePath, ignore, isRepo)
	}
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
	var lines []string
	inside := map[string]int{} // new files per top-level folder that was already there
	if isRepo {
		after, _ := untrackedTop(ctx, dir)
		prefix := repoPrefix(ctx, dir)
		// anchored at the work tree's top, so a folder below it needs its path in front
		for _, n := range added {
			if after[n] && !before[n] {
				lines = append(lines, "/"+escapeIgnore(prefix+n))
			}
		}
		for _, f := range createdInside(res, existing, filesBefore, untrackedFiles(ctx, dir)) {
			top, _, _ := strings.Cut(f, "/")
			inside[top]++
			lines = append(lines, "/"+escapeIgnore(prefix+f))
		}
		if err := appendInfoExclude(ctx, dir, lines); err != nil {
			fmt.Fprintf(w, "note: could not add the new files to .git/info/exclude: %v\n", err)
		}
	}
	for top, n := range inside {
		added = append(added, fmt.Sprintf("%d new file(s) inside %s", n, top))
	}
	sort.Strings(added[len(added)-len(inside):])
	if appended != "" {
		added = append(added, appended)
	}
	if len(added) == 0 {
		return res, nil
	}
	fmt.Fprintf(w, "monoagent added %s to %s so coder turns can run there", strings.Join(added, ", "), dir)
	if isRepo {
		fmt.Fprint(w, " (kept out of git status through .git/info/exclude)")
	}
	fmt.Fprintln(w)
	return res, nil
}

// keepGitignore undoes the block monomind appended to the .gitignore that held
// was; any other change is left alone, it may be the user's. In a git work tree they go to .git/info/exclude, so they still ignore what
// they ignored, and the file is put back; elsewhere, or when that cannot be
// done, the lines stay. The result names the change for the notice.
func keepGitignore(ctx context.Context, w io.Writer, dir, path string, was []byte, isRepo bool) string {
	now, err := os.ReadFile(path)
	if err != nil || len(now) <= len(was) || !bytes.HasPrefix(now, was) ||
		!bytes.HasPrefix(bytes.TrimSpace(now[len(was):]), []byte("# monomind")) {
		return ""
	}
	const left = "lines at the end of .gitignore"
	if !isRepo {
		return left
	}
	if err := appendInfoExclude(ctx, dir, excludePatterns(string(now[len(was):]), repoPrefix(ctx, dir))); err != nil {
		fmt.Fprintf(w, "note: could not move the new .gitignore lines to .git/info/exclude: %v\n", err)
		return left
	}
	if err := os.WriteFile(path, was, 0o644); err != nil {
		fmt.Fprintf(w, "note: could not put .gitignore back: %v\n", err)
		return left
	}
	return "ignore rules (to .git/info/exclude, your .gitignore is unchanged)"
}

// excludePatterns rewrites the lines of a .gitignore found at prefix below the
// work tree's top ("" is the top) so that they mean the same in info/exclude.
func excludePatterns(ignore, prefix string) []string {
	var out []string
	for _, l := range strings.Split(ignore, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if prefix != "" {
			neg := ""
			if strings.HasPrefix(l, "!") {
				neg, l = "!", l[1:]
			}
			if strings.Contains(strings.TrimSuffix(l, "/"), "/") {
				l = strings.TrimPrefix(l, "/")
			} else {
				l = "**/" + l // a bare name matches at any depth below its .gitignore
			}
			l = neg + "/" + escapeIgnore(prefix) + l
		}
		out = append(out, l)
	}
	return out
}

// createdInside lists the files that became untracked during the setup, below a
// top-level entry that was already there, and that the setup itself reports as
// created (the file, or a folder above it). Anything else that appeared
// meanwhile is the user's and stays visible.
func createdInside(res *monomind.WorkspaceInit, existing, before, after map[string]bool) []string {
	if res == nil || before == nil {
		return nil
	}
	created := map[string]bool{}
	for _, c := range res.Created {
		created[path.Clean(c)] = true
	}
	var out []string
	for f := range after {
		top, _, nested := strings.Cut(f, "/")
		if before[f] || !nested || !existing[top] {
			continue
		}
		for p := f; p != "."; p = path.Dir(p) {
			if created[p] {
				out = append(out, f)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// untrackedFiles lists every untracked, non-ignored file below dir, relative to
// it (nil outside a work tree).
func untrackedFiles(ctx context.Context, dir string) map[string]bool {
	out, err := gitsafe.Command(ctx, "-C", dir, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil
	}
	files := map[string]bool{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files[f] = true
		}
	}
	return files
}

// escapeIgnore makes a path match itself in an ignore file.
func escapeIgnore(p string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(p)
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
	// a second chat on the folder must not list the same patterns again
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(old), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var missing []string
	for _, l := range lines {
		if !have[l] {
			missing = append(missing, l)
		}
	}
	if lines = missing; len(lines) == 0 {
		return nil
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
