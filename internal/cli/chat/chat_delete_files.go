package chat

import (
	"encoding/json"
	"errors"
	"fmt"
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

// remove deletes the files, best effort: one that is gone already is fine, and one that cannot be
// removed is reported. Every folder is opened as a pinned root first (see openPinnedDir) and the
// removal goes through that handle, so a folder or a link swapped in later cannot redirect it; a
// link is removed as itself and nothing is followed. The conversation's folder itself is the
// conversation's own and is opened as it is.
func (f turnFiles) remove(warn func(format string, a ...any)) {
	if f.logDir != "" && len(f.logs) > 0 {
		if root, err := openPinnedDir(f.logDir); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				warn("could not open %s: %v", f.logDir, err)
			}
		} else {
			for _, name := range f.logs {
				if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
					warn("could not remove %s: %v", filepath.Join(f.logDir, name), err)
				}
			}
			root.Close()
		}
	}
	if f.cwd == "" || len(f.copies) == 0 {
		return
	}
	cwd, err := os.OpenRoot(f.cwd)
	if err != nil {
		return
	}
	defer cwd.Close()
	meta, err := openPinnedSub(cwd, ".monoagent", filepath.Join(f.cwd, ".monoagent"))
	if err != nil {
		return
	}
	defer meta.Close()
	root, err := openPinnedSub(meta, "attachments", f.attachRoot)
	if err != nil {
		return
	}
	defer root.Close()
	dirs := map[string]bool{}
	for _, p := range f.copies {
		rel, err := filepath.Rel(f.attachRoot, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			warn("could not remove %s: %v", p, err)
			continue
		}
		if d := filepath.Dir(rel); d != "." {
			dirs[d] = true
		}
	}
	for d := range dirs {
		_ = root.Remove(d) // only while empty
	}
}

func warnOnStderr(format string, a ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...) }
