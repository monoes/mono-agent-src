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
	logs       []string
	copies     []string
	attachRoot string
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
		out.attachRoot = filepath.Join(filepath.Clean(conv.Cwd), ".monoagent", "attachments")
	}
	for cursor := ""; ; {
		turns, next, err := store.ListTurns(conversationID, profileID, cursor, 200)
		if err != nil {
			return out, chatStoreErr(err)
		}
		for _, t := range turns {
			if hostID.MatchString(t.ID) {
				dir := filepath.Join(hostDir(cfg), "turns")
				out.logs = append(out.logs, filepath.Join(dir, t.ID+".log"), filepath.Join(dir, t.ID+".prompt"))
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

// remove deletes the files, best effort: one that is gone already is fine, and one that cannot be
// removed is reported and does not undo the delete. The copies are removed through the attachments
// folder opened as a root: a path that would leave it through a link is refused by the system, a link
// is removed as itself, and nothing is followed. When the attachments folder is itself a link (or its
// parent is one that leads elsewhere than the conversation's folder), no copy is touched.
func (f turnFiles) remove(warn func(format string, a ...any)) {
	for _, p := range f.logs {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			warn("could not remove %s: %v", p, err)
		}
	}
	if f.attachRoot == "" || len(f.copies) == 0 {
		return
	}
	if fi, err := os.Lstat(f.attachRoot); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
		return
	}
	if real, err := filepath.EvalSymlinks(f.attachRoot); err != nil || real != filepath.Join(evalDir(filepath.Dir(filepath.Dir(f.attachRoot))), ".monoagent", "attachments") {
		return
	}
	root, err := os.OpenRoot(f.attachRoot)
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

// evalDir is dir with links resolved, or dir itself when it cannot be.
func evalDir(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

func warnOnStderr(format string, a ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...) }
