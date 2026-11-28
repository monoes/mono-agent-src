package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// monomindIgnoreHeading starts the block monomind init appends to a .gitignore;
// the block has no end marker.
const monomindIgnoreHeading = "# monomind runtime state"

// monomindIgnoreRules are the lines that block can hold, in the order monomind
// writes them (those the file already covers are left out). A monomind that
// writes anything else gets its block left where it is.
var monomindIgnoreRules = []string{
	".monomind/sessions/", ".monomind/data/", ".monomind/metrics/", ".monomind/knowledge/",
	".monomind/*.json", ".monomind/*.jsonl", "**/.monomind/sessions/", "**/.monomind/*.json",
	"data/sessions/", "data/mastermind-*.json", "data/mastermind-*.jsonl", "**/.claude-flow/",
	"monomind.config.json", ".swarm/", ".claude/memory.db",
	".monomind/monoes-connection.json", ".monomind/dashboard-token", ".monomind/enable-terminal.json",
}

// monomindBlock returns the rules of the block that turns was into now, only
// when now is was followed by exactly one such block and nothing else.
func monomindBlock(was, now []byte) ([]string, bool) {
	tail, ok := bytes.CutPrefix(now, was)
	if !ok {
		return nil, false
	}
	head := "\n" + monomindIgnoreHeading + "\n"
	if len(was) > 0 && !bytes.HasSuffix(was, []byte("\n")) {
		head = "\n" + head
	}
	body, ok := strings.CutPrefix(string(tail), head)
	if !ok || !strings.HasSuffix(body, "\n") {
		return nil, false
	}
	had := map[string]bool{}
	for _, l := range strings.Split(string(was), "\n") {
		had[strings.TrimSpace(l)] = true
	}
	rules := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	next := 0 // each rule at most once, in monomind's order, and new to the file
	for _, r := range rules {
		i := slices.Index(monomindIgnoreRules[next:], r)
		if i < 0 || had[r] {
			return nil, false
		}
		next += i + 1
	}
	return rules, true
}

// ignoreFile is a .gitignore as it was before a setup ran.
type ignoreFile struct {
	path    string
	content []byte
	info    fs.FileInfo // of the path itself, so a link is seen as a link
}

// readIgnoreFile returns nil when there is no .gitignore to watch.
func readIgnoreFile(path string) *ignoreFile {
	info, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return &ignoreFile{path: path, content: content, info: info}
}

// keepGitignore takes back the block monomind appended to the .gitignore: in a
// git work tree its rules go to .git/info/exclude, so they still ignore what
// they ignored, and the file is cut back to its old length. Any other change,
// or a file that is not a plain file, is left completely alone and reported on
// w: it may be the user's. The result names what was done, for the notice.
func keepGitignore(ctx context.Context, w io.Writer, dir string, old *ignoreFile, isRepo bool) string {
	if old == nil {
		return ""
	}
	now, err := os.ReadFile(old.path)
	if err == nil && bytes.Equal(now, old.content) {
		return ""
	}
	leftAlone := func(why string) string {
		fmt.Fprintf(w, "note: .gitignore changed while the folder was set up and was left as it is (%s); check it\n", why)
		return ""
	}
	if err != nil {
		return leftAlone("it cannot be read: " + err.Error())
	}
	rules, ok := monomindBlock(old.content, now)
	if !ok {
		return leftAlone("the change is not just the block the setup adds")
	}
	if info, err := os.Lstat(old.path); err != nil || !old.info.Mode().IsRegular() || !info.Mode().IsRegular() {
		return leftAlone("it is not a plain file")
	}
	const left = "ignore rules at the end of .gitignore"
	if !isRepo {
		return left
	}
	moved := excludePatterns(strings.Join(rules, "\n"), repoPrefix(ctx, dir))
	if err := appendInfoExclude(ctx, dir, moved); err != nil {
		fmt.Fprintf(w, "note: could not move the new .gitignore lines to .git/info/exclude: %v\n", err)
		return left
	}
	if err := cutBack(old.path, now, int64(len(old.content))); err != nil {
		fmt.Fprintf(w, "note: .gitignore was left as it is: %v\n", err)
		return left
	}
	return "ignore rules (to .git/info/exclude, your .gitignore is as it was)"
}

// cutBack truncates the plain file at path to size, only while it still holds
// exactly want. Nothing is rewritten: the file keeps its mode and owner, and a
// file saved over the path meanwhile is another file, which this never touches.
func cutBack(path string, want []byte, size int64) error {
	seen, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !seen.Mode().IsRegular() {
		return fmt.Errorf("%s is not a plain file", path)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(seen, opened) {
		return fmt.Errorf("%s was replaced", path)
	}
	got, err := io.ReadAll(io.LimitReader(f, int64(len(want))+1))
	if err != nil {
		return err
	}
	// the size again, last thing: an append since the read must survive
	last, err := f.Stat()
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) || last.Size() != int64(len(want)) {
		return fmt.Errorf("%s changed again", path)
	}
	return f.Truncate(size)
}
