package doomsday

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// A chat message that a stop could not write is owed. The first stop records that in a small file in the run directory (next
// to the stop marker and the unit file, the same per-generation records), BEFORE it reports the failure, so the next stop
// can write exactly that message and clear the record. The record names the run (charter, generation, the time its stop was
// recorded) so another run's or another profile's file is never honoured (the directory is the profile's own), and it is
// honoured only inside the repair window. It holds the message text, never anything the chat did not already get to see.

type pendingNotice struct {
	Charter    string            `json:"charter"`
	Generation int64             `json:"generation"`
	IntentAt   int64             `json:"intent_at"`
	Chats      map[string]string `json:"chats"` // conversation id -> the message owed
}

func (w Wiring) noticePath(gen int64) string {
	return filepath.Join(w.RunDir.Dir, fmt.Sprintf("%d.notice-pending", gen))
}

// readPending is the record of run (empty when there is none, or when it names another run).
func (w Wiring) readPending(run RunRef, intentAt int64) pendingNotice {
	if w.RunDir.Dir == "" {
		return pendingNotice{}
	}
	b, err := os.ReadFile(w.noticePath(run.Generation))
	if err != nil {
		return pendingNotice{}
	}
	var p pendingNotice
	if json.Unmarshal(b, &p) != nil || p.Charter != run.CharterID || p.Generation != run.Generation || p.IntentAt != intentAt {
		return pendingNotice{}
	}
	return p
}

// writePending records that msg is owed to chat conv; it replaces the file atomically (temp file, then rename).
func (w Wiring) writePending(run RunRef, intentAt int64, conv, msg string) error {
	if w.RunDir.Dir == "" {
		return errors.New("no run directory to record the owed message in")
	}
	p := w.readPending(run, intentAt)
	if p.Chats == nil {
		p = pendingNotice{Charter: run.CharterID, Generation: run.Generation, IntentAt: intentAt, Chats: map[string]string{}}
	}
	p.Chats[conv] = msg
	return w.putPending(run.Generation, p)
}

// clearPending removes chat conv from the record, and the file when nothing is left.
func (w Wiring) clearPending(run RunRef, intentAt int64, conv string) error {
	p := w.readPending(run, intentAt)
	delete(p.Chats, conv)
	if len(p.Chats) == 0 {
		if err := os.Remove(w.noticePath(run.Generation)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return w.putPending(run.Generation, p)
}

func (w Wiring) putPending(gen int64, p pendingNotice) error {
	if err := os.MkdirAll(w.RunDir.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(w.RunDir.Dir, fmt.Sprintf(".%d.notice-*", gen))
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return werr
	}
	if err := os.Rename(tmp.Name(), w.noticePath(gen)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// pendingChats are the chats owed a message, sorted.
func (p pendingNotice) pendingChats() []string {
	out := make([]string, 0, len(p.Chats))
	for c := range p.Chats {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
