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
// removed is reported and does not undo the delete. A copy is removed only if its folder, with links
// resolved, is still under the attachments folder, and a link is removed as itself, never followed.
func (f turnFiles) remove(warn func(format string, a ...any)) {
	for _, p := range f.logs {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			warn("could not remove %s: %v", p, err)
		}
	}
	if f.attachRoot == "" {
		return
	}
	root, err := filepath.EvalSymlinks(f.attachRoot)
	if err != nil {
		return
	}
	dirs := map[string]bool{}
	for _, p := range f.copies {
		dir, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil || !(dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))) {
			continue
		}
		target := filepath.Join(dir, filepath.Base(p))
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			warn("could not remove %s: %v", target, err)
			continue
		}
		if dir != root {
			dirs[dir] = true
		}
	}
	for d := range dirs {
		_ = os.Remove(d) // only while empty
	}
}

func warnOnStderr(format string, a ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...) }
