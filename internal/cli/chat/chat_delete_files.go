package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// turnFiles are the files a conversation's turns left outside the database: the coder host's log and
// prompt file of each turn, and the copies of attached files made in the conversation's folder.
type turnFiles struct {
	logDir     string   // the host's turns folder
	logs       []string // file names in it
	cwd        string   // the conversation's folder; "" when it has none
	attachRoot string   // <cwd>/.monoagent/attachments, as a path (what the recorded copies are checked against)
	copies     []string // recorded copy paths under attachRoot
}

// collectTurnFiles reads what the conversation's turns left. A copy counts only if the host recorded
// it in a turn's started event and it lies under <folder>/.monoagent/attachments: a file the user
// had in the folder, or one that is named from elsewhere, is never on the list.
func collectTurnFiles(cfg *Deps, store *ai.AIStore, profileID, conversationID string) (turnFiles, error) {
	var out turnFiles
	conv, err := store.GetConversation(conversationID, profileID)
	if err != nil {
		return out, chatStoreErr(err)
	}
	if conv.Cwd != "" {
		out.cwd = filepath.Clean(conv.Cwd)
		out.attachRoot = filepath.Join(out.cwd, ".monoagent", "attachments")
	}
	for cursor := ""; ; {
		turns, next, err := store.ListTurns(conversationID, profileID, cursor, 200)
		if err != nil {
			return out, chatStoreErr(err)
		}
		for _, t := range turns {
			if hostID.MatchString(t.ID) {
				out.logDir = filepath.Join(hostDir(cfg), "turns")
				out.logs = append(out.logs, t.ID+".log", t.ID+".prompt")
			}
			if out.attachRoot == "" {
				continue
			}
			for after := int64(0); ; {
				page, err := store.GetEvents(conversationID, t.ID, profileID, after, 1000)
				if err != nil {
					return out, chatStoreErr(err)
				}
				for _, ev := range page {
					var p chatevents.TurnStartedPayload
					if ev.Type != chatevents.EventTurnStarted || json.Unmarshal(ev.Payload, &p) != nil {
						continue
					}
					for _, a := range p.Attachments {
						if c := filepath.Clean(a.Path); strings.HasPrefix(c, out.attachRoot+string(filepath.Separator)) {
							out.copies = append(out.copies, c)
						}
					}
				}
				if len(page) < 1000 {
					break
				}
				after = page[len(page)-1].Seq
			}
		}
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// afterLstat is a seam for tests: it runs between a folder being inspected and being opened.
var afterLstat = func(path string) {}

// openPinnedDir opens dir as a root, and only if it is the folder that was inspected: it must be a
// real folder (not a link) when looked at, and the folder opened must be that same one. A folder
// swapped for a link between the two is refused, and once it is open the handle names the folder
// whatever happens to the path after.
func openPinnedDir(dir string) (*os.Root, error) {
	lst, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !lst.IsDir() {
		return nil, fmt.Errorf("%s is not a plain folder", dir)
	}
	afterLstat(dir)
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	st, err := r.Stat(".")
	if err != nil || !os.SameFile(lst, st) {
		r.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", dir)
	}
	return r, nil
}

// openPinnedSub is openPinnedDir for the folder name inside parent.
func openPinnedSub(parent *os.Root, name, full string) (*os.Root, error) {
	lst, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !lst.IsDir() {
		return nil, fmt.Errorf("%s is not a plain folder", full)
	}
	afterLstat(full)
	r, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	st, err := r.Stat(".")
	if err != nil || !os.SameFile(lst, st) {
		r.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", full)
	}
	return r, nil
}

// tombstone is one file renamed out of the way: orig is where it was, tomb what it is called now.
type tombstone struct {
	root       *os.Root
	orig, tomb string
}

// quarantined is what quarantine moved aside, with the pinned folders it did so through.
type quarantined struct {
	items []tombstone
	roots []*os.Root
}

// quarantine renames every file of the list to a name of its own, inside the pinned folders (see
// openPinnedDir): a rename is cheap, so it can run inside the delete's write transaction, where no
// turn can be admitted and no id can be given to a new turn. Nothing is removed yet: finish removes
// the renamed files once the delete has committed, and puts them back if it did not. A link is
// renamed as itself and nothing is followed; the conversation's folder itself is the conversation's
// own and is opened as it is. What cannot be opened or renamed is reported and left where it is.
func (f turnFiles) quarantine(warn func(format string, a ...any)) *quarantined {
	q := &quarantined{}
	suffix := ".deleting-" + uuid.NewString()
	if f.logDir != "" && len(f.logs) > 0 {
		if root, err := openPinnedDir(f.logDir); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				warn("could not open %s: %v", f.logDir, err)
			}
		} else {
			q.roots = append(q.roots, root)
			for _, name := range f.logs {
				q.rename(root, name, name+suffix, filepath.Join(f.logDir, name), warn)
			}
		}
	}
	if f.cwd == "" || len(f.copies) == 0 {
		return q
	}
	cwd, err := os.OpenRoot(f.cwd)
	if err != nil {
		return q
	}
	defer cwd.Close()
	meta, err := openPinnedSub(cwd, ".monoagent", filepath.Join(f.cwd, ".monoagent"))
	if err != nil {
		return q
	}
	defer meta.Close()
	root, err := openPinnedSub(meta, "attachments", f.attachRoot)
	if err != nil {
		return q
	}
	q.roots = append(q.roots, root)
	for _, p := range f.copies {
		rel, err := filepath.Rel(f.attachRoot, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		q.rename(root, rel, rel+suffix, p, warn)
	}
	return q
}

func (q *quarantined) rename(root *os.Root, orig, tomb, shown string, warn func(string, ...any)) {
	if err := root.Rename(orig, tomb); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warn("could not set aside %s: %v", shown, err)
		}
		return
	}
	q.items = append(q.items, tombstone{root, orig, tomb})
}

// finish ends what quarantine began. After a delete that committed the renamed files are removed
// (and the folder a turn's copies were in, when it is empty); after one that did not they are put
// back. Best effort: what cannot be removed or restored is reported.
func (q *quarantined) finish(committed bool, warn func(format string, a ...any)) {
	if q == nil {
		return
	}
	dirs := map[*os.Root]map[string]bool{}
	for _, it := range q.items {
		if committed {
			if err := it.root.Remove(it.tomb); err != nil && !errors.Is(err, fs.ErrNotExist) {
				warn("could not remove %s: %v", it.tomb, err)
				continue
			}
			if d := filepath.Dir(it.orig); d != "." {
				if dirs[it.root] == nil {
					dirs[it.root] = map[string]bool{}
				}
				dirs[it.root][d] = true
			}
			continue
		}
		if err := it.root.Rename(it.tomb, it.orig); err != nil {
			warn("could not put %s back: %v", it.orig, err)
		}
	}
	for root, ds := range dirs {
		for d := range ds {
			_ = root.Remove(d) // only while empty
		}
	}
	for _, r := range q.roots {
		r.Close()
	}
}

func warnOnStderr(format string, a ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...) }
